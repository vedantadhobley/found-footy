"""Monitor Job - Continuous fixture monitoring pipeline"""
from dagster import ScheduleDefinition, job

from .ops import (
    activate_fixtures_op,
    batch_fetch_active_op,
    process_and_debounce_events_op,
)


@job(
    name="monitor_job",
    description="Monitor active fixtures for events and completion",
    tags={"pipeline": "monitor", "frequency": "per_minute"}
)
def monitor_job():
    """
    Pipeline to monitor fixtures and process events.
    
    Flow:
    1. Activate fixtures (staging → active if start time reached)
    2. Batch fetch current data for all active fixtures (includes events array)
    3. Update fixtures and trigger debounce_job for fixtures with trackable events
    
    Event Processing:
    - Batch API returns full fixture data including events array
    - Only trackable events (Goals with specific details) are processed
    - debounce_job handles event-level debounce tracking
    - Stable events are confirmed and trigger twitter_job
    
    Fixture Completion:
    - Fixtures with status FT/AET/PEN are moved to fixtures_completed
    """
    # Step 1: Activate fixtures (staging → active if start time reached)
    activate_result = activate_fixtures_op()
    
    # Step 2: Fetch fresh data for all active fixtures (includes events)
    fresh_fixtures = batch_fetch_active_op(activate_result)
    
    # Step 3: Update fixtures, trigger debounce for events, move completed
    process_and_debounce_events_op(fresh_fixtures)


# Schedule to run every minute at :00 seconds
monitor_schedule = ScheduleDefinition(
    job=monitor_job,
    cron_schedule="* * * * *",  # Every minute
    name="per_minute_monitor_schedule",
    description="Run monitor job every minute"
)
