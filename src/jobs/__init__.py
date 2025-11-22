"""Dagster jobs - Clean modular pipeline architecture for Found Footy

Complete workflow with proper validation and separation of concerns:

1. ingestion_job (daily 00:05 UTC)
   → Fetch day's fixtures from api-football.com
   → Store in fixtures_staging

2. monitor_job (every minute)
   → Activate fixtures (staging → active when start time reached)
   → Batch fetch current data for all active fixtures
   → Detect goal deltas (compare stored vs fetched counts)
   → Spawn goal_job for each fixture with new goals
   → Complete fixtures (active → completed, only if no pending goals)

3. goal_job (spawned per fixture with new goals)
   → Fetch goal events from API
   → Check status (confirmed/pending/new)
   → Add new goals to goals_pending
   → Validate pending goals → move to goals_confirmed
   → Clean up invalidated goals (disappeared from API)
   → Update fixture (only after validation)
   → Spawn twitter_job for each validated goal

4. twitter_job (spawned per validated goal)
   → Search Twitter for goal videos
   → Extract video URLs
   → Download videos
   → Upload to S3
"""

from .goal import GoalJobConfig, goal_job
from .ingest import ingestion_job, ingestion_schedule
from .monitor import monitor_job, monitor_schedule
from .twitter import TwitterJobConfig, twitter_job

__all__ = [
    "ingestion_job",
    "ingestion_schedule",
    "monitor_job",
    "monitor_schedule",
    "goal_job",
    "GoalJobConfig",
    "twitter_job",
    "TwitterJobConfig",
]
