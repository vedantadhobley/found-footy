"""Fixture ingestion job - fetches and categorizes fixtures from API-Football

Migrated from found_footy/flows/ingest_flow.py
"""

import logging
from typing import Optional

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
    """
    context.log.info("📥 Starting Fixtures Ingest")
    
    # Process parameters
    params = fixture_logic.process_parameters(config.team_ids, config.date_str)
    
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
        staging_fixtures=categorized.get("staging", []),
        active_fixtures=categorized.get("active", []),
        completed_fixtures=categorized.get("completed", []),
        store=store
    )
    
    total_stored = sum(storage_results.values())
    context.log.info(f"✅ Stored {total_stored} fixtures: {storage_results}")
    
    return {
        "status": "success",
        "stored": storage_results,
        "query_date": params["query_date"],
        "team_ids": params["valid_team_ids"],
        "total_fixtures": total_stored
    }


@job(
    name="ingest_fixtures",
    description="Fetch and categorize fixtures from API-Football"
)
def ingest_fixtures_job():
    """Fixture ingestion workflow"""
    ingest_fixtures_op()
