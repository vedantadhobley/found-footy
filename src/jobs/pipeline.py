"""Main pipeline job - shows complete goal processing flow

This is a composed job that chains together the full workflow.
It provides visibility into the complete pipeline in Dagster UI.
"""

from dagster import job, op, OpExecutionContext, DynamicOut, DynamicOutput, Config
from typing import List, Dict, Any

# Import all the individual ops
from src.jobs.monitor import monitor_fixtures_op
from src.jobs.process_goals import validate_and_store_goals_op, ProcessGoalsConfig
from src.jobs.scrape_twitter import search_twitter_videos_op, ScrapeTwitterConfig
from src.jobs.download_videos import (
    get_videos_to_download_op,
    download_and_upload_video_op,
    update_goal_completion_op,
    DownloadVideosConfig
)
from src.jobs.filter_videos import deduplicate_videos_op, FilterVideosConfig


@op(
    name="fan_out_fixtures",
    description="Fan out fixtures with goals to parallel processing",
    out=DynamicOut(Dict[str, Any])
)
def fan_out_fixtures_op(
    context: OpExecutionContext,
    monitor_result: Dict[str, Any]
):
    """
    Takes monitor results and creates dynamic outputs for each fixture with goals.
    This allows parallel processing of multiple fixtures.
    """
    # In a real implementation, query MongoDB for fixtures with detected goals
    # For now, this is a placeholder that shows the pattern
    
    fixtures_to_process = []  # TODO: Get from MongoDB based on monitor_result
    
    if not fixtures_to_process:
        context.log.info("No fixtures to process")
        return
    
    for fixture_data in fixtures_to_process:
        yield DynamicOutput(
            value={
                "fixture_id": fixture_data["fixture_id"],
                "goal_events": fixture_data["goal_events"]
            },
            mapping_key=f"fixture_{fixture_data['fixture_id']}"
        )


@op(
    name="fan_out_goals",
    description="Fan out goals for parallel Twitter scraping",
    out=DynamicOut(str)
)
def fan_out_goals_op(
    context: OpExecutionContext,
    new_goal_ids: List[str]
):
    """
    Takes list of new goal IDs and creates dynamic outputs for parallel processing.
    """
    if not new_goal_ids:
        context.log.info("No new goals to process")
        return
    
    for goal_id in new_goal_ids:
        yield DynamicOutput(
            value=goal_id,
            mapping_key=f"goal_{goal_id.replace('_', '-')}"
        )


@job(
    name="goal_processing_pipeline",
    description="Complete pipeline: Monitor → Process → Scrape → Download → Filter"
)
def goal_processing_pipeline():
    """
    Main pipeline that shows the complete workflow in Dagster UI.
    
    Flow:
    1. Monitor active fixtures for goal changes
    2. Process each fixture with goals (parallel)
    3. For each new goal, scrape Twitter (parallel)
    4. Download videos for each goal
    5. Filter/deduplicate videos
    
    This provides full visibility into the pipeline while still allowing
    individual jobs to be run manually.
    """
    # Step 1: Monitor fixtures
    monitor_result = monitor_fixtures_op()
    
    # Step 2: Fan out to parallel fixture processing
    # (In practice, you'd query MongoDB here and create dynamic outputs)
    
    # Step 3: Process goals for each fixture
    # Step 4: Fan out to parallel Twitter scraping
    # Step 5: Download videos
    # Step 6: Filter videos
    
    # Note: This is a skeleton - the actual implementation would need
    # proper dynamic mapping and config passing


# Keep the individual jobs for manual execution
__all__ = [
    "goal_processing_pipeline",
]
