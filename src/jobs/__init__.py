"""Dagster jobs - Direct execution pipeline architecture for Found Footy

Complete workflow with direct job execution (no sensors or queues):

1. ingestion_job (daily 00:05 UTC)
   → Fetch day's fixtures from api-football.com
   → Route to fixtures_staging/active/completed based on status

2. monitor_job (every minute)
   → Activate fixtures (staging → active when start time reached)
   → Batch fetch current data for all active fixtures
   → Detect goal deltas (compare stored vs fetched counts)
   → DIRECTLY EXECUTE goal_job per fixture (via execute_in_process)

3. goal_job (per fixture_id)
   → Fetch goals from API
   → Filter out already-confirmed goals
   → Compare with goals_pending for this fixture
   → Process changes (add/confirm/drop)
   → Update fixture & complete if FT/AET/PEN + no pending goals
   → DIRECTLY EXECUTE twitter_job per confirmed goal (via execute_in_process)

4. twitter_job (per goal_id)
   → Search Twitter with 2+3+4 min retry logic
   → Extract video URLs
   → Save discovered_videos to goals_confirmed

Collections: fixtures_staging, fixtures_active, fixtures_completed, goals_pending, goals_confirmed
No queue collections - direct execution throughout
"""

from .goal import goal_job
from .ingest import ingestion_job, ingestion_schedule
from .monitor import monitor_job, monitor_schedule
from .twitter import TwitterJobConfig, twitter_job

__all__ = [
    "ingestion_job",
    "ingestion_schedule",
    "monitor_job",
    "monitor_schedule",
    "goal_job",
    "twitter_job",
    "TwitterJobConfig",
]
