"""Dagster sensors - watch for job completions and trigger downstream jobs"""

from dagster import (
    sensor,
    RunRequest,
    SensorEvaluationContext,
    DagsterRunStatus,
    RunsFilter,
    SensorDefinition,
)
from src.jobs import (
    process_goals_job,
    scrape_twitter_job,
    download_videos_job,
    filter_videos_job,
)


@sensor(
    name="monitor_to_process_goals",
    job=process_goals_job,
    description="Triggers process_goals_job when monitor detects goals"
)
def monitor_to_process_goals_sensor(context: SensorEvaluationContext):
    """
    Watches monitor_fixtures_job runs for goal detections.
    For each fixture with goals, triggers process_goals_job.
    """
    # Get recent completed runs of monitor_fixtures_job
    runs = context.instance.get_runs(
        filters=RunsFilter(
            job_name="monitor_fixtures",
            statuses=[DagsterRunStatus.SUCCESS],
        ),
        limit=1,
    )
    
    if not runs:
        return
    
    latest_run = runs[0]
    
    # Check if we've already processed this run
    cursor = context.cursor or ""
    if latest_run.run_id == cursor:
        return
    
    # Get the output from the monitor job
    # In a real implementation, you'd fetch the actual goal data from MongoDB
    # For now, we'll create a simple example
    
    context.update_cursor(latest_run.run_id)
    
    # TODO: Query MongoDB for fixtures with new goals
    # For each fixture with goals, yield a RunRequest
    
    # Example (you'll need to implement the actual MongoDB query):
    # fixtures_with_goals = get_fixtures_with_new_goals()
    # for fixture in fixtures_with_goals:
    #     yield RunRequest(
    #         run_key=f"process_goals_{fixture['fixture_id']}_{fixture['timestamp']}",
    #         run_config={
    #             "ops": {
    #                 "validate_and_store_goals_op": {
    #                     "config": {
    #                         "fixture_id": fixture["fixture_id"],
    #                         "goal_events": fixture["goal_events"]
    #                     }
    #                 }
    #             }
    #         }
    #     )


@sensor(
    name="process_goals_to_scrape_twitter",
    job=scrape_twitter_job,
    description="Triggers scrape_twitter_job when goals are processed"
)
def process_goals_to_scrape_twitter_sensor(context: SensorEvaluationContext):
    """
    Watches process_goals_job runs for new goal IDs.
    For each new goal, triggers scrape_twitter_job.
    """
    runs = context.instance.get_runs(
        filters=RunsFilter(
            job_name="process_goals",
            statuses=[DagsterRunStatus.SUCCESS],
        ),
        limit=10,
    )
    
    cursor = context.cursor or ""
    processed_run_ids = set(cursor.split(",")) if cursor else set()
    
    for run in runs:
        if run.run_id in processed_run_ids:
            continue
        
        # TODO: Get the new goal IDs from the run output or MongoDB
        # For each goal, yield a RunRequest
        
        # Example:
        # new_goal_ids = get_new_goal_ids_from_run(run.run_id)
        # for goal_id in new_goal_ids:
        #     yield RunRequest(
        #         run_key=f"scrape_twitter_{goal_id}_{run.run_id}",
        #         run_config={
        #             "ops": {
        #                 "search_twitter_videos_op": {
        #                     "config": {
        #                         "goal_id": goal_id
        #                     }
        #                 }
        #             }
        #         }
        #     )
        
        processed_run_ids.add(run.run_id)
    
    context.update_cursor(",".join(processed_run_ids))


@sensor(
    name="scrape_twitter_to_download_videos",
    job=download_videos_job,
    description="Triggers download_videos_job when videos are discovered"
)
def scrape_twitter_to_download_videos_sensor(context: SensorEvaluationContext):
    """
    Watches scrape_twitter_job runs for discovered videos.
    For each goal with videos, triggers download_videos_job.
    """
    runs = context.instance.get_runs(
        filters=RunsFilter(
            job_name="scrape_twitter",
            statuses=[DagsterRunStatus.SUCCESS],
        ),
        limit=10,
    )
    
    cursor = context.cursor or ""
    processed_run_ids = set(cursor.split(",")) if cursor else set()
    
    for run in runs:
        if run.run_id in processed_run_ids:
            continue
        
        # TODO: Get the goal_id from the run and check if videos were found
        # If videos found, trigger download
        
        processed_run_ids.add(run.run_id)
    
    context.update_cursor(",".join(processed_run_ids))


@sensor(
    name="download_videos_to_filter",
    job=filter_videos_job,
    description="Triggers filter_videos_job after videos are downloaded"
)
def download_videos_to_filter_sensor(context: SensorEvaluationContext):
    """
    Watches download_videos_job runs for successful downloads.
    For each goal with downloaded videos, triggers filter_videos_job.
    """
    runs = context.instance.get_runs(
        filters=RunsFilter(
            job_name="download_videos",
            statuses=[DagsterRunStatus.SUCCESS],
        ),
        limit=10,
    )
    
    cursor = context.cursor or ""
    processed_run_ids = set(cursor.split(",")) if cursor else set()
    
    for run in runs:
        if run.run_id in processed_run_ids:
            continue
        
        # TODO: Get the goal_id from the run
        # If videos were successfully downloaded, trigger filter
        
        processed_run_ids.add(run.run_id)
    
    context.update_cursor(",".join(processed_run_ids))


# Export all sensors
__all__ = [
    "monitor_to_process_goals_sensor",
    "process_goals_to_scrape_twitter_sensor",
    "scrape_twitter_to_download_videos_sensor",
    "download_videos_to_filter_sensor",
]
