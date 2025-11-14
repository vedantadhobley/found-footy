"""Dagster schedules - automated job execution"""
from dagster import ScheduleDefinition
from src.jobs import ingest_fixtures_job, monitor_fixtures_job


# Daily fixture ingestion at midnight UTC
daily_ingest_schedule = ScheduleDefinition(
    name="daily_ingest_schedule",
    description="Ingest fixtures daily at midnight UTC",
    job=ingest_fixtures_job,
    cron_schedule="0 0 * * *",  # Midnight UTC
)

# Monitor active fixtures every 3 minutes
monitor_schedule = ScheduleDefinition(
    name="monitor_schedule",
    description="Monitor active fixtures for goal changes every 3 minutes",
    job=monitor_fixtures_job,
    cron_schedule="*/3 * * * *",  # Every 3 minutes
)
