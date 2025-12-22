"""
Monitor Workflow - Every Minute

Tracks active fixtures, fetches fresh data, and processes events inline.
No EventWorkflow needed with player_id in event_id!

ORCHESTRATION MODEL:
- Monitor is the single orchestrator for event monitoring and debouncing
- Monitor triggers RAGWorkflow ONCE when _monitor_complete=true
- RAGWorkflow → TwitterWorkflow (manages its own 3 attempts with 3-min timers)
- Twitter workflow sets: _twitter_complete (when all 3 attempts done)
- Fixture completes when ALL events have _monitor_complete=true AND _twitter_complete=true

FIXTURE LIFECYCLE:
- Staging fixtures: Polled for updates (times, status, metadata)
  - When status changes NS/TBD → anything else: Queued for activation
- Active fixtures: Full event monitoring, debouncing, RAG→Twitter workflows
  - When status is completed and all events complete: Moved to completed
- End of cycle: Complete ready fixtures, then activate queued fixtures
"""
from temporalio import workflow
from temporalio.common import RetryPolicy
from temporalio.workflow import ParentClosePolicy
from datetime import timedelta
from typing import List

with workflow.unsafe.imports_passed_through():
    from src.activities import monitor as monitor_activities
    from src.workflows.rag_workflow import RAGWorkflow, RAGWorkflowInput
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
        
        workflow.logger.info("👁️ Starting monitor cycle")
        
        # Get completed statuses for checking if fixtures are finished
        completed_statuses = get_completed_statuses()
        
        # =================================================================
        # STAGING: Fetch and process staging fixtures (updates only, no activation yet)
        # =================================================================
        staging_fixtures = await workflow.execute_activity(
            monitor_activities.fetch_staging_fixtures,
            start_to_close_timeout=timedelta(seconds=60),
            retry_policy=RetryPolicy(maximum_attempts=3),
        )
        
        staging_result = {"updated_count": 0, "fixtures_to_activate": []}
        if staging_fixtures:
            staging_result = await workflow.execute_activity(
                monitor_activities.process_staging_fixtures,
                staging_fixtures,
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(maximum_attempts=2),
            )
        
        fixtures_to_activate = staging_result.get("fixtures_to_activate", [])
        
        # =================================================================
        # ACTIVE: Fetch and process active fixtures (event monitoring)
        # =================================================================
        
        # Fetch all active fixtures from API
        fixtures = await workflow.execute_activity(
            monitor_activities.fetch_active_fixtures,
            start_to_close_timeout=timedelta(seconds=60),
            retry_policy=RetryPolicy(maximum_attempts=3),
        )
        
        # Process each fixture
        rag_workflows_started = []
        completed_count = 0
        
        for fixture_data in fixtures:
            fixture_id = fixture_data.get("fixture", {}).get("id")
            status = fixture_data.get("fixture", {}).get("status", {}).get("short")
            fixture_finished = status in completed_statuses
            
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
            result = await workflow.execute_activity(
                monitor_activities.process_fixture_events,
                fixture_id,
                start_to_close_timeout=timedelta(seconds=60),
                retry_policy=RetryPolicy(maximum_attempts=3),
            )
            
            # Notify frontend immediately when events transition to _monitor_complete=true
            # This lets frontend show "extracting" state BEFORE Twitter/Download starts
            if result.get("twitter_triggered"):
                await workflow.execute_activity(
                    monitor_activities.notify_frontend_refresh,
                    start_to_close_timeout=timedelta(seconds=5),
                    retry_policy=RetryPolicy(maximum_attempts=1),
                )
            
            # Trigger RAGWorkflow for each newly stable event (ONCE per event)
            # RAGWorkflow → TwitterWorkflow handles all 3 attempts internally
            for event_info in result.get("twitter_triggered", []):
                event_id = event_info["event_id"]
                player_name = event_info["player_name"]
                team_name = event_info["team_name"]
                minute = event_info["minute"]
                extra = event_info.get("extra")
                
                # Build human-readable workflow ID
                player_last = player_name.split()[-1] if player_name else "Unknown"
                team_clean = team_name.replace(" ", "_").replace(".", "_")
                minute_str = f"{minute}+{extra}min" if extra else f"{minute}min"
                rag_workflow_id = f"rag-{team_clean}-{player_last}-{minute_str}-{event_id}"
                
                rag_workflows_started.append(rag_workflow_id)
                
                # Start RAGWorkflow (fire-and-forget)
                # RAGWorkflow triggers TwitterWorkflow which manages all 3 attempts
                await workflow.start_child_workflow(
                    RAGWorkflow.run,
                    RAGWorkflowInput(
                        fixture_id=fixture_id,
                        event_id=event_id,
                        team_name=team_name,
                        player_name=player_name,
                        minute=minute,
                        extra=extra,
                    ),
                    id=rag_workflow_id,
                    execution_timeout=timedelta(minutes=25),  # 3 attempts @ ~5min + 2x 3-min timers
                    parent_close_policy=ParentClosePolicy.ABANDON,
                )
                
                workflow.logger.info(f"🔍 Started RAGWorkflow: {rag_workflow_id}")
            
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
                if was_completed:
                    completed_count += 1
        
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
        
        workflow.logger.info(
            f"✅ Monitor complete: "
            f"staging={staging_result.get('updated_count', 0)} updated/{activated_count} activated, "
            f"active={len(fixtures)} processed/{completed_count} completed, "
            f"{len(rag_workflows_started)} RAG workflows started"
        )
        
        return {
            "staging_updated": staging_result.get("updated_count", 0),
            "staging_activated": activated_count,
            "active_fixtures_processed": len(fixtures),
            "active_fixtures_completed": completed_count,
            "rag_workflows_started": len(rag_workflows_started),
        }
