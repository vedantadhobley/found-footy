"""Ingestion Job - Daily fixture ingestion pipeline"""
from datetime import date

from dagster import ScheduleDefinition, job

from .ops import categorize_fixtures_op, fetch_fixtures_op, store_fixtures_op


@job(
    name="ingestion_job",
    description="Daily job to ingest fixtures for the current day from api-football.com",
    tags={"pipeline": "ingestion", "frequency": "daily"}
)
def ingestion_job():
    """
    Pipeline to fetch and store daily fixtures with status-based routing.
    
    Flow:
    1. Fetch fixtures from api-football.com
    2. Categorize by status (staging/active/completed)
    3. Store in appropriate collections
    
    Benefits:
    - Fixtures already live go straight to active (catch goals immediately!)
    - Fixtures already finished go straight to completed (no wasted monitor cycles)
    - Monitor only processes fixtures actually in progress
    """
    fixtures_data = fetch_fixtures_op()
    categorized_fixtures = categorize_fixtures_op(fixtures_data)
    store_fixtures_op(categorized_fixtures)


# Schedule to run daily at 00:05 UTC
ingestion_schedule = ScheduleDefinition(
    job=ingestion_job,
    cron_schedule="5 0 * * *",  # 00:05 UTC every day
    name="daily_ingestion_schedule",
    description="Run ingestion job daily at 00:05 UTC"
)
