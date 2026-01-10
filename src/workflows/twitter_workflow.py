"""
Twitter Workflow - Self-Managing Video Discovery Pipeline

Orchestrates Twitter video search with 3 attempts, 3-minute spacing.
MUST wait for Download workflows to complete before marking _twitter_complete.

Design Philosophy:
- This workflow runs for ~12-15 minutes (3 attempts + 2 waits of 3 min)
- MUST wait for each Download workflow to complete (data integrity requirement)
- Marks _twitter_complete=true ONLY after all downloads finish
- Fixture completion depends on _twitter_complete flag

Why we WAIT for Downloads:
- Download workflows upload videos and update MongoDB
- If we fire-and-forget, fixture could move to completed before downloads finish
- This would cause lookups in wrong collection → data loss
- Each attempt's Download MUST complete before we continue

Flow:
1. ATTEMPT 1: Search all aliases → dedupe → wait for Download to complete
2. WAIT 3 minutes (durable timer, START-to-START spacing)
3. ATTEMPT 2: Search all aliases → dedupe → wait for Download to complete
4. WAIT 3 minutes
5. ATTEMPT 3: Search all aliases → dedupe → wait for Download to complete
6. Mark _twitter_complete=true (ONLY after all downloads done)
7. Notify frontend

Graceful Termination:
- At start of each attempt, checks if event still exists in MongoDB
- If event was deleted (VAR/count hit 0), terminates early
- Prevents wasted work on events that no longer exist

Started by: RAGWorkflow (fire-and-forget, runs independently)
Waits for: DownloadWorkflow (REQUIRED - must complete before marking done)
"""
from temporalio import workflow
from temporalio.common import RetryPolicy
from datetime import timedelta
from dataclasses import dataclass
from typing import List, Optional

with workflow.unsafe.imports_passed_through():
    from src.activities import twitter as twitter_activities
    from src.activities import monitor as monitor_activities
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
    Self-managing Twitter search workflow with 3 attempts.
    
    CRITICAL: This workflow WAITS for Download workflows to complete.
    We cannot fire-and-forget downloads because:
    1. Download updates MongoDB with S3 video URLs
    2. We mark _twitter_complete only after downloads finish
    3. Fixture completion checks _twitter_complete flag
    4. Fire-and-forget would cause data loss (fixture moves before downloads done)
    
    Each attempt:
    1. Builds search queries from player name + each team alias
    2. Runs searches for all aliases
    3. Deduplicates within batch AND against existing videos
    4. WAITS for DownloadWorkflow to complete
    5. Updates _twitter_count
    6. Waits 3 minutes before next attempt (except after last)
    
    After attempt 3: Marks _twitter_complete=true (all downloads done)
    
    Expected duration: ~12-15 minutes
    """
    
    @workflow.run
    async def run(self, input: TwitterWorkflowInput) -> dict:
        """
        Execute 3 Twitter search attempts with 3-minute spacing.
        
        Args:
            input: TwitterWorkflowInput with fixture_id, event_id, player_name, team_aliases
        
        Returns:
            Dict with total_videos_found, total_videos_uploaded, attempts completed
        """
        total_videos_found = 0
        total_videos_uploaded = 0
        
        # Get player search name (handles accents, hyphens, "Jr" suffixes, etc.)
        player_search = extract_player_search_name(input.player_name) if input.player_name else "Unknown"
        
        workflow.logger.info(
            f"🐦 [TWITTER] STARTED | event={input.event_id} | "
            f"player_search='{player_search}' | aliases={input.team_aliases}"
        )
        
        for attempt in range(1, 4):  # Attempts 1, 2, 3
            # =================================================================
            # Check if event still exists (graceful termination for VAR/deleted)
            # =================================================================
            workflow.logger.info(
                f"🔍 [TWITTER] Attempt {attempt}/3 | Checking event exists | event={input.event_id}"
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
            
            # Record attempt start time for "START to START" 3-minute spacing
            attempt_start = workflow.now()
            workflow.logger.info(
                f"🐦 [TWITTER] Attempt {attempt}/3 STARTING | event={input.event_id} | "
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
                    search_result = await workflow.execute_activity(
                        twitter_activities.execute_twitter_search,
                        args=[search_query, 5, list(existing_urls) + list(seen_urls_this_batch), match_date],
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
                    f"⬇️ [TWITTER] Starting DownloadWorkflow (WAITING for completion) | "
                    f"download_id={download_workflow_id} | videos={len(videos_to_download)}"
                )
                
                try:
                    # EXECUTE (wait) - not START (fire-and-forget)
                    # We MUST wait for Download to complete for data integrity
                    download_result = await workflow.execute_child_workflow(
                        DownloadWorkflow.run,
                        args=[input.fixture_id, input.event_id, input.player_name, input.team_aliases[0] if input.team_aliases else "", videos_to_download],
                        id=download_workflow_id,
                        # No execution_timeout - Download manages its own lifecycle via heartbeats
                    )
                    
                    s3_count = download_result.get("videos_uploaded", 0)
                    total_videos_uploaded += s3_count
                    workflow.logger.info(
                        f"✅ [TWITTER] Download COMPLETE | download_id={download_workflow_id} | "
                        f"uploaded={s3_count} | total_uploaded_so_far={total_videos_uploaded}"
                    )
                    
                except Exception as e:
                    workflow.logger.error(
                        f"❌ [TWITTER] DownloadWorkflow FAILED | download_id={download_workflow_id} | "
                        f"error={e} | Continuing to next attempt"
                    )
            else:
                workflow.logger.info(
                    f"📭 [TWITTER] No new videos found | event={input.event_id} | attempt={attempt}"
                )
            
            # =================================================================
            # Wait for next attempt (3 minutes from START of this attempt)
            # =================================================================
            if attempt < 3:
                elapsed = (workflow.now() - attempt_start).total_seconds()
                wait_seconds = max(180 - elapsed, 30)  # 3 min minus elapsed, min 30s
                workflow.logger.info(
                    f"⏳ [TWITTER] Attempt {attempt} took {elapsed:.0f}s | "
                    f"Waiting {wait_seconds:.0f}s before attempt {attempt + 1} | "
                    f"event={input.event_id}"
                )
                await workflow.sleep(timedelta(seconds=wait_seconds))
        
        # =================================================================
        # Mark twitter complete after ALL 3 attempts AND downloads done
        # =================================================================
        workflow.logger.info(
            f"✅ [TWITTER] All 3 attempts COMPLETE | event={input.event_id} | "
            f"Marking _twitter_complete=true"
        )
        
        try:
            await workflow.execute_activity(
                twitter_activities.mark_event_twitter_complete,
                args=[input.fixture_id, input.event_id],
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(
                    maximum_attempts=5,
                    initial_interval=timedelta(seconds=2),
                    backoff_coefficient=2.0,
                ),
            )
            workflow.logger.info(
                f"✅ [TWITTER] Marked _twitter_complete=true | event={input.event_id}"
            )
        except Exception as e:
            workflow.logger.error(
                f"❌ [TWITTER] mark_event_twitter_complete FAILED | event={input.event_id} | "
                f"error={e} | UI may show 'extracting' forever!"
            )
        
        # Notify frontend
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
            f"total_found={total_videos_found} | total_uploaded={total_videos_uploaded} | "
            f"attempts=3"
        )
        
        return {
            "fixture_id": input.fixture_id,
            "event_id": input.event_id,
            "total_videos_found": total_videos_found,
            "total_videos_uploaded": total_videos_uploaded,
            "attempts_completed": 3,
        }

