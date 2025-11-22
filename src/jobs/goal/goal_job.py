"""Goal Job - Process and validate goal events for a fixture"""
from dagster import Config, job

from .ops import (
    add_to_pending_op,
    check_goal_status_op,
    cleanup_invalidated_goals_op,
    fetch_goal_events_op,
    spawn_twitter_jobs_op,
    update_fixture_goals_op,
    validate_pending_goals_op,
)


class GoalJobConfig(Config):
    """Configuration for goal job"""
    fixture_id: int


@job(
    name="goal_job",
    description="Process goal events for a fixture - validate and trigger twitter jobs",
    tags={"pipeline": "goal", "trigger": "on_demand"}
)
def goal_job():
    """
    Pipeline to process goal events for a single fixture.
    
    Flow:
    1. Fetch all goal events for the fixture
    2. Check status of each goal (confirmed/pending/new)
    3. Add new goals to goals_pending
    4. Validate pending goals (still in API?) → move to goals_confirmed
    5. Clean up invalidated goals (disappeared from API)
    6. Update fixture goal count (after validation)
    7. Spawn twitter_job for each validated goal
    
    This job is spawned by monitor_job when a goal delta is detected.
    Config will be provided at runtime via RunConfig.
    """
    # Note: fixture_id from config will be passed to fetch_goal_events_op at runtime
    # Ops will receive it through their config/context
    
    # Fetch all goal events for this fixture
    goal_events = fetch_goal_events_op()
    
    # Check status of each goal
    goal_status = check_goal_status_op(goal_events)
    
    # Add new goals to pending
    add_to_pending_op(goal_status)
    
    # Validate pending goals → move to confirmed
    validation_result = validate_pending_goals_op(goal_status)
    
    # Clean up invalidated goals
    cleanup_invalidated_goals_op(goal_status)
    
    # Update fixture with validated goals
    update_fixture_goals_op(validation_result)
    
    # Spawn twitter jobs for validated goals
    spawn_twitter_jobs_op(validation_result)
