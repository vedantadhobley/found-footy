"""Dagster jobs - Event-level debounce architecture for Found Footy

Complete workflow with Pro API batch endpoint and event debouncing:

1. ingestion_job (daily 00:05 UTC)
   → Fetch day's fixtures from api-football.com
   → Route to fixtures_staging/active/completed based on status

2. monitor_job (every minute)
   → Activate fixtures (staging → active when start time reached)
   → Batch fetch ALL data for active fixtures (fixtures + events + lineups in one call)
   → Update fixtures_active with fresh data
   → Trigger debounce_job for fixtures with trackable events
   → Move completed fixtures (FT/AET/PEN) to fixtures_completed

3. debounce_job (per fixture_id)
   → Extract events from fixture data
   → Process events: add new, update existing snapshots
   → Track stability: 3 consecutive identical hashes = stable
   → Confirm stable events (events_pending → events_confirmed)
   → DIRECTLY EXECUTE twitter_job per confirmed event

4. twitter_job (per event_id)
   → Search Twitter with 2+3+4 min retry logic
   → Extract video URLs
   → Save discovered_videos to events_confirmed

Collections: fixtures_staging, fixtures_active, fixtures_completed, events_pending, events_confirmed
Event-level granularity with debounce tracking
"""

from .debounce import debounce_job
from .ingest import ingestion_job, ingestion_schedule
from .monitor import monitor_job, monitor_schedule
from .twitter import TwitterJobConfig, twitter_job

__all__ = [
    "ingestion_job",
    "ingestion_schedule",
    "monitor_job",
    "monitor_schedule",
    "debounce_job",
    "twitter_job",
    "TwitterJobConfig",
]
