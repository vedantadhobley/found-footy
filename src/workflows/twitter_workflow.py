"""
Twitter Workflow - Scheduled Video Discovery Pipeline

Orchestrates Twitter video search with up to 10 download workflows on a ~1-minute schedule.
Uses workflow-ID-based tracking for reliable completion detection.

Design Philosophy:
- Resolves team aliases at start (cache lookup or full RAG pipeline)
- Uses WHILE loop that checks download workflow count (exits when 10 reached)
- Each iteration: search → ALWAYS start DownloadWorkflow (even with 0 videos)
- DownloadWorkflow registers itself at START, so failed starts don't count
- UploadWorkflow sets _download_complete when 10 download workflows have registered
- Max 15 attempts safety limit prevents infinite loops

Workflow-ID-Based Tracking (NEW):
- OLD: Counter incremented after each attempt (could lose counts on failures)
- NEW: Array of workflow IDs, checked at start of each iteration
  - DownloadWorkflow registers itself in _download_workflows at its START
  - TwitterWorkflow checks len(_download_workflows) >= 10 to exit
  - UploadWorkflow checks and marks _download_complete when count reaches 10
  - $addToSet ensures idempotent registration (no double-counting)

_monitor_complete:
- Set by THIS workflow at the VERY START (not by MonitorWorkflow)
- Ensures the flag is only set when Twitter ACTUALLY STARTS running
- If Twitter fails to start, _monitor_complete stays false → retry spawn

Race Condition Prevention:
- DownloadWorkflow → UploadWorkflow (signal-with-start pattern)
- UploadWorkflow serializes S3 operations per event (ID: upload-{event_id})
- Each sees fresh S3 state, eliminating race conditions

Flow:
1. Set _monitor_complete = true (we're running!)
2. Resolve team aliases (cache or RAG pipeline) - blocking, ~30-90s
3. WHILE download_count < 10 (max 15 attempts):
   a. Check download workflow count
   b. Search Twitter
   c. ALWAYS start DownloadWorkflow (registers itself, signals UploadWorkflow)
   d. Wait ~60 seconds
4. UploadWorkflow marks _download_complete when count reaches 10

Started by: MonitorWorkflow (fire-and-forget when monitor_workflows >= 3)
Starts: DownloadWorkflow (fire-and-forget) → UploadWorkflow (signal-with-start)
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
    from src.utils.event_enhancement import extract_player_search_names
    from src.utils.footy_logging import log

MODULE = "twitter_workflow"


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
    Twitter search workflow with workflow-ID-based tracking.
    
    Uses a WHILE loop that checks download workflow count (not a fixed FOR loop).
    Continues until 10 DownloadWorkflows have registered themselves.
    Max 15 attempts safety limit prevents infinite loops.
    
    Key changes from counter-based approach:
    1. Sets _monitor_complete at START (proves we're running)
    2. WHILE loop checks len(_download_workflows) each iteration
    3. ALWAYS starts DownloadWorkflow (even with 0 videos)
    4. Failed starts don't register → count stays low → we retry
    5. UploadWorkflow marks _download_complete when count reaches 10
    
    Downloads are fire-and-forget child workflows:
    1. DownloadWorkflow registers itself, downloads, validates, generates hashes
    2. DownloadWorkflow signals UploadWorkflow (signal-with-start pattern)
    3. UploadWorkflow (ID: upload-{event_id}) serializes S3 operations
    4. UploadWorkflow checks count and marks _download_complete when 10 reached
    
    Expected duration: ~10-15 minutes (depends on how quickly 10 downloads register)
    """
    
    @workflow.run
    async def run(self, input: TwitterWorkflowInput) -> dict:
        """
        Execute Twitter search attempts until 10 DownloadWorkflows have registered.
        
        First sets _monitor_complete (proves we're running), then resolves team aliases,
        then loops until download count >= 10 (max 15 attempts safety limit).
        
        Args:
            input: TwitterWorkflowInput with fixture_id, event_id, team_id, team_name, player_name
        
        Returns:
            Dict with total_videos_found, attempts completed, download count
        """
        MAX_ATTEMPTS = 15  # Safety limit - should only need 10, but handles start failures
        REQUIRED_DOWNLOADS = 10
        
        # Get player search names (handles accents, hyphens, returns multiple names for OR search)
        # e.g., "Florian Wirtz" -> ["Florian", "Wirtz"] for "(Florian OR Wirtz)" query
        player_names = extract_player_search_names(input.player_name) if input.player_name else ["Unknown"]
        player_search = player_names[0]  # Primary name for logging/IDs
        
        log.info(workflow.logger, MODULE, "started", "TwitterWorkflow started",
                 event_id=input.event_id, team_id=input.team_id,
                 team_name=input.team_name, player_names=player_names)
        
        # =========================================================================
        # Step 0: Set _monitor_complete = true (proves we're running!)
        # This is CRITICAL - it must be the FIRST thing we do.
        # If we crash after this, MonitorWorkflow won't re-spawn us (which is correct).
        # If we never run this, MonitorWorkflow will retry spawning us.
        # =========================================================================
        log.info(workflow.logger, MODULE, "set_monitor_complete_start",
                 "Setting _monitor_complete=true", event_id=input.event_id)
        try:
            await workflow.execute_activity(
                twitter_activities.set_monitor_complete,
                args=[input.fixture_id, input.event_id],
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(
                    maximum_attempts=5,  # Important - retry hard
                    initial_interval=timedelta(seconds=2),
                    backoff_coefficient=2.0,
                ),
            )
            log.info(workflow.logger, MODULE, "set_monitor_complete_success",
                     "_monitor_complete=true SET", event_id=input.event_id)
        except Exception as e:
            log.error(workflow.logger, MODULE, "set_monitor_complete_failed",
                      "FAILED to set _monitor_complete - Continuing anyway",
                      event_id=input.event_id, error=str(e))
        
        # =========================================================================
        # Step 1: Resolve team aliases (cache lookup or full RAG pipeline)
        # This is a blocking call - we need aliases before searching
        # =========================================================================
        log.info(workflow.logger, MODULE, "resolving_aliases",
                 "Resolving team aliases", team_id=input.team_id)
        
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
                log.info(workflow.logger, MODULE, "alias_cache_hit",
                         "Alias cache HIT", aliases=team_aliases)
        except Exception as e:
            log.warning(workflow.logger, MODULE, "alias_cache_lookup_failed",
                        "Cache lookup failed", error=str(e))
        
        # Cache miss - do full RAG lookup
        if not team_aliases:
            log.info(workflow.logger, MODULE, "alias_cache_miss",
                     "Cache MISS - Running full RAG pipeline")
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
                log.info(workflow.logger, MODULE, "rag_success",
                         "RAG SUCCESS", aliases=team_aliases)
            except Exception as e:
                log.error(workflow.logger, MODULE, "rag_failed",
                          "RAG FAILED", error=str(e))
                # Fallback to just team name
                team_aliases = [input.team_name]
                log.warning(workflow.logger, MODULE, "alias_fallback",
                            "Using FALLBACK", aliases=team_aliases)
        
        # Save aliases to event for debugging
        try:
            await workflow.execute_activity(
                rag_activities.save_team_aliases,
                args=[input.fixture_id, input.event_id, team_aliases],
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(maximum_attempts=2),
            )
            log.info(workflow.logger, MODULE, "aliases_saved",
                     "Aliases saved", aliases=team_aliases)
        except Exception as e:
            log.warning(workflow.logger, MODULE, "aliases_save_failed",
                        "Failed to save aliases", error=str(e))
        
        log.info(workflow.logger, MODULE, "search_loop_start",
                 "Starting search loop", event_id=input.event_id,
                 player_names=player_names, aliases=team_aliases,
                 cache_hit=cache_hit, max_attempts=MAX_ATTEMPTS,
                 required_downloads=REQUIRED_DOWNLOADS)
        
        # Track cumulative stats across all attempts
        total_videos_found = 0
        attempt = 0
        
        # =========================================================================
        # MAIN LOOP: Continue until 10 DownloadWorkflows have registered
        # =========================================================================
        while attempt < MAX_ATTEMPTS:
            attempt += 1
            
            # =================================================================
            # Check download workflow count - exit if we have 10
            # =================================================================
            log.info(workflow.logger, MODULE, "checking_download_count",
                     "Checking download count", attempt=attempt,
                     max_attempts=MAX_ATTEMPTS, event_id=input.event_id)
            
            try:
                count_result = await workflow.execute_activity(
                    twitter_activities.get_download_workflow_count,
                    args=[input.fixture_id, input.event_id],
                    start_to_close_timeout=timedelta(seconds=30),
                    retry_policy=RetryPolicy(
                        maximum_attempts=3,
                        initial_interval=timedelta(seconds=2),
                        backoff_coefficient=2.0,
                    ),
                )
                download_count = count_result.get("count", 0)
                log.info(workflow.logger, MODULE, "download_count",
                         "Got download count", count=download_count,
                         required=REQUIRED_DOWNLOADS, event_id=input.event_id)
                
                if download_count >= REQUIRED_DOWNLOADS:
                    log.info(workflow.logger, MODULE, "download_count_reached",
                             "Download count reached - Exiting loop",
                             count=download_count, event_id=input.event_id)
                    break
            except Exception as e:
                log.warning(workflow.logger, MODULE, "get_download_count_failed",
                            "get_download_workflow_count FAILED - Continuing",
                            event_id=input.event_id, error=str(e))
                download_count = 0
            
            # =================================================================
            # Check if event still exists (graceful termination for VAR/deleted)
            # =================================================================
            log.info(workflow.logger, MODULE, "checking_event_exists",
                     "Checking if event exists", attempt=attempt,
                     max_attempts=MAX_ATTEMPTS, event_id=input.event_id)
            
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
                log.info(workflow.logger, MODULE, "event_check_complete",
                         "Event check complete", event_id=input.event_id,
                         exists=event_check.get('exists'))
            except Exception as e:
                log.warning(workflow.logger, MODULE, "check_event_exists_failed",
                            "check_event_exists FAILED - Assuming event exists",
                            event_id=input.event_id, error=str(e))
                event_check = {"exists": True}
            
            if not event_check.get("exists", False):
                log.warning(workflow.logger, MODULE, "event_deleted",
                            "Event NO LONGER EXISTS - Terminating early (VAR reversal or deletion)",
                            event_id=input.event_id)
                return {
                    "fixture_id": input.fixture_id,
                    "event_id": input.event_id,
                    "total_videos_found": total_videos_found,
                    "download_count": download_count,
                    "attempts_completed": attempt - 1,
                    "terminated_early": True,
                    "reason": "event_deleted",
                }
            
            # Record attempt start time for "START to START" 1-minute spacing
            attempt_start = workflow.now()
            log.info(workflow.logger, MODULE, "attempt_start",
                     "Attempt STARTING", attempt=attempt, max_attempts=MAX_ATTEMPTS,
                     event_id=input.event_id, player_names=player_names,
                     aliases=team_aliases, download_count=download_count)
            
            # =================================================================
            # Get existing video URLs (for deduplication)
            # =================================================================
            log.info(workflow.logger, MODULE, "fetching_existing_urls",
                     "Fetching existing video URLs", event_id=input.event_id)
            
            try:
                search_data = await workflow.execute_activity(
                    twitter_activities.get_twitter_search_data,
                    args=[input.fixture_id, input.event_id],
                    start_to_close_timeout=timedelta(seconds=30),
                    retry_policy=RetryPolicy(maximum_attempts=3),
                )
                existing_urls = search_data.get("existing_video_urls", [])
                match_date = search_data.get("match_date", "")
                log.info(workflow.logger, MODULE, "got_search_data",
                         "Got search data", event_id=input.event_id,
                         existing_urls_count=len(existing_urls),
                         match_date=match_date[:10] if match_date else "N/A")
            except Exception as e:
                log.error(workflow.logger, MODULE, "get_search_data_failed",
                          "get_twitter_search_data FAILED",
                          event_id=input.event_id, error=str(e))
                existing_urls = []
                match_date = ""
            
            # =================================================================
            # Build search query with OR operators for player names AND team aliases
            # Twitter supports nested OR: "(Florian OR Wirtz) (LFC OR Liverpool)"
            # This dramatically improves search coverage for well-known players
            # =================================================================
            
            # Build player part: "(Florian OR Wirtz)" or just "Salah"
            if len(player_names) > 1:
                player_or = " OR ".join(player_names)
                player_part = f"({player_or})"
            else:
                player_part = player_names[0]
            
            # Build team part: "(LFC OR Liverpool)" or just "Liverpool"
            if len(team_aliases) > 1:
                aliases_or = " OR ".join(team_aliases)
                team_part = f"({aliases_or})"
            else:
                team_part = team_aliases[0]
            
            search_query = f"{player_part} {team_part}"
            
            log.info(workflow.logger, MODULE, "search_query",
                     "Search query built", query=search_query,
                     excluding_count=len(existing_urls))
            
            # Execute single search with combined query
            # Returns ALL videos found (limited to 5 longest for download later)
            all_videos = []
            try:
                search_result = await workflow.execute_activity(
                    twitter_activities.execute_twitter_search,
                    args=[search_query, list(existing_urls), 3],  # max_age_minutes=3
                    start_to_close_timeout=timedelta(seconds=60),
                    retry_policy=RetryPolicy(
                        maximum_attempts=3,
                        initial_interval=timedelta(seconds=10),
                        backoff_coefficient=1.5,
                    ),
                )
                all_videos = search_result.get("videos", [])
                log.info(workflow.logger, MODULE, "search_complete",
                         "Search complete", query=search_query,
                         found=len(all_videos))
            except Exception as e:
                log.warning(workflow.logger, MODULE, "search_failed",
                            "Search FAILED", query=search_query, error=str(e))
            
            video_count = len(all_videos)
            total_videos_found += video_count
            log.info(workflow.logger, MODULE, "attempt_search_complete",
                     "Attempt search complete", attempt=attempt,
                     event_id=input.event_id, unique_videos=video_count,
                     total_found_so_far=total_videos_found)
            
            # =================================================================
            # ALWAYS Execute DownloadWorkflow - even with 0 videos
            # DownloadWorkflow registers itself at START, signals UploadWorkflow
            # This ensures we always increment the download count
            # =================================================================
            
            # Sort by duration (longest first) and take top 5 to reduce processing time
            # Videos without duration go to the end (treated as 0)
            MAX_VIDEOS_TO_DOWNLOAD = 5
            if all_videos:
                sorted_videos = sorted(
                    all_videos, 
                    key=lambda v: v.get("duration_seconds") or 0, 
                    reverse=True
                )
                videos_to_download = sorted_videos[:MAX_VIDEOS_TO_DOWNLOAD]
                
                # Log what we're selecting
                if len(all_videos) > MAX_VIDEOS_TO_DOWNLOAD:
                    log.info(workflow.logger, MODULE, "selecting_top_videos",
                             "Selecting top longest videos",
                             selected=MAX_VIDEOS_TO_DOWNLOAD, total=len(all_videos),
                             durations=[v.get('duration_seconds', 0) for v in videos_to_download])
            else:
                videos_to_download = []  # Empty list - DownloadWorkflow will still register itself
                log.info(workflow.logger, MODULE, "no_videos_found",
                         "No videos found - Starting DownloadWorkflow anyway for tracking",
                         event_id=input.event_id)
            
            # =================================================================
            # Save ONLY the videos we're actually downloading to _discovered_videos
            # This ensures we don't permanently skip videos we never tried
            # =================================================================
            if videos_to_download:
                log.info(workflow.logger, MODULE, "saving_discovered_videos",
                         "Saving URLs to _discovered_videos",
                         count=len(videos_to_download), event_id=input.event_id)
                try:
                    await workflow.execute_activity(
                        twitter_activities.save_discovered_videos,
                        args=[input.fixture_id, input.event_id, videos_to_download],
                        start_to_close_timeout=timedelta(seconds=30),
                        retry_policy=RetryPolicy(
                            maximum_attempts=3,
                            initial_interval=timedelta(seconds=2),
                            backoff_coefficient=2.0,
                        ),
                    )
                    log.info(workflow.logger, MODULE, "urls_saved",
                             "Saved URLs", event_id=input.event_id,
                             count=len(videos_to_download))
                except Exception as e:
                    log.error(workflow.logger, MODULE, "save_discovered_videos_failed",
                              "save_discovered_videos FAILED",
                              event_id=input.event_id, error=str(e))
            
            team_clean = team_aliases[0].replace(" ", "_").replace(".", "_").replace("-", "_") if team_aliases else "Unknown"
            # Don't include video count in workflow ID - it causes nondeterminism when code changes
            download_workflow_id = f"download{attempt}-{team_clean}-{player_search}-{input.event_id}"
            
            log.info(workflow.logger, MODULE, "starting_download_workflow",
                     "Starting DownloadWorkflow (FIRE-AND-FORGET)",
                     download_id=download_workflow_id, videos=len(videos_to_download))
            
            try:
                # START (fire-and-forget) - not EXECUTE (wait)
                # DownloadWorkflow registers itself at START, then signals UploadWorkflow
                from temporalio.workflow import ParentClosePolicy
                await workflow.start_child_workflow(
                    DownloadWorkflow.run,
                    args=[input.fixture_id, input.event_id, input.player_name, team_aliases[0] if team_aliases else "", videos_to_download],
                    id=download_workflow_id,
                    parent_close_policy=ParentClosePolicy.ABANDON,  # Continue even if parent closes
                    # Increase task timeout from 10s to 60s - large histories need more
                    # time to replay, otherwise we get "Task not found" errors
                    task_timeout=timedelta(seconds=60),
                    task_queue="found-footy",  # Explicit queue - don't inherit from parent
                )
                
                log.info(workflow.logger, MODULE, "download_started",
                         "Download STARTED (fire-and-forget)",
                         download_id=download_workflow_id)
                
            except Exception as e:
                # Download failed to START - it didn't register, so count stays low
                # The while loop will naturally retry on the next iteration
                log.error(workflow.logger, MODULE, "download_start_failed",
                          "Failed to START DownloadWorkflow - Will retry on next iteration",
                          download_id=download_workflow_id, error=str(e))
            
            # =================================================================
            # Wait for next attempt (1 minute from START of this attempt)
            # =================================================================
            elapsed = (workflow.now() - attempt_start).total_seconds()
            wait_seconds = max(60 - elapsed, 10)  # 1 min minus elapsed, min 10s
            log.info(workflow.logger, MODULE, "waiting_for_next_attempt",
                     "Waiting before next iteration",
                     attempt=attempt, elapsed_s=int(elapsed),
                     wait_s=int(wait_seconds), event_id=input.event_id)
            await workflow.sleep(timedelta(seconds=wait_seconds))
        
        # =================================================================
        # TwitterWorkflow complete - either we reached 10 downloads or hit max attempts
        # _download_complete is set by UploadWorkflow when it sees 10 downloads
        # =================================================================
        final_download_count = download_count if 'download_count' in dir() else 0
        exit_reason = "download_count_reached" if final_download_count >= REQUIRED_DOWNLOADS else "max_attempts_reached"
        
        log.info(workflow.logger, MODULE, "loop_complete",
                 "Search loop COMPLETE", event_id=input.event_id,
                 reason=exit_reason, download_count=final_download_count,
                 attempts=attempt)
        
        # NOTE: Temp directory cleanup happens at FIXTURE level when fixture moves to completed
        # This avoids race conditions with uploads that may still be processing
        
        # Notify frontend (best-effort, non-critical)
        try:
            await workflow.execute_activity(
                monitor_activities.notify_frontend_refresh,
                start_to_close_timeout=timedelta(seconds=15),
                retry_policy=RetryPolicy(maximum_attempts=2),
            )
            log.info(workflow.logger, MODULE, "frontend_notified",
                     "Frontend notified", event_id=input.event_id)
        except Exception as e:
            log.warning(workflow.logger, MODULE, "frontend_notify_failed",
                        "Frontend notification FAILED",
                        event_id=input.event_id, error=str(e))
        
        log.info(workflow.logger, MODULE, "workflow_complete",
                 "TwitterWorkflow COMPLETE", event_id=input.event_id,
                 total_found=total_videos_found, download_count=final_download_count,
                 attempts=attempt, exit_reason=exit_reason)
        
        return {
            "fixture_id": input.fixture_id,
            "event_id": input.event_id,
            "total_videos_found": total_videos_found,
            "download_count": final_download_count,
            "attempts_completed": attempt,
            "exit_reason": exit_reason,
        }
