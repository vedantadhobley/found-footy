"""
Twitter Workflow - Scheduled Video Discovery Pipeline

Orchestrates Twitter video search with 10 attempts on a STRICT 1-minute schedule.
All searches are spawned on a timer, each runs independently.

Design Philosophy:
- Resolves team aliases at start (cache lookup or full RAG pipeline)
- Searches spawn at T+0, T+60, T+120, T+180... (strict schedule)
- Each search + download runs as a unit
- Downloads are BLOCKING child workflows that delegate to UploadWorkflow
- UploadWorkflow serializes S3 operations per event (deterministic ID)
- _twitter_complete is set by DownloadWorkflow (or this workflow for no-video attempts)
- Strict scheduling means searches happen at predictable times regardless of processing

Race Condition Prevention:
- Problem: Multiple downloads could upload the same video to S3
- Solution: UploadWorkflow with ID `upload-{event_id}` serializes uploads per event
- Temporal ensures only ONE UploadWorkflow runs at a time per event
- Each sees fresh S3 state inside the serialized context

_twitter_complete Tracking:
- Each DownloadWorkflow increments _twitter_count when complete
- If no videos found, this workflow increments _twitter_count directly
- When count reaches 10, _twitter_complete is set atomically
- Fixture can't move to completed until _twitter_complete=true

Dedup Flow (per DownloadWorkflow → UploadWorkflow):
1. DownloadWorkflow: Download → MD5 batch dedup → AI validation → Perceptual hash
2. UploadWorkflow (serialized): Fetch FRESH S3 state → MD5 dedup vs S3 → Perceptual dedup vs S3
3. Upload new/better quality videos → Update MongoDB → Recalculate ranks

Flow:
1. Resolve team aliases (cache or RAG pipeline) - blocking, ~30-90s
2. Start 10 search attempts at T+0, T+60, T+120...
3. Each search runs: Search → Save → Download (blocking) → Upload (serialized)
4. Downloads increment _twitter_count when done
5. When count reaches 10, _twitter_complete is set

Graceful Termination:
- Each search child checks if event still exists before searching
- If event was deleted (VAR/count hit 0), that search exits early
- Other searches continue on schedule

Started by: MonitorWorkflow (fire-and-forget when _monitor_complete=true)
Starts: DownloadWorkflow (blocking per search attempt) → UploadWorkflow (serialized)
"""
from temporalio import workflow
from temporalio.common import RetryPolicy
from datetime import timedelta
from dataclasses import dataclass
from typing import List, Optional

with workflow.unsafe.imports_passed_through():
    from src.activities import twitter as twitter_activities
    from src.activities import monitor as monitor_activities
    from src.activities import download as download_activities
    from src.activities import rag as rag_activities
    from src.workflows.download_workflow import DownloadWorkflow
    from src.utils.event_enhancement import extract_player_search_name


@dataclass
class TwitterWorkflowInput:
    """Input for TwitterWorkflow"""
    fixture_id: int
    event_id: str
    team_id: int                    # API-Football team ID (for alias cache lookup)
    team_name: str                  # "Liverpool" (fallback if no aliases)
    player_name: Optional[str]      # Can be None for events without player info


@workflow.defn
class TwitterWorkflow:
    """
    Scheduled Twitter search workflow with 10 attempts on STRICT 1-minute intervals.
    
    Spawns all 10 searches as parallel async tasks, each with its own timer.
    Search 1 starts immediately, Search 2 after 60s, Search 3 after 120s, etc.
    This ensures searches happen at predictable times regardless of processing time.
    
    Downloads are BLOCKING child workflows that call UploadWorkflow:
    1. DownloadWorkflow downloads, validates, and generates hashes
    2. UploadWorkflow (ID: upload-{event_id}) serializes S3 operations
    3. Only ONE UploadWorkflow runs at a time per event
    4. Each sees fresh S3 state, eliminating race conditions
    
    _twitter_complete handling:
    - DownloadWorkflow increments _twitter_count when complete
    - If no videos found, this workflow increments directly
    - When count reaches 10, _twitter_complete=true is set atomically
    - This prevents fixture from moving to completed while downloads run
    
    Expected duration: ~10 minutes (9 x 60s intervals + final search time)
    """
    
    @workflow.run
    async def run(self, input: TwitterWorkflowInput) -> dict:
        """
        Execute 10 Twitter search attempts on a strict 1-minute schedule.
        
        First resolves team aliases (cache or RAG), then:
        - Attempt 1: starts at T+0
        - Attempt 2: starts at T+60
        - Attempt 3: starts at T+120
        - etc.
        
        Args:
            input: TwitterWorkflowInput with fixture_id, event_id, team_id, team_name, player_name
        
        Returns:
            Dict with total_videos_found, attempts completed
        """
        # Get player search name (handles accents, hyphens, "Jr" suffixes, etc.)
        player_search = extract_player_search_name(input.player_name) if input.player_name else "Unknown"
        
        workflow.logger.info(
            f"🐦 [TWITTER] STARTED | event={input.event_id} | "
            f"team_id={input.team_id} | team_name='{input.team_name}' | "
            f"player_search='{player_search}'"
        )
        
        # =========================================================================
        # Step 0: Resolve team aliases (cache lookup or full RAG pipeline)
        # This is a blocking call - we need aliases before searching
        # =========================================================================
        workflow.logger.info(f"🔍 [TWITTER] Resolving aliases for team_id={input.team_id}")
        
        team_aliases = None
        cache_hit = False
        
        # Try cache first (pre-computed during ingestion)
        try:
            team_aliases = await workflow.execute_activity(
                rag_activities.get_cached_team_aliases,
                input.team_id,
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(
                    maximum_attempts=3,
                    initial_interval=timedelta(seconds=2),
                    backoff_coefficient=2.0,
                ),
            )
            if team_aliases:
                cache_hit = True
                workflow.logger.info(f"📦 [TWITTER] Alias cache HIT | aliases={team_aliases}")
        except Exception as e:
            workflow.logger.warning(f"⚠️ [TWITTER] Cache lookup failed | error={e}")
        
        # Cache miss - do full RAG lookup
        if not team_aliases:
            workflow.logger.info(f"🔄 [TWITTER] Cache MISS | Running full RAG pipeline...")
            try:
                team_aliases = await workflow.execute_activity(
                    rag_activities.get_team_aliases,
                    args=[input.team_id, input.team_name],
                    start_to_close_timeout=timedelta(seconds=90),
                    retry_policy=RetryPolicy(
                        maximum_attempts=3,
                        initial_interval=timedelta(seconds=5),
                        backoff_coefficient=2.0,
                    ),
                )
                workflow.logger.info(f"✅ [TWITTER] RAG SUCCESS | aliases={team_aliases}")
            except Exception as e:
                workflow.logger.error(f"❌ [TWITTER] RAG FAILED | error={e}")
                # Fallback to just team name
                team_aliases = [input.team_name]
                workflow.logger.warning(f"⚠️ [TWITTER] Using FALLBACK | aliases={team_aliases}")
        
        # Save aliases to event for debugging
        try:
            await workflow.execute_activity(
                rag_activities.save_team_aliases,
                args=[input.fixture_id, input.event_id, team_aliases],
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(maximum_attempts=2),
            )
            workflow.logger.info(f"💾 [TWITTER] Aliases saved | aliases={team_aliases}")
        except Exception as e:
            workflow.logger.warning(f"⚠️ [TWITTER] Failed to save aliases | error={e}")
        
        workflow.logger.info(
            f"🐦 [TWITTER] Starting 10 search attempts | event={input.event_id} | "
            f"player='{player_search}' | aliases={team_aliases} | cache_hit={cache_hit}"
        )
        
        # Track cumulative stats across all attempts
        total_videos_found = 0
        total_videos_uploaded = 0
        
        for attempt in range(1, 11):  # Attempts 1-10
            # =================================================================
            # Check if event still exists (graceful termination for VAR/deleted)
            # =================================================================
            workflow.logger.info(
                f"🔍 [TWITTER] Attempt {attempt}/10 | Checking event exists | event={input.event_id}"
            )
            
            try:
                event_check = await workflow.execute_activity(
                    twitter_activities.check_event_exists,
                    args=[input.fixture_id, input.event_id],
                    start_to_close_timeout=timedelta(seconds=30),
                    retry_policy=RetryPolicy(
                        maximum_attempts=3,
                        initial_interval=timedelta(seconds=2),
                        backoff_coefficient=2.0,
                    ),
                )
                workflow.logger.info(
                    f"✅ [TWITTER] Event check complete | event={input.event_id} | exists={event_check.get('exists')}"
                )
            except Exception as e:
                workflow.logger.warning(
                    f"⚠️ [TWITTER] check_event_exists FAILED | event={input.event_id} | "
                    f"error={e} | Assuming event exists, continuing"
                )
                event_check = {"exists": True}
            
            if not event_check.get("exists", False):
                workflow.logger.warning(
                    f"⚠️ [TWITTER] Event NO LONGER EXISTS | event={input.event_id} | "
                    f"Terminating workflow early (VAR reversal or deletion)"
                )
                return {
                    "fixture_id": input.fixture_id,
                    "event_id": input.event_id,
                    "total_videos_found": total_videos_found,
                    "total_videos_uploaded": total_videos_uploaded,
                    "attempts_completed": attempt - 1,
                    "terminated_early": True,
                    "reason": "event_deleted",
                }
            
            # Record attempt start time for "START to START" 1-minute spacing
            attempt_start = workflow.now()
            workflow.logger.info(
                f"🐦 [TWITTER] Attempt {attempt}/10 STARTING | event={input.event_id} | "
                f"player='{player_search}' | aliases={team_aliases}"
            )
            
            # =================================================================
            # Get existing video URLs (for deduplication)
            # =================================================================
            workflow.logger.info(
                f"📋 [TWITTER] Fetching existing video URLs | event={input.event_id}"
            )
            
            try:
                search_data = await workflow.execute_activity(
                    twitter_activities.get_twitter_search_data,
                    args=[input.fixture_id, input.event_id],
                    start_to_close_timeout=timedelta(seconds=30),
                    retry_policy=RetryPolicy(maximum_attempts=3),
                )
                existing_urls = search_data.get("existing_video_urls", [])
                match_date = search_data.get("match_date", "")
                workflow.logger.info(
                    f"✅ [TWITTER] Got search data | event={input.event_id} | "
                    f"existing_urls={len(existing_urls)} | match_date={match_date[:10] if match_date else 'N/A'}"
                )
            except Exception as e:
                workflow.logger.error(
                    f"❌ [TWITTER] get_twitter_search_data FAILED | event={input.event_id} | error={e}"
                )
                existing_urls = []
                match_date = ""
            
            # =================================================================
            # Run searches for each alias, collect all videos
            # =================================================================
            all_videos = []
            seen_urls_this_batch = set()
            
            for alias in team_aliases:
                search_query = f"{player_search} {alias}"
                workflow.logger.info(
                    f"🔍 [TWITTER] Searching | query='{search_query}' | "
                    f"excluding={len(existing_urls) + len(seen_urls_this_batch)} URLs"
                )
                
                try:
                    # Search with 3-minute window (fresher videos for more frequent searches)
                    search_result = await workflow.execute_activity(
                        twitter_activities.execute_twitter_search,
                        args=[search_query, 5, list(existing_urls) + list(seen_urls_this_batch), match_date, 3],
                        start_to_close_timeout=timedelta(seconds=60),  # Actual searches take ~6s each
                        retry_policy=RetryPolicy(
                            maximum_attempts=3,
                            initial_interval=timedelta(seconds=10),
                            backoff_coefficient=1.5,
                        ),
                    )
                    
                    videos = search_result.get("videos", [])
                    workflow.logger.info(
                        f"✅ [TWITTER] Search complete | query='{search_query}' | found={len(videos)} videos"
                    )
                    
                    # Dedupe within this batch (by URL)
                    for video in videos:
                        url = video.get("video_page_url") or video.get("url", "")
                        if url and url not in seen_urls_this_batch:
                            seen_urls_this_batch.add(url)
                            all_videos.append(video)
                            
                except Exception as e:
                    workflow.logger.warning(
                        f"⚠️ [TWITTER] Search FAILED | query='{search_query}' | error={e} | "
                        f"Continuing with other aliases"
                    )
                    continue
            
            video_count = len(all_videos)
            total_videos_found += video_count
            workflow.logger.info(
                f"📹 [TWITTER] Attempt {attempt} search complete | event={input.event_id} | "
                f"unique_videos={video_count} | total_found_so_far={total_videos_found}"
            )
            
            # =================================================================
            # Save discovered videos to MongoDB
            # =================================================================
            if all_videos:
                workflow.logger.info(
                    f"💾 [TWITTER] Saving {video_count} URLs to _discovered_videos | event={input.event_id}"
                )
                try:
                    await workflow.execute_activity(
                        twitter_activities.save_discovered_videos,
                        args=[input.fixture_id, input.event_id, all_videos],
                        start_to_close_timeout=timedelta(seconds=30),
                        retry_policy=RetryPolicy(
                            maximum_attempts=3,
                            initial_interval=timedelta(seconds=2),
                            backoff_coefficient=2.0,
                        ),
                    )
                    workflow.logger.info(
                        f"✅ [TWITTER] Saved URLs | event={input.event_id} | count={video_count}"
                    )
                except Exception as e:
                    workflow.logger.error(
                        f"❌ [TWITTER] save_discovered_videos FAILED | event={input.event_id} | error={e}"
                    )
            
            # =================================================================
            # Execute DownloadWorkflow - MUST WAIT for completion
            # This is critical for data integrity
            # =================================================================
            if all_videos:
                # Sort by duration (longest first) and take top 5 to reduce processing time
                # Videos without duration go to the end (treated as 0)
                MAX_VIDEOS_TO_DOWNLOAD = 5
                sorted_videos = sorted(
                    all_videos, 
                    key=lambda v: v.get("duration_seconds") or 0, 
                    reverse=True
                )
                videos_to_download = sorted_videos[:MAX_VIDEOS_TO_DOWNLOAD]
                
                # Log what we're selecting
                if len(all_videos) > MAX_VIDEOS_TO_DOWNLOAD:
                    workflow.logger.info(
                        f"📊 [TWITTER] Selecting top {MAX_VIDEOS_TO_DOWNLOAD} longest videos from {len(all_videos)} found | "
                        f"durations: {[v.get('duration_seconds', 0) for v in videos_to_download]}"
                    )
                
                team_clean = team_aliases[0].replace(" ", "_").replace(".", "_").replace("-", "_") if team_aliases else "Unknown"
                # Don't include video count in workflow ID - it causes nondeterminism when code changes
                download_workflow_id = f"download{attempt}-{team_clean}-{player_search}-{input.event_id}"
                
                workflow.logger.info(
                    f"⬇️ [TWITTER] Starting DownloadWorkflow (FIRE-AND-FORGET) | "
                    f"download_id={download_workflow_id} | videos={len(videos_to_download)}"
                )
                
                try:
                    # START (fire-and-forget) - not EXECUTE (wait)
                    # Downloads run independently, dedup against MongoDB's _s3_videos
                    from temporalio.workflow import ParentClosePolicy
                    await workflow.start_child_workflow(
                        DownloadWorkflow.run,
                        args=[input.fixture_id, input.event_id, input.player_name, team_aliases[0] if team_aliases else "", videos_to_download],
                        id=download_workflow_id,
                        parent_close_policy=ParentClosePolicy.ABANDON,  # Continue even if parent closes
                        # Increase task timeout from 10s to 60s - large histories need more
                        # time to replay, otherwise we get "Task not found" errors
                        task_timeout=timedelta(seconds=60),
                    )
                    
                    workflow.logger.info(
                        f"🚀 [TWITTER] Download STARTED (fire-and-forget) | download_id={download_workflow_id}"
                    )
                    
                except Exception as e:
                    workflow.logger.error(
                        f"❌ [TWITTER] Failed to START DownloadWorkflow | download_id={download_workflow_id} | "
                        f"error={e} | Continuing to next attempt"
                    )
                    # Download failed to start - still need to increment count
                    # so we reach 10 and mark complete
                    try:
                        await workflow.execute_activity(
                            download_activities.increment_twitter_count,
                            args=[input.fixture_id, input.event_id, 10],
                            start_to_close_timeout=timedelta(seconds=30),
                            retry_policy=RetryPolicy(maximum_attempts=3),
                        )
                    except Exception as inc_e:
                        workflow.logger.error(
                            f"❌ [TWITTER] increment_twitter_count also FAILED | error={inc_e}"
                        )
            else:
                # No videos found - we still need to increment the counter
                # (normally DownloadWorkflow does this, but we didn't start one)
                workflow.logger.info(
                    f"📭 [TWITTER] No new videos found | event={input.event_id} | attempt={attempt}"
                )
                try:
                    increment_result = await workflow.execute_activity(
                        download_activities.increment_twitter_count,
                        args=[input.fixture_id, input.event_id, 10],
                        start_to_close_timeout=timedelta(seconds=30),
                        retry_policy=RetryPolicy(maximum_attempts=3),
                    )
                    if increment_result.get("marked_complete"):
                        workflow.logger.info(
                            f"🏁 [TWITTER] All 10 attempts complete (no downloads pending) | event={input.event_id}"
                        )
                except Exception as e:
                    workflow.logger.error(
                        f"❌ [TWITTER] increment_twitter_count FAILED | error={e} | event={input.event_id}"
                    )
            
            # =================================================================
            # Wait for next attempt (1 minute from START of this attempt)
            # =================================================================
            if attempt < 10:
                elapsed = (workflow.now() - attempt_start).total_seconds()
                wait_seconds = max(60 - elapsed, 10)  # 1 min minus elapsed, min 10s
                workflow.logger.info(
                    f"⏳ [TWITTER] Attempt {attempt} took {elapsed:.0f}s | "
                    f"Waiting {wait_seconds:.0f}s before attempt {attempt + 1} | "
                    f"event={input.event_id}"
                )
                await workflow.sleep(timedelta(seconds=wait_seconds))
        
        # =================================================================
        # TwitterWorkflow complete - but downloads may still be running!
        # _twitter_complete is set by the LAST thing to finish:
        # - If downloads are pending: last DownloadWorkflow sets it
        # - If no downloads: we already set it via increment_twitter_count above
        # =================================================================
        workflow.logger.info(
            f"✅ [TWITTER] All 10 search attempts COMPLETE | event={input.event_id} | "
            f"Downloads still running will set _twitter_complete when done"
        )
        
        # NOTE: Temp directory cleanup happens at FIXTURE level when fixture moves to completed
        # This avoids race conditions with uploads that may still be processing
        
        # Notify frontend (best-effort, non-critical)
        try:
            await workflow.execute_activity(
                monitor_activities.notify_frontend_refresh,
                start_to_close_timeout=timedelta(seconds=15),
                retry_policy=RetryPolicy(maximum_attempts=2),
            )
            workflow.logger.info(f"📡 [TWITTER] Frontend notified | event={input.event_id}")
        except Exception as e:
            workflow.logger.warning(
                f"⚠️ [TWITTER] Frontend notification FAILED | event={input.event_id} | error={e}"
            )
        
        workflow.logger.info(
            f"🎉 [TWITTER] WORKFLOW COMPLETE | event={input.event_id} | "
            f"total_found={total_videos_found} | "
            f"attempts=10 (downloads may still be running)"
        )
        
        return {
            "fixture_id": input.fixture_id,
            "event_id": input.event_id,
            "total_videos_found": total_videos_found,
            "total_videos_uploaded": "N/A (fire-and-forget)",
            "attempts_completed": 10,
        }

