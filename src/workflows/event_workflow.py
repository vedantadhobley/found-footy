"""
Event Workflow - Per Fixture with Changes

Debounces all events for a single fixture, then triggers TwitterWorkflow for stable events.
"""
from temporalio import workflow
from temporalio.common import RetryPolicy
from datetime import timedelta

with workflow.unsafe.imports_passed_through():
    from src.activities import event as event_activities
    from src.workflows.twitter_workflow import TwitterWorkflow


@workflow.defn
class EventWorkflow:
    """Debounce all events for a fixture"""
    
    @workflow.run
    async def run(self, fixture_id: int) -> dict:
        """
        Workflow:
        1. Get live events and active events for fixture
        2. Iterate and compare:
           - Event in both: check hash, increment/reset stable_count
           - Event in live only: add NEW event to active
           - Event in active only: mark REMOVED (VAR)
        3. For events with stable_count >= 3:
           - Mark debounce_complete
           - Trigger TwitterWorkflow
        """
        
        # Debounce all events for this fixture
        result = await workflow.execute_activity(
            event_activities.debounce_fixture_events,
            fixture_id,
            start_to_close_timeout=timedelta(seconds=60),
            retry_policy=RetryPolicy(maximum_attempts=2),
        )
        
        # For each event that completed debounce, trigger Twitter workflow
        twitter_triggered = result.get("twitter_triggered", [])
        for event_id in twitter_triggered:
            workflow_id = f"twitter-{fixture_id}-{event_id}"
            
            # Start TwitterWorkflow as child workflow
            await workflow.execute_child_workflow(
                TwitterWorkflow.run,
                args=[fixture_id, event_id],
                id=workflow_id,
            )
        
        return {
            "fixture_id": fixture_id,
            "new_events": result.get("new_events", 0),
            "updated_events": result.get("updated_events", 0),
            "completed_events": result.get("completed_events", 0),
            "twitter_triggered": len(twitter_triggered),
        }
