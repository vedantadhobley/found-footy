"""Monitor Job - Continuous fixture monitoring pipeline"""
from dagster import ScheduleDefinition, job

from .ops import (
    activate_fixtures_op,
    batch_fetch_active_op,
    complete_fixtures_op,
    detect_goal_delta_op,
    spawn_goal_jobs_op,
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
    4. Spawn goal jobs for fixtures with new goals
    5. Complete fixtures (active → completed if FT status AND no pending goals for THAT fixture)
    
    CRITICAL RULES:
    - Fixture goal counts are NOT updated until goals are validated
    - Fixtures stay in active even if FT status until ALL their goals are validated/dropped
    - This ensures late-game goals don't get lost during validation polling
    """
    # Step 1: Activate fixtures (staging → active if start time reached)
    # MUST happen before batch_fetch to ensure newly-live fixtures are included
    activate_result = activate_fixtures_op()
    
    # Step 2: Fetch fresh data for all active fixtures
    # Explicit dependency: waits for activate_result to complete
    fresh_fixtures = batch_fetch_active_op(activate_result)
    
    # Step 3: Detect goal deltas (compare stored vs fetched)
    fixtures_with_new_goals = detect_goal_delta_op(fresh_fixtures)
    
    # Step 4: Spawn goal jobs for fixtures with new goals
    spawn_goal_jobs_op(fixtures_with_new_goals)
    
    # Step 5: Complete finished fixtures (only if FT/AET/PEN/etc AND no pending goals)
    # Runs downstream from detect_goal_delta to ensure logical ordering:
    # - We detect goals first
    # - We spawn goal jobs
    # - Then we check for completion (which queries MongoDB for pending goals)
    # This ensures we've processed any new goals before checking if fixture can be completed
    complete_fixtures_op(fixtures_with_new_goals, fresh_fixtures)


# Schedule to run every minute at :00 seconds
monitor_schedule = ScheduleDefinition(
    job=monitor_job,
    cron_schedule="* * * * *",  # Every minute
    name="per_minute_monitor_schedule",
    description="Run monitor job every minute"
)
