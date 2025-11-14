"""
Fixture processing business logic - no orchestration dependencies.
Pure Python functions that can be called from Dagster assets.
"""
import logging
from datetime import datetime, timezone
from typing import Optional, List, Dict, Any

from src.api.mongo_api import (
    fixtures, 
    fixtures_batch, 
    filter_fixtures_by_teams,
    parse_team_ids_parameter
)
from src.data.mongo_store import FootyMongoStore
from src.utils.team_data import get_team_ids
from src.utils.fixture_status import get_fixture_statuses

logger = logging.getLogger(__name__)


def process_parameters(team_ids=None, date_str=None) -> Dict[str, Any]:
    """Parse and validate input parameters"""
    # Process date
    if date_str is None:
        query_date = datetime.now(timezone.utc).date()
        logger.info(f"📅 Using today's date: {query_date}")
    else:
        try:
            if len(date_str) == 8:
                query_date = datetime.strptime(date_str, "%Y%m%d").date()
            else:
                query_date = datetime.strptime(date_str[:10], "%Y-%m-%d").date()
            logger.info(f"📅 Using provided date: {query_date}")
        except (ValueError, TypeError):
            query_date = datetime.now(timezone.utc).date()
            logger.warning(f"⚠️ Invalid date format, using today: {query_date}")
    
    # Get team IDs
    available_team_ids = get_team_ids()
    valid_team_ids = available_team_ids
    
    if team_ids and str(team_ids).strip() != "null":
        try:
            parsed_team_ids = parse_team_ids_parameter(team_ids)
            if parsed_team_ids:
                valid_team_ids = [tid for tid in parsed_team_ids if tid in available_team_ids]
                logger.info(f"⚽ Using {len(valid_team_ids)} parsed team IDs")
            else:
                logger.info("⚽ No valid parsed team IDs, using all teams")
        except Exception as e:
            logger.warning(f"⚠️ Error parsing team_ids: {e}, using all teams")
    
    logger.info(f"⚽ Using {len(valid_team_ids)} teams")
    
    return {
        "query_date": query_date,
        "valid_team_ids": valid_team_ids
    }


def fetch_fixtures(query_date, valid_team_ids) -> List[Dict]:
    """Fetch and filter fixtures from API"""
    all_fixtures = fixtures(query_date)
    team_fixtures = filter_fixtures_by_teams(all_fixtures, valid_team_ids)
    
    if not team_fixtures:
        logger.warning("❌ No fixtures found")
        return []
    
    logger.info(f"✅ Found {len(team_fixtures)} relevant fixtures")
    return team_fixtures


def categorize_fixtures(team_fixtures: List[Dict]) -> Dict[str, List[Dict]]:
    """Categorize fixtures by FIFA status - pure status-driven routing"""
    staging_fixtures = []
    active_fixtures = []
    completed_fixtures = []
    
    # Get statuses configuration
    try:
        status_config = get_fixture_statuses()
        completed_statuses = set(status_config["completed"])
        active_statuses = set(status_config["active"])
        staging_statuses = set(status_config["staging"])
    except Exception as e:
        logger.warning(f"⚠️ Could not load status config: {e}")
        # Fallback to hardcoded values
        completed_statuses = {"FT", "AET", "PEN", "PST", "CANC", "ABD", "AWD", "WO"}
        active_statuses = {"1H", "HT", "2H", "ET", "BT", "P", "SUSP", "INT", "LIVE"}
        staging_statuses = {"TBD", "NS"}
    
    # Get full fixture data including current scores
    fixture_ids = [fixture["fixture"]["id"] for fixture in team_fixtures]
    logger.info(f"🔍 Getting detailed data for {len(fixture_ids)} fixtures...")
    
    try:
        api_fixtures_data = fixtures_batch(fixture_ids)
        api_lookup = {f["fixture"]["id"]: f for f in api_fixtures_data}
    except Exception as e:
        logger.warning(f"⚠️ Could not get detailed fixture data: {e}")
        api_lookup = {}
    
    for fixture in team_fixtures:
        fixture_id = fixture["fixture"]["id"]
        
        # Get detailed API data
        api_data = api_lookup.get(fixture_id, fixture)  # Use fixture as fallback
        
        # Extract status
        status = api_data.get("fixture", {}).get("status", {}).get("short", "NS")
        
        # Use the raw fixture data as-is
        processed_fixture = api_data
        
        # STATUS-BASED ROUTING
        if status in completed_statuses:
            completed_fixtures.append(processed_fixture)
            logger.info(f"📋 COMPLETED: Fixture {fixture_id} ({status})")
            
        elif status in active_statuses:
            active_fixtures.append(processed_fixture)
            logger.info(f"🔴 ACTIVE: Fixture {fixture_id} ({status})")
            
        elif status in staging_statuses:
            staging_fixtures.append(processed_fixture)
            logger.info(f"📅 STAGING: Fixture {fixture_id} ({status})")
                
        else:
            logger.warning(f"❓ UNKNOWN STATUS: Fixture {fixture_id} has status '{status}'")
            staging_fixtures.append(processed_fixture)  # Default to staging
    
    logger.info(f"📊 CATEGORIZATION: {len(staging_fixtures)} staging, {len(active_fixtures)} active, {len(completed_fixtures)} completed")
    
    return {
        "staging_fixtures": staging_fixtures,
        "active_fixtures": active_fixtures,
        "completed_fixtures": completed_fixtures
    }


def store_fixtures(staging_fixtures: List[Dict], active_fixtures: List[Dict], 
                   completed_fixtures: List[Dict], store: FootyMongoStore) -> Dict[str, int]:
    """Store fixtures in appropriate collections"""
    staging_count = store.bulk_insert_fixtures(staging_fixtures, "fixtures_staging") if staging_fixtures else 0
    active_count = store.bulk_insert_fixtures(active_fixtures, "fixtures_active") if active_fixtures else 0
    completed_count = store.bulk_insert_fixtures(completed_fixtures, "fixtures_completed") if completed_fixtures else 0
    
    logger.info(f"💾 STORAGE: {staging_count} staging, {active_count} active, {completed_count} completed")
    
    return {
        "staging_count": staging_count,
        "active_count": active_count,
        "completed_count": completed_count
    }


def detect_fixture_deltas(store: FootyMongoStore) -> Dict[str, Any]:
    """Bulk delta detection for entire fixtures_active collection"""
    active_fixtures = store.get_active_fixtures()
    if not active_fixtures:
        logger.info("⏸️ No active fixtures for delta detection")
        return {
            "status": "success",
            "fixtures_with_changes": [], 
            "fixtures_completed": [], 
            "total_goals_detected": 0
        }
    
    fixture_ids = [store._extract_fixture_id(f) for f in active_fixtures]
    logger.info(f"🔍 Running bulk delta detection on {len(fixture_ids)} fixtures")
    
    try:
        # Bulk API call for all active fixtures
        api_fixtures_data = fixtures_batch(fixture_ids)
        api_lookup = {f["fixture"]["id"]: f for f in api_fixtures_data}
        
        fixtures_with_changes = []
        fixtures_completed = []
        total_goals_detected = 0
        
        for active_fixture in active_fixtures:
            fixture_id = store._extract_fixture_id(active_fixture)
            api_data = api_lookup.get(fixture_id)
            
            if not api_data:
                continue
                
            delta_result = store.fixtures_delta(fixture_id, api_data)
            
            if delta_result.get("goals_changed"):
                fixtures_with_changes.append({
                    "fixture_id": fixture_id,
                    "delta_result": delta_result
                })
                total_goals_detected += delta_result.get("total_goal_increase", 0)
                
            if delta_result.get("status_changed_to_completed"):
                fixtures_completed.append({
                    "fixture_id": fixture_id,
                    "delta_result": delta_result
                })
        
        logger.info(f"✅ Delta detection: {len(fixtures_with_changes)} with goals, {len(fixtures_completed)} completed")
        
        return {
            "status": "success",
            "fixtures_with_changes": fixtures_with_changes,
            "fixtures_completed": fixtures_completed,
            "total_goals_detected": total_goals_detected
        }
        
    except Exception as e:
        logger.error(f"❌ Bulk delta detection failed: {e}")
        return {
            "status": "error", 
            "fixtures_with_changes": [], 
            "fixtures_completed": [], 
            "total_goals_detected": 0, 
            "error": str(e)
        }


def advance_fixtures(source_collection: str, destination_collection: str, 
                    fixture_id: Optional[int], store: FootyMongoStore) -> Dict[str, Any]:
    """Generic function to advance fixtures between collections"""
    try:
        if fixture_id:
            logger.info(f"📋 Advancing fixture {fixture_id}: {source_collection} → {destination_collection}")
        else:
            logger.info(f"📋 Advancing all fixtures: {source_collection} → {destination_collection}")
        
        result = store.fixtures_advance(source_collection, destination_collection, fixture_id)
        logger.info(f"✅ Advanced {result.get('advanced_count', 0)} fixtures")
        
        return result
        
    except Exception as e:
        logger.error(f"❌ Error advancing fixtures: {e}")
        return {"status": "error", "advanced_count": 0, "error": str(e)}
