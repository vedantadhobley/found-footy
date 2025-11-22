"""Monitor Job - Continuous fixture monitoring pipeline"""
from dagster import ScheduleDefinition, job

from .ops import (
    activate_fixtures_op,
    batch_fetch_active_op,
    detect_goal_delta_op,
    trigger_goal_jobs_op,
)


@job(
    name="monitor_job",
    description="Monitor active fixtures for goal events and completion",
    tags={"pipeline": "monitor", "frequency": "per_minute"}
)
def monitor_job():
    """
    Pipeline to monitor fixtures and detect goal events.
    
    Flow:
    1. Activate fixtures (staging → active if start time reached)
    2. Batch fetch current data for all active fixtures
    3. Detect goal deltas (compare stored vs fetched goal counts)
    4. Trigger goal_job for fixtures with new goals
    
    CRITICAL RULES:
    - Monitor does NOT update fixture data or move to completed
    - goal_job handles all fixture updates and completion
    - This ensures atomic operations per fixture
    """
    # Step 1: Activate fixtures (staging → active if start time reached)
    activate_result = activate_fixtures_op()
    
    # Step 2: Fetch fresh data for all active fixtures
    fresh_fixtures = batch_fetch_active_op(activate_result)
    
    # Step 3: Detect goal deltas (compare stored vs fetched)
    fixtures_with_new_goals = detect_goal_delta_op(fresh_fixtures)
    
    # Step 4: Trigger goal_job for fixtures with new goals
    # goal_job will handle validation, fixture updates, and completion
    trigger_goal_jobs_op(fixtures_with_new_goals)


# Schedule to run every minute at :00 seconds
monitor_schedule = ScheduleDefinition(
    job=monitor_job,
    cron_schedule="* * * * *",  # Every minute
    name="per_minute_monitor_schedule",
    description="Run monitor job every minute"
)
