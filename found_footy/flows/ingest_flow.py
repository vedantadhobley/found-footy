# ✅ NEW: found_footy/flows/ingest_flow.py
from datetime import datetime, timedelta, timezone
from prefect import flow, get_run_logger
from prefect.deployments import run_deployment
from typing import Optional

from found_footy.flows.shared_tasks import (
    fixtures_process_parameters_task,
    fixtures_fetch_api_task,
    fixtures_categorize_task,
    fixtures_store_task
)
from found_footy.flows.flow_triggers import schedule_advance_flow  # ✅ UPDATE: Use renamed function
from found_footy.flows.flow_naming import generate_ingest_flow_name

@flow(
    name="ingest-flow",
    flow_run_name=generate_ingest_flow_name  # ✅ Safe - runs immediately
)
def ingest_flow(date_str: Optional[str] = None, team_ids: Optional[str] = None):
    """Pure fixtures ingest flow with status-based routing"""
    logger = get_run_logger()
    
    logger.info("📥 Starting Pure Fixtures Ingest Flow")
    
    # Process parameters
    params = fixtures_process_parameters_task(team_ids, date_str)
    
    # Fetch and categorize fixtures
    team_fixtures = fixtures_fetch_api_task(params["query_date"], params["valid_team_ids"])
    if not team_fixtures:
        return {"status": "no_fixtures", "message": "No fixtures found"}
    
    categorized = fixtures_categorize_task(team_fixtures)
    
    # Store in appropriate collections
    storage_result = fixtures_store_task(
        categorized["staging_fixtures"], 
        categorized["active_fixtures"],
        categorized["completed_fixtures"]
    )

    # Non-blocking scheduling using async client
    scheduled_advances = 0
    for fixture in categorized["staging_fixtures"]:
        try:
            # ✅ FIX: Use correct API-Football field structure
            if "fixture" in fixture and "date" in fixture["fixture"]:
                date_field = fixture["fixture"]["date"]  # "2025-09-23T09:00:00+00:00"
                fixture_id = fixture["fixture"]["id"]
            else:
                logger.warning(f"⚠️ No date field found in fixture")
                logger.debug(f"Available fixture keys: {list(fixture.keys())}")
                if "fixture" in fixture:
                    logger.debug(f"Nested fixture keys: {list(fixture['fixture'].keys())}")
                continue
            
            # ✅ Parse the kickoff time - handle timezone properly
            if date_field.endswith('Z'):
                kickoff_time = datetime.fromisoformat(date_field.replace('Z', '+00:00'))
            elif '+' in date_field or date_field.endswith('Z'):
                # Already has timezone info
                kickoff_time = datetime.fromisoformat(date_field)
            else:
                # Assume UTC if no timezone
                kickoff_time = datetime.fromisoformat(date_field + '+00:00')
            
            advance_time = kickoff_time - timedelta(minutes=3)
            
            # ✅ UPDATE: Use renamed function
            result = schedule_advance_flow("fixtures_staging", "fixtures_active", fixture_id, advance_time)
            if result["status"] in ["scheduled", "immediate"]:
                scheduled_advances += 1
                logger.info(f"✅ Scheduled advance for fixture {fixture_id} at {advance_time}")
                
        except Exception as e:
            fixture_id = fixture.get("fixture", {}).get("id", "unknown")
            logger.error(f"❌ Failed to schedule advance for fixture {fixture_id}: {e}")
            logger.debug(f"Fixture structure sample: {str(fixture)[:200]}...")

    return {
        "status": "success",
        "approach": "pure_status_based_live_only",
        "staging_fixtures": storage_result["staging_count"],
        "active_fixtures": storage_result["active_count"],
        "completed_fixtures": storage_result["completed_count"],
        "scheduled_advances": scheduled_advances,
        "note": "All team data from Prefect Variables - no MongoDB dependency"
    }