"""
Monitor Workflow - Every Minute

Tracks active fixtures, fetches fresh data, and processes events inline.
No EventWorkflow needed with player_id in event_id!
"""
from temporalio import workflow
from temporalio.common import RetryPolicy
from datetime import timedelta
from typing import List

with workflow.unsafe.imports_passed_through():
    from src.activities import monitor as monitor_activities
    from src.workflows.twitter_workflow import TwitterWorkflow


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
           - Generate event IDs with player_id: {fixture}_{team}_{player}_{type}_{#}
           - Store in fixtures_live
           - Process events inline (pure set comparison - no hash!)
           - Trigger TwitterWorkflow directly for stable events
        4. Complete finished fixtures (FT/AET/PEN → completed)
        
        Note: With player_id in event_id, VAR scenarios handled automatically:
        - Player changes → different event_id → old removed, new added
        - Goal cancelled → event_id disappears → marked removed
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
        twitter_workflows_triggered = []
        
        for fixture_data in fixtures:
            fixture_id = fixture_data.get("fixture", {}).get("id")
            
            # Store in live
            await workflow.execute_activity(
                monitor_activities.store_and_compare,
                args=[fixture_id, fixture_data],
                start_to_close_timeout=timedelta(seconds=10),
            )
            
            # Process events inline (no EventWorkflow needed!)
            result = await workflow.execute_activity(
                monitor_activities.process_fixture_events,
                fixture_id,
                start_to_close_timeout=timedelta(seconds=60),
                retry_policy=RetryPolicy(maximum_attempts=3),
            )
            
            # Trigger TwitterWorkflow for each newly stable event
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
                twitter_id = f"twitter-{team_clean}-{player_last}-{minute_str}-{event_id}"
                
                twitter_workflows_triggered.append(twitter_id)
                
                # Start TwitterWorkflow as child
                await workflow.execute_child_workflow(
                    TwitterWorkflow.run,
                    args=[fixture_id, event_id, player_name, team_name],
                    id=twitter_id,
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
            "twitter_workflows_triggered": len(twitter_workflows_triggered),
            "active_fixture_count": len(fixtures),
        }
