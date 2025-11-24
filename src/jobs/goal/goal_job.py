"""Goal Job - Process and validate goal events for a fixture"""
from dagster import job

from .ops import (
    compare_with_pending_op,
    fetch_fixture_goals_op,
    filter_confirmed_goals_op,
    process_goal_changes_op,
    trigger_twitter_jobs_op,
)


@job(
    name="goal_job",
    description="Validate goal events for a fixture and trigger twitter jobs",
    tags={"pipeline": "goal", "trigger": "monitor"}
)
def goal_job():
    """
    Pipeline to validate goal events for a single fixture.
    
    Flow:
    1. Fetch goals from API
    2. Filter out already-confirmed goals
    3. Compare remaining with goals_pending
    4. Process changes (add/confirm/drop)
    5. Trigger twitter jobs for confirmed goals
    
    This job is triggered by monitor_job when goal delta detected.
    Config (fixture_id) will be provided at runtime.
    
    NOTE: Fixture data updates and completion are handled by monitor_job.
    This job ONLY validates goals.
    """
    # Step 1: Fetch goals from API
    fetch_result = fetch_fixture_goals_op()
    
    # Step 2: Filter out already-confirmed goals
    filter_result = filter_confirmed_goals_op(fetch_result)
    
    # Step 3: Compare with goals_pending for this fixture
    compare_result = compare_with_pending_op(filter_result)
    
    # Step 4: Process changes (add/confirm/drop)
    process_result = process_goal_changes_op(compare_result)
    
    # Step 5: Trigger twitter jobs for confirmed goals
    trigger_twitter_jobs_op(process_result)
