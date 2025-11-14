"""Dagster schedules - automated asset materialization"""
from dagster import ScheduleDefinition, AssetSelection


# Daily fixture ingestion at midnight UTC
daily_ingest_schedule = ScheduleDefinition(
    name="daily_ingest_schedule",
    description="Ingest fixtures daily at midnight UTC",
    target=AssetSelection.assets("ingest_fixtures"),
    cron_schedule="0 0 * * *",  # Midnight UTC
)

# Monitor active fixtures every 3 minutes
monitor_schedule = ScheduleDefinition(
    name="monitor_schedule",
    description="Monitor active fixtures for goal changes every 3 minutes",
    target=AssetSelection.assets("monitor_fixtures"),
    cron_schedule="*/3 * * * *",  # Every 3 minutes
)
