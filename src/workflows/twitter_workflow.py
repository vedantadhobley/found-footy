"""
Twitter Workflow - Fire-and-Forget Video Discovery Pipeline

Orchestrates Twitter video search with 10 attempts, 1-minute spacing.
Downloads are FIRE-AND-FORGET to maximize video capture window.

Design Philosophy:
- This workflow runs for ~10 minutes (10 attempts x 1 min spacing)
- Downloads are fire-and-forget (start_child_workflow with ABANDON policy)
- _twitter_complete is set by DownloadWorkflow (or this workflow for no-video attempts)
- More frequent searches = fresher videos = better content

Race Condition Prevention:
- Problem: Fixture could move to fixtures_completed while downloads still running
- Solution: _twitter_complete is only set when ALL 10 attempts have finished processing
- Each DownloadWorkflow increments _twitter_count when complete
- If no videos found, this workflow increments _twitter_count directly
- When count reaches 10, _twitter_complete is set atomically

Why Fire-and-Forget Downloads:
- Old approach (3 x 3min blocking): Only 3 search windows, missed lots of content
- New approach (10 x 1min fire-and-forget): 10 search windows, captures more videos
- Dedup happens per-batch AND against MongoDB's _s3_videos (populated by prior downloads)
- Race window is small since searches are 1min apart and downloads take time
- Worst case: occasional duplicate (better than zero videos!)

Dedup Flow (per DownloadWorkflow):
1. fetch_event_data reads existing _s3_videos hashes from MongoDB
2. Download ALL videos in batch
3. MD5 dedup (exact duplicates within batch + against S3)
4. AI validation (only MD5-unique videos)
5. Perceptual hash generation IN PARALLEL (only AI-validated videos)
6. Perceptual dedup (batch + against MongoDB's _s3_videos)
7. Upload to S3, update MongoDB

Flow:
1. ATTEMPT 1: Search (3min window) → dedupe → fire-and-forget Download
2. WAIT 1 minute (START-to-START spacing)
... repeat for 10 attempts ...
10. All downloads complete → _twitter_complete=true set by last finisher
11. Notify frontend

Graceful Termination:
- At start of each attempt, checks if event still exists in MongoDB
- If event was deleted (VAR/count hit 0), terminates early
- Prevents wasted work on events that no longer exist

Started by: RAGWorkflow (fire-and-forget, runs independently)
Starts: DownloadWorkflow (fire-and-forget with ABANDON policy)
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
    from src.workflows.download_workflow import DownloadWorkflow
    from src.utils.event_enhancement import extract_player_search_name


@dataclass
class TwitterWorkflowInput:
    """Input for TwitterWorkflow"""
    fixture_id: int
    event_id: str
    player_name: Optional[str]  # Can be None for events without player info
    team_aliases: List[str]  # ["Liverpool", "LFC", "Reds"] or just ["Liverpool"]


@workflow.defn
class TwitterWorkflow:
    """
    Fire-and-forget Twitter search workflow with 10 attempts.
    
    Downloads are fire-and-forget (ABANDON policy) for faster video capture.
    Deduplication still works because each DownloadWorkflow:
    1. Reads existing _s3_videos from MongoDB at start
    2. Dedupes within batch (MD5 + perceptual)
    3. Dedupes against MongoDB's S3 videos (from prior downloads)
    4. AI validates videos before expensive perceptual hashing
    
    Each attempt:
    1. Builds search queries from player name + each team alias
    2. Runs searches for all aliases (3-minute window)
    3. Deduplicates within batch AND against existing videos
    4. STARTS DownloadWorkflow (fire-and-forget, doesn't wait)
    5. Waits 1 minute before next attempt (except after last)
    
    _twitter_complete handling:
    - DownloadWorkflow increments _twitter_count when complete
    - If no videos found, this workflow increments directly
    - When count reaches 10, _twitter_complete=true is set atomically
    - This prevents fixture from moving to completed while downloads run
    
    Expected duration: ~10 minutes
    """
    
    @workflow.run
    async def run(self, input: TwitterWorkflowInput) -> dict:
        """
        Execute 10 Twitter search attempts with 1-minute spacing.
        
        Args:
            input: TwitterWorkflowInput with fixture_id, event_id, player_name, team_aliases
        
        Returns:
            Dict with total_videos_found, attempts completed
        """
        total_videos_found = 0
        total_videos_uploaded = 0
        
        # Get player search name (handles accents, hyphens, "Jr" suffixes, etc.)
        player_search = extract_player_search_name(input.player_name) if input.player_name else "Unknown"
        
        workflow.logger.info(
            f"🐦 [TWITTER] STARTED | event={input.event_id} | "
            f"player_search='{player_search}' | aliases={input.team_aliases}"
        )
        
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
                f"player='{player_search}' | aliases={input.team_aliases}"
            )
            
            # =================================================================
            # Update attempt counter in MongoDB
            # =================================================================
            try:
                await workflow.execute_activity(
                    twitter_activities.update_twitter_attempt,
                    args=[input.fixture_id, input.event_id, attempt],
                    start_to_close_timeout=timedelta(seconds=30),
                    retry_policy=RetryPolicy(maximum_attempts=3),
                )
                workflow.logger.info(
                    f"✅ [TWITTER] Updated attempt counter | event={input.event_id} | attempt={attempt}"
                )
            except Exception as e:
                workflow.logger.warning(
                    f"⚠️ [TWITTER] update_twitter_attempt FAILED | event={input.event_id} | "
                    f"error={e} | Continuing anyway"
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
            
            for alias in input.team_aliases:
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
                        start_to_close_timeout=timedelta(seconds=180),  # Browser automation can be slow
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
                
                team_clean = input.team_aliases[0].replace(" ", "_").replace(".", "_").replace("-", "_") if input.team_aliases else "Unknown"
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
                        args=[input.fixture_id, input.event_id, input.player_name, input.team_aliases[0] if input.team_aliases else "", videos_to_download],
                        id=download_workflow_id,
                        parent_close_policy=ParentClosePolicy.ABANDON,  # Continue even if parent closes
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

