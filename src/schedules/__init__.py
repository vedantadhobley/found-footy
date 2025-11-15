"""Dagster schedules - automated job execution"""
from dagster import ScheduleDefinition, DefaultScheduleStatus
from src.jobs import ingest_fixtures_job, monitor_fixtures_job, advance_fixtures_job


# Daily fixture ingestion at 00:05 UTC
daily_ingest_schedule = ScheduleDefinition(
    name="daily_ingest_schedule",
    description="Ingest fixtures daily at 00:05 UTC",
    job=ingest_fixtures_job,
    cron_schedule="5 0 * * *",  # 00:05 UTC
)

# Advance fixtures from staging to active at kickoff time
advance_schedule = ScheduleDefinition(
    name="advance_schedule",
    description="Check for fixtures ready to advance from staging to active (runs every minute)",
    job=advance_fixtures_job,
    cron_schedule="* * * * *",  # Every minute
    default_status=DefaultScheduleStatus.RUNNING,  # ✅ Auto-start
)

# Monitor active fixtures every 5 minutes
monitor_schedule = ScheduleDefinition(
    name="monitor_schedule",
    description="Monitor active fixtures for goal changes every 5 minutes",
    job=monitor_fixtures_job,
    cron_schedule="*/5 * * * *",  # Every 5 minutes
    default_status=DefaultScheduleStatus.RUNNING,  # ✅ Auto-start
)
