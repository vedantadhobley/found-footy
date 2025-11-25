"""Fixture ingestion job - fetches and categorizes fixtures from API-Football

Migrated from found_footy/flows/ingest_flow.py

Flow:
1. Fetch fixtures and store in staging
2. Schedule advance jobs to run 3 minutes before each fixture's kickoff
3. Advance jobs move fixtures from staging → active at game time
"""

import logging
from typing import Optional
from datetime import datetime, timedelta

from dagster import job, op, OpExecutionContext, Config
from src.utils import fixture_logic
from src.data.mongo_store import FootyMongoStore

logger = logging.getLogger(__name__)


class IngestFixturesConfig(Config):
    """Configuration for ingesting fixtures"""
    date_str: Optional[str] = None
    team_ids: Optional[str] = None


@op(
    name="ingest_fixtures_op",
    description="Fetch fixtures from API-Football, categorize by status, and store to MongoDB"
)
def ingest_fixtures_op(
    context: OpExecutionContext,
    config: IngestFixturesConfig
) -> dict:
    """
    Ingest fixtures:
    1. Process parameters (date and team IDs)
    2. Fetch fixtures from API-Football
    3. Categorize by status (staging/active/completed)
    4. Store to appropriate MongoDB collections
    
    Args:
        date_str: Date in YYYY-MM-DD or YYYYMMDD format (defaults to today if None/empty)
        team_ids: Comma-separated team IDs (defaults to all configured teams if None/empty)
    """
    context.log.info("📥 Starting Fixtures Ingest")
    
    # Normalize empty strings to None for proper default handling
    date_str = config.date_str if config.date_str and config.date_str.strip() else None
    team_ids = config.team_ids if config.team_ids and config.team_ids.strip() else None
    
    # Process parameters
    params = fixture_logic.process_parameters(team_ids, date_str)
    
    # Fetch and categorize fixtures
    team_fixtures = fixture_logic.fetch_fixtures(
        params["query_date"],
        params["valid_team_ids"]
    )
    
    if not team_fixtures:
        context.log.warning("No fixtures found")
        return {
            "status": "no_fixtures",
            "message": "No fixtures found",
            "query_date": params["query_date"]
        }
    
    # Categorize by status
    categorized = fixture_logic.categorize_fixtures(team_fixtures)
    
    # Store to MongoDB
    store = FootyMongoStore()
    storage_results = fixture_logic.store_fixtures(
        staging_fixtures=categorized.get("staging_fixtures", []),
        active_fixtures=categorized.get("active_fixtures", []),
        completed_fixtures=categorized.get("completed_fixtures", []),
        store=store
    )
    
    total_stored = sum(storage_results.values())
    context.log.info(f"✅ Stored {total_stored} fixtures: {storage_results}")
    
    # Dagster pattern: fixtures are stored with kickoff times
    # advance_fixtures job (scheduled every 1 min) will check and advance ready fixtures
    staging_fixtures = categorized.get("staging_fixtures", [])
    if staging_fixtures:
        context.log.info(f"📅 {len(staging_fixtures)} staging fixtures will be checked by advance_fixtures schedule")
    
    return {
        "status": "success",
        "stored": storage_results,
        "query_date": params["query_date"],
        "team_ids": params["valid_team_ids"],
        "total_fixtures": total_stored,
        "staging_fixtures": len(staging_fixtures)
    }


@job(
    name="ingest_fixtures",
    description="Fetch and categorize fixtures from API-Football"
)
def ingest_fixtures_job():
    """Fixture ingestion workflow"""
    ingest_fixtures_op()
