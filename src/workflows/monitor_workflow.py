"""
Monitor Workflow - Every Minute

Tracks active fixtures, fetches fresh data, and triggers EventWorkflow for fixtures with changes.
"""
from temporalio import workflow
from temporalio.common import RetryPolicy
from datetime import timedelta
from typing import List

with workflow.unsafe.imports_passed_through():
    from src.activities import monitor as monitor_activities
    from src.workflows.event_workflow import EventWorkflow


@workflow.defn
class MonitorWorkflow:
    """Monitor active fixtures and trigger event workflows for fixtures with changes"""
    
    @workflow.run
    async def run(self) -> dict:
        """
        Workflow:
        1. Activate ready fixtures (staging → active with empty events)
        2. Batch fetch all active fixtures from API
        3. For each fixture:
           - Filter to trackable events (Goals only)
           - Generate event IDs (fixture_team_Goal_#)
           - Store in fixtures_live
           - Compare live vs active
           - If needs debounce → trigger EventWorkflow
        4. Complete finished fixtures (FT/AET/PEN → completed)
        """
        
        # Activate fixtures whose start time has been reached
        await workflow.execute_activity(
            monitor_activities.activate_fixtures,
            start_to_close_timeout=timedelta(seconds=30),
            retry_policy=RetryPolicy(maximum_attempts=2),
        )
        
        # Fetch all active fixtures from API
        fixtures = await workflow.execute_activity(
            monitor_activities.fetch_active_fixtures,
            start_to_close_timeout=timedelta(seconds=60),
            retry_policy=RetryPolicy(maximum_attempts=3),
        )
        
        # Process each fixture
        workflows_triggered = []
        for fixture_data in fixtures:
            fixture_id = fixture_data.get("fixture", {}).get("id")
            
            # Store in live and compare with active
            comparison = await workflow.execute_activity(
                monitor_activities.store_and_compare,
                args=[fixture_id, fixture_data],
                start_to_close_timeout=timedelta(seconds=10),
            )
            
            # If fixture needs debounce, trigger EventWorkflow
            if comparison.get("needs_debounce"):
                workflow_id = f"event-{fixture_id}-{workflow.now().timestamp()}"
                workflows_triggered.append(workflow_id)
                
                # Start EventWorkflow as child workflow
                await workflow.execute_child_workflow(
                    EventWorkflow.run,
                    args=[fixture_id],
                    id=workflow_id,
                )
            
            # Check if fixture is finished and should be completed
            status = fixture_data.get("fixture", {}).get("status", {}).get("short")
            if status in ["FT", "AET", "PEN"]:
                await workflow.execute_activity(
                    monitor_activities.complete_fixture_if_ready,
                    fixture_id,
                    start_to_close_timeout=timedelta(seconds=10),
                )
        
        return {
            "fixtures_processed": len(fixtures),
            "event_workflows_triggered": len(workflows_triggered),
        }
