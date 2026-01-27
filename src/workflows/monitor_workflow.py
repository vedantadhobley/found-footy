"""
Monitor Workflow - Every 30 Seconds

Tracks active fixtures, fetches fresh data, and processes events inline.
No EventWorkflow needed with player_id in event_id!

ORCHESTRATION MODEL:
- Monitor is the single orchestrator for event monitoring and debouncing
- Monitor triggers TwitterWorkflow ONCE when _monitor_complete=true
- TwitterWorkflow resolves aliases (cache or RAG) then runs 10 search attempts
- Twitter workflow sets: _twitter_complete (when all 10 attempts done)
- Fixture completes when ALL events have _monitor_complete=true AND _twitter_complete=true

FIXTURE LIFECYCLE:
- Staging fixtures: Polled for updates (times, status, metadata)
  - When status changes NS/TBD → anything else: Queued for activation
- Active fixtures: Full event monitoring, debouncing, Twitter workflows
  - When status is completed and all events complete: Moved to completed
  - When fixture completes: Temp directories are cleaned up
- End of cycle: Complete ready fixtures, then activate queued fixtures
"""
from temporalio import workflow
from temporalio.common import RetryPolicy
from temporalio.workflow import ParentClosePolicy
from datetime import timedelta
from typing import List
import asyncio

with workflow.unsafe.imports_passed_through():
    from src.activities import monitor as monitor_activities
    from src.activities import upload as upload_activities
    from src.workflows.twitter_workflow import TwitterWorkflow, TwitterWorkflowInput
    from src.utils.fixture_status import get_completed_statuses


@workflow.defn
class MonitorWorkflow:
    """Monitor active fixtures and trigger event workflows for fixtures with changes"""
    
    @workflow.run
    async def run(self) -> dict:
        """
        Workflow:
        1. Fetch and process staging fixtures (update data, detect status changes)
        2. Batch fetch all active fixtures from API
        3. For each active fixture:
           - Filter to trackable events (Goals only)
           - Generate event IDs: {fixture}_{team}_{player}_{type}_{sequence}
           - Store in fixtures_live
           - Process events (pure set comparison)
           - Trigger TwitterWorkflow for stable events
           - Trigger retry TwitterWorkflow for events needing more videos
        4. Complete finished fixtures (active → completed)
        5. Activate queued fixtures (staging → active)
        6. Notify frontend
        
        VAR handling: Events removed from API are DELETED from MongoDB + S3.
        This frees the sequence ID slot so if the same player scores again,
        the new goal gets the same sequence number without collision.
        """
        cycle_start = workflow.now()
        
        workflow.logger.info("👁️ Starting monitor cycle")
        
        # Get completed statuses for checking if fixtures are finished
        completed_statuses = get_completed_statuses()
        
        # =================================================================
        # STAGING: Fetch and process staging fixtures (updates only, no activation yet)
        # =================================================================
        t0 = workflow.now()
        staging_fixtures = await workflow.execute_activity(
            monitor_activities.fetch_staging_fixtures,
            start_to_close_timeout=timedelta(seconds=15),  # API has 10s timeout, allow buffer
            retry_policy=RetryPolicy(maximum_attempts=2),
        )
        t1 = workflow.now()
        staging_ms = (t1 - t0).total_seconds() * 1000
        
        staging_result = {"updated_count": 0, "fixtures_to_activate": []}
        if staging_fixtures:
            staging_result = await workflow.execute_activity(
                monitor_activities.process_staging_fixtures,
                staging_fixtures,
                start_to_close_timeout=timedelta(seconds=10),  # Pure MongoDB ops
                retry_policy=RetryPolicy(maximum_attempts=2),
            )
        t2 = workflow.now()
        process_staging_ms = (t2 - t1).total_seconds() * 1000
        
        fixtures_to_activate = staging_result.get("fixtures_to_activate", [])
        
        # =================================================================
        # ACTIVE: Fetch and process active fixtures (event monitoring)
        # =================================================================
        
        # Fetch all active fixtures from API
        fixtures = await workflow.execute_activity(
            monitor_activities.fetch_active_fixtures,
            start_to_close_timeout=timedelta(seconds=15),  # API has 10s timeout, allow buffer
            retry_policy=RetryPolicy(maximum_attempts=2),
        )
        t3 = workflow.now()
        fetch_active_ms = (t3 - t2).total_seconds() * 1000
        
        workflow.logger.info(
            f"⏱️ [MONITOR] Timing | fetch_staging={staging_ms:.0f}ms | "
            f"process_staging={process_staging_ms:.0f}ms | fetch_active={fetch_active_ms:.0f}ms"
        )
        
        # =========================================================================
        # Process fixtures IN PARALLEL - each fixture is independent
        # This significantly speeds up processing when multiple matches are active
        # =========================================================================
        twitter_workflows_started = []
        completed_count = 0
        needs_frontend_refresh = False
        
        async def process_fixture(fixture_data: dict):
            """Process a single fixture - store, process events, trigger Twitter workflows"""
            nonlocal needs_frontend_refresh
            
            fixture_id = fixture_data.get("fixture", {}).get("id")
            status = fixture_data.get("fixture", {}).get("status", {}).get("short")
            fixture_finished = status in completed_statuses
            
            local_twitter_workflows = []
            was_completed = False
            
            # Store in live
            await workflow.execute_activity(
                monitor_activities.store_and_compare,
                args=[fixture_id, fixture_data],
                start_to_close_timeout=timedelta(seconds=10),
                retry_policy=RetryPolicy(
                    maximum_attempts=3,
                    initial_interval=timedelta(seconds=1),
                    backoff_coefficient=2.0,
                ),
            )
            
            # Process events inline (no EventWorkflow needed!)
            # Pass workflow_id for workflow-ID-based tracking
            monitor_workflow_id = workflow.info().workflow_id
            result = await workflow.execute_activity(
                monitor_activities.process_fixture_events,
                args=[fixture_id, monitor_workflow_id],
                start_to_close_timeout=timedelta(seconds=60),
                retry_policy=RetryPolicy(maximum_attempts=3),
            )
            
            # Flag for frontend refresh if new events triggered
            if result.get("twitter_triggered"):
                needs_frontend_refresh = True
            
            # Trigger TwitterWorkflow for each newly stable event (ONCE per event)
            # TwitterWorkflow resolves aliases, then handles all 10 search attempts internally
            for event_info in result.get("twitter_triggered", []):
                event_id = event_info["event_id"]
                player_name = event_info["player_name"]
                team_id = event_info["team_id"]
                team_name = event_info["team_name"]
                minute = event_info["minute"]
                extra = event_info.get("extra")
                first_seen = event_info.get("first_seen")
                
                # Build human-readable workflow ID
                player_last = player_name.split()[-1] if player_name else "Unknown"
                team_clean = team_name.replace(" ", "_").replace(".", "_")
                minute_str = f"{minute}+{extra}min" if extra else f"{minute}min"
                twitter_workflow_id = f"twitter-{team_clean}-{player_last}-{minute_str}-{event_id}"
                
                # Check if Twitter workflow is already running or completed
                # This prevents unnecessary restart attempts and log spam
                workflow_status = await workflow.execute_activity(
                    monitor_activities.check_twitter_workflow_running,
                    twitter_workflow_id,
                    start_to_close_timeout=timedelta(seconds=10),
                    retry_policy=RetryPolicy(maximum_attempts=2),
                )
                
                if workflow_status.get("running"):
                    workflow.logger.info(
                        f"⏭️ [MONITOR] Twitter workflow already running | id={twitter_workflow_id}"
                    )
                    continue
                
                if workflow_status.get("status") == "COMPLETED":
                    workflow.logger.info(
                        f"✅ [MONITOR] Twitter workflow already completed | id={twitter_workflow_id}"
                    )
                    continue
                
                local_twitter_workflows.append(twitter_workflow_id)
                
                # Start TwitterWorkflow (fire-and-forget)
                # TwitterWorkflow resolves aliases, then runs 10 search attempts
                await workflow.start_child_workflow(
                    TwitterWorkflow.run,
                    TwitterWorkflowInput(
                        fixture_id=fixture_id,
                        event_id=event_id,
                        team_id=team_id,
                        team_name=team_name,
                        player_name=player_name,
                    ),
                    id=twitter_workflow_id,
                    # IMPORTANT: Explicitly set task_queue to prevent inheritance issues
                    # during workflow replay that could route to wrong/non-existent queues
                    task_queue="found-footy",
                    # No execution_timeout - Twitter manages its own lifecycle
                    # Twitter runs ~10-12 minutes (alias lookup + 10 search attempts)
                    parent_close_policy=ParentClosePolicy.ABANDON,
                    # Increase task timeout from 10s to 60s - large histories (300+ events)
                    # need more time to replay, otherwise we get "Task not found" errors
                    task_timeout=timedelta(seconds=60),
                )
                
                workflow.logger.info(f"🐦 Started TwitterWorkflow: {twitter_workflow_id}")
                
                # NOTE: _monitor_complete is now set by TwitterWorkflow at its START
                # This ensures the flag is only set when Twitter actually starts running,
                # not just when MonitorWorkflow attempts to spawn it.
                # If Twitter fails to start, _monitor_complete stays false, and the next
                # monitor will see (count >= 3 AND complete = false) → retry spawn.
            
            # Check if fixture is finished and should be completed
            if fixture_finished:
                was_completed = await workflow.execute_activity(
                    monitor_activities.complete_fixture_if_ready,
                    fixture_id,
                    start_to_close_timeout=timedelta(seconds=10),
                    retry_policy=RetryPolicy(
                        maximum_attempts=3,
                        initial_interval=timedelta(seconds=1),
                        backoff_coefficient=2.0,
                    ),
                )
            
            return {
                "twitter_workflows": local_twitter_workflows,
                "completed": 1 if was_completed else 0,
                "completed_fixture_id": fixture_id if was_completed else None,
            }
        
        # Execute all fixture processing in parallel
        if fixtures:
            fixture_tasks = [process_fixture(f) for f in fixtures]
            fixture_results = await asyncio.gather(*fixture_tasks)
            
            # Aggregate results
            completed_fixture_ids = []
            for result in fixture_results:
                twitter_workflows_started.extend(result["twitter_workflows"])
                completed_count += result["completed"]
                if result.get("completed_fixture_id"):
                    completed_fixture_ids.append(result["completed_fixture_id"])
            
            # Cleanup temp directories for completed fixtures
            for fixture_id in completed_fixture_ids:
                try:
                    await workflow.execute_activity(
                        upload_activities.cleanup_fixture_temp_dirs,
                        fixture_id,
                        start_to_close_timeout=timedelta(seconds=60),
                        retry_policy=RetryPolicy(maximum_attempts=2),
                    )
                    workflow.logger.info(f"🧹 Cleaned up temp dirs for completed fixture {fixture_id}")
                except Exception as e:
                    workflow.logger.warning(
                        f"⚠️ Failed to cleanup temp dirs for fixture {fixture_id} | error={e}"
                    )
            
            # Single frontend notification after all processing
            if needs_frontend_refresh:
                await workflow.execute_activity(
                    monitor_activities.notify_frontend_refresh,
                    start_to_close_timeout=timedelta(seconds=5),
                    retry_policy=RetryPolicy(maximum_attempts=1),
                )
        
        # =================================================================
        # END OF CYCLE: Activate queued fixtures (staging → active)
        # =================================================================
        activated_count = 0
        if fixtures_to_activate:
            activation_result = await workflow.execute_activity(
                monitor_activities.activate_pending_fixtures,
                fixtures_to_activate,
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(maximum_attempts=2),
            )
            activated_count = activation_result.get("activated_count", 0)
        
        # Notify frontend to refresh (SSE broadcast to connected clients)
        await workflow.execute_activity(
            monitor_activities.notify_frontend_refresh,
            start_to_close_timeout=timedelta(seconds=10),
            retry_policy=RetryPolicy(maximum_attempts=1),  # Don't retry - frontend may not be running
        )
        
        total_time = (workflow.now() - cycle_start).total_seconds()
        workflow.logger.info(
            f"✅ Monitor complete ({total_time:.1f}s): "
            f"staging={staging_result.get('updated_count', 0)} updated/{activated_count} activated, "
            f"active={len(fixtures)} processed/{completed_count} completed, "
            f"{len(twitter_workflows_started)} Twitter workflows started"
        )
        
        return {
            "staging_updated": staging_result.get("updated_count", 0),
            "staging_activated": activated_count,
            "active_fixtures_processed": len(fixtures),
            "active_fixtures_completed": completed_count,
            "twitter_workflows_started": len(twitter_workflows_started),
        }
