"""
Twitter Workflow - Self-Managing Video Discovery Pipeline

Orchestrates Twitter video search with 3 attempts, 3-minute spacing.

KEY CHANGE FROM PREVIOUS VERSION:
- Previously: Monitor triggered this workflow 3 times (once per minute poll)
- Now: This workflow manages all 3 attempts internally with durable timers

Input:
- team_aliases: List of team name variations (e.g., ["Liverpool", "LFC", "Reds"])
- For each attempt, runs a search for EACH alias
- Deduplicates videos across aliases and previous attempts

Attempt Flow:
1. ATTEMPT 1: Search "Salah Liverpool", "Salah LFC", "Salah Reds" → dedupe → download
2. WAIT 3 minutes (durable timer aligned to 3-min boundary)
3. ATTEMPT 2: Same queries (new videos may have appeared) → dedupe → download
4. WAIT 3 minutes
5. ATTEMPT 3: Same queries → dedupe → download → mark _twitter_complete=true
"""
from temporalio import workflow
from temporalio.common import RetryPolicy
from datetime import timedelta
from dataclasses import dataclass
from typing import List, Optional

with workflow.unsafe.imports_passed_through():
    from src.activities import twitter as twitter_activities
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
    
    Each attempt:
    1. Builds search queries from player name + each team alias
    2. Runs searches for all aliases
    3. Deduplicates within batch AND against existing videos
    4. Triggers DownloadWorkflow
    5. Updates _twitter_count
    6. Waits 3 minutes before next attempt (except after last)
    
    After attempt 3: Marks _twitter_complete=true
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
        # "Vinícius Júnior" → "Vinicius", "T. Alexander-Arnold" → "Alexander"
        player_search = extract_player_search_name(input.player_name) if input.player_name else "Unknown"
        
        workflow.logger.info(
            f"🐦 TwitterWorkflow starting for {input.event_id} "
            f"(player: {player_search}, aliases: {input.team_aliases})"
        )
        
        for attempt in range(1, 4):  # Attempts 1, 2, 3
            # Record attempt start time for "START to START" 3-minute spacing
            attempt_start = workflow.now()
            workflow.logger.info(f"🐦 Twitter attempt {attempt}/3 for {input.event_id}")
            
            # =================================================================
            # Update attempt counter in MongoDB
            # =================================================================
            await workflow.execute_activity(
                twitter_activities.update_twitter_attempt,
                args=[input.fixture_id, input.event_id, attempt],
                start_to_close_timeout=timedelta(seconds=10),
                retry_policy=RetryPolicy(maximum_attempts=2),
            )
            
            # =================================================================
            # Get existing video URLs (for deduplication)
            # =================================================================
            search_data = await workflow.execute_activity(
                twitter_activities.get_twitter_search_data,
                args=[input.fixture_id, input.event_id],
                start_to_close_timeout=timedelta(seconds=10),
                retry_policy=RetryPolicy(maximum_attempts=2),
            )
            existing_urls = search_data.get("existing_video_urls", [])
            match_date = search_data.get("match_date", "")  # For date filtering
            
            # =================================================================
            # Run searches for each alias, collect all videos
            # =================================================================
            all_videos = []
            seen_urls_this_batch = set()
            
            for alias in input.team_aliases:
                search_query = f"{player_search} {alias}"
                workflow.logger.info(f"🔍 Searching: '{search_query}'")
                
                try:
                    search_result = await workflow.execute_activity(
                        twitter_activities.execute_twitter_search,
                        args=[search_query, 5, list(existing_urls) + list(seen_urls_this_batch), match_date],
                        start_to_close_timeout=timedelta(seconds=150),
                        retry_policy=RetryPolicy(
                            maximum_attempts=3,
                            initial_interval=timedelta(seconds=10),
                            backoff_coefficient=1.5,
                        ),
                    )
                    
                    videos = search_result.get("videos", [])
                    workflow.logger.info(f"📹 Found {len(videos)} videos for '{search_query}'")
                    
                    # Dedupe within this batch (by URL)
                    for video in videos:
                        url = video.get("video_page_url") or video.get("url", "")
                        if url and url not in seen_urls_this_batch:
                            seen_urls_this_batch.add(url)
                            all_videos.append(video)
                            
                except Exception as e:
                    workflow.logger.warning(f"⚠️ Search failed for '{search_query}': {e}")
                    continue
            
            video_count = len(all_videos)
            total_videos_found += video_count
            workflow.logger.info(f"📹 Attempt {attempt}: {video_count} unique videos found")
            
            # =================================================================
            # Save discovered videos to MongoDB
            # =================================================================
            if all_videos:
                await workflow.execute_activity(
                    twitter_activities.save_discovered_videos,
                    args=[input.fixture_id, input.event_id, all_videos],
                    start_to_close_timeout=timedelta(seconds=10),
                    retry_policy=RetryPolicy(
                        maximum_attempts=3,
                        initial_interval=timedelta(seconds=1),
                        backoff_coefficient=2.0,
                    ),
                )
                workflow.logger.info(f"💾 Saved {video_count} URLs to _discovered_videos")
            
            # =================================================================
            # Trigger DownloadWorkflow
            # =================================================================
            if all_videos:
                team_clean = input.team_aliases[0].replace(" ", "_").replace(".", "_").replace("-", "_") if input.team_aliases else "Unknown"
                download_workflow_id = f"download{attempt}-{team_clean}-{player_search}-{video_count}vids-{input.event_id}"
                
                workflow.logger.info(f"⬇️ Starting download: {download_workflow_id}")
                
                try:
                    download_result = await workflow.execute_child_workflow(
                        DownloadWorkflow.run,
                        args=[input.fixture_id, input.event_id, input.player_name, input.team_aliases[0] if input.team_aliases else "", all_videos],
                        id=download_workflow_id,
                        execution_timeout=timedelta(minutes=15),
                    )
                    
                    s3_count = download_result.get("videos_uploaded", 0)
                    total_videos_uploaded += s3_count
                    workflow.logger.info(f"✅ Download complete: {s3_count} videos in S3")
                    
                except Exception as e:
                    workflow.logger.error(f"❌ Download workflow failed: {e}")
                    workflow.logger.info(f"⚠️ Continuing despite download failure")
            else:
                workflow.logger.info(f"📭 No new videos to download for attempt {attempt}")
            
            # =================================================================
            # Wait for next attempt (3 minutes from START of this attempt)
            # =================================================================
            if attempt < 3:
                elapsed = (workflow.now() - attempt_start).total_seconds()
                wait_seconds = max(180 - elapsed, 30)  # 3 min minus elapsed, min 30s
                workflow.logger.info(
                    f"⏳ Attempt {attempt} took {elapsed:.0f}s, waiting {wait_seconds:.0f}s "
                    f"(3 min START-to-START spacing)"
                )
                await workflow.sleep(timedelta(seconds=wait_seconds))
        
        # =================================================================
        # Mark twitter complete after all 3 attempts
        # =================================================================
        workflow.logger.info(f"✅ All 3 attempts complete, marking twitter_complete for {input.event_id}")
        
        await workflow.execute_activity(
            twitter_activities.mark_event_twitter_complete,
            args=[input.fixture_id, input.event_id],
            start_to_close_timeout=timedelta(seconds=10),
            retry_policy=RetryPolicy(maximum_attempts=3),
        )
        
        workflow.logger.info(
            f"✅ TwitterWorkflow complete: {total_videos_found} found, "
            f"{total_videos_uploaded} uploaded for {input.event_id}"
        )
        
        return {
            "fixture_id": input.fixture_id,
            "event_id": input.event_id,
            "total_videos_found": total_videos_found,
            "total_videos_uploaded": total_videos_uploaded,
            "attempts_completed": 3,
        }

