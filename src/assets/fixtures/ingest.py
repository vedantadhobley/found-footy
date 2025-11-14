"""Fixture ingestion asset - fetches and categorizes fixtures from API-Football"""

import logging
from typing import Optional

from dagster import asset, AssetExecutionContext, Output
from src.utils import fixture_logic
from src.data.mongo_store import FootyMongoStore

logger = logging.getLogger(__name__)


@asset(
    name="ingest_fixtures",
    description="Fetch fixtures from API-Football, categorize by status, and store to MongoDB",
    group_name="fixtures",
    compute_kind="api"
)
def ingest_fixtures_asset(
    context: AssetExecutionContext,
    date_str: Optional[str] = None,
    team_ids: Optional[str] = None
) -> Output:
    """
    Ingest fixtures asset - migrated from ingest_flow.py
    
    Steps:
    1. Process parameters (date and team IDs)
    2. Fetch fixtures from API-Football
    3. Categorize by status (staging/active/completed)
    4. Store to appropriate MongoDB collections
    """
    context.log.info("📥 Starting Fixtures Ingest")
    
    # Process parameters
    params = fixture_logic.process_parameters(team_ids, date_str)
    
    # Fetch and categorize fixtures
    team_fixtures = fixture_logic.fetch_fixtures(
        params["query_date"],
        params["valid_team_ids"]
    )
    
    if not team_fixtures:
        context.log.warning("No fixtures found")
        return Output(
            value={"status": "no_fixtures", "message": "No fixtures found"},
            metadata={
                "fixtures_found": 0,
                "query_date": params["query_date"]
            }
        )
    
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
    
    result = {
        "status": "success",
        "stored": storage_results,
        "query_date": params["query_date"],
        "team_ids": params["valid_team_ids"]
    }
    
    return Output(
        value=result,
        metadata={
            "total_fixtures": total_stored,
            "staging": storage_results.get("staging", 0),
            "active": storage_results.get("active", 0),
            "completed": storage_results.get("completed", 0)
        }
    )
