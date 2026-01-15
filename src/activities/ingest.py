"""Ingest activities"""
from temporalio import activity
from typing import Dict, List, Any
from datetime import date


@activity.defn
async def fetch_todays_fixtures(target_date_str: str | None = None) -> List[Dict[str, Any]]:
    """
    Fetch fixtures for the given date (defaults to today).
    Filters to only tracked teams (50 teams: 25 UEFA + 25 FIFA).
    
    Args:
        target_date_str: ISO format date string (e.g., "2025-12-26") or None for today
    """
    if target_date_str:
        target_date = date.fromisoformat(target_date_str)
    else:
        target_date = date.today()
    
    activity.logger.info(f"🌐 Fetching fixtures for {target_date}")
    
    try:
        # Import here to avoid circular imports
        from src.api.api_client import get_fixtures_for_date
        from src.utils.team_data import get_team_ids
        
        # Get all fixtures for the date
        all_fixtures = get_fixtures_for_date(target_date)
        activity.logger.info(f"📥 Retrieved {len(all_fixtures)} total fixtures from API")
        
        # Filter to only our tracked teams
        tracked_team_ids = set(get_team_ids())
        
        filtered_fixtures = [
            fixture for fixture in all_fixtures
            if fixture.get("teams", {}).get("home", {}).get("id") in tracked_team_ids
            or fixture.get("teams", {}).get("away", {}).get("id") in tracked_team_ids
        ]
        
        activity.logger.info(
            f"✅ Filtered to {len(filtered_fixtures)} fixtures for our {len(tracked_team_ids)} tracked teams "
            f"(removed {len(all_fixtures) - len(filtered_fixtures)} irrelevant fixtures)"
        )
        
        return filtered_fixtures
    
    except Exception as e:
        activity.logger.error(f"❌ Failed to fetch fixtures: {e}")
        raise


@activity.defn
async def fetch_fixtures_by_ids(fixture_ids: List[int]) -> List[Dict[str, Any]]:
    """
    Fetch specific fixtures by their IDs.
    Used for manual ingest of specific fixtures from Temporal UI.
    
    Args:
        fixture_ids: List of fixture IDs to fetch
        
    Returns:
        List of fixture objects from API-Football
    """
    if not fixture_ids:
        activity.logger.warning("⚠️ No fixture IDs provided")
        return []
    
    activity.logger.info(f"🌐 Fetching {len(fixture_ids)} specific fixtures: {fixture_ids}")
    
    try:
        from src.api.api_client import fixtures_batch
        
        # Fetch fixtures by IDs (API supports batch fetch)
        fixtures = fixtures_batch(fixture_ids)
        activity.logger.info(f"✅ Retrieved {len(fixtures)} fixtures by ID")
        
        # Log which fixtures were found
        for fixture in fixtures:
            fixture_id = fixture.get("fixture", {}).get("id")
            home = fixture.get("teams", {}).get("home", {}).get("name", "?")
            away = fixture.get("teams", {}).get("away", {}).get("name", "?")
            status = fixture.get("fixture", {}).get("status", {}).get("short", "?")
            activity.logger.info(f"  📋 {fixture_id}: {home} vs {away} ({status})")
        
        # Warn about any IDs that weren't found
        found_ids = {f.get("fixture", {}).get("id") for f in fixtures}
        missing_ids = set(fixture_ids) - found_ids
        if missing_ids:
            activity.logger.warning(f"⚠️ Could not find fixtures: {missing_ids}")
        
        return fixtures
    
    except Exception as e:
        activity.logger.error(f"❌ Failed to fetch fixtures by ID: {e}")
        raise


@activity.defn
async def categorize_and_store_fixtures(fixtures: List[Dict]) -> Dict[str, int]:
    """
    Categorize fixtures by status and store in appropriate collections.
    
    Routes to:
    - staging: TBD, NS (not started)
    - active: LIVE, 1H, HT, 2H, ET, P, BT, SUSP, INT, PST (in progress or delayed)
    - completed: FT, AET, PEN, CANC, ABD, AWD, WO (finished)
    
    NOTE: PST (Postponed) is treated as ACTIVE to handle short delays (15-30 min).
    """
    if not fixtures:
        activity.logger.warning("⚠️  No fixtures to categorize")
        return {"staging": 0, "active": 0, "completed": 0}
    
    try:
        from src.utils.fixture_status import (
            get_completed_statuses,
            get_active_statuses,
            get_staging_statuses,
        )
        from src.data.mongo_store import FootyMongoStore
        
        # Get status sets
        completed_statuses = set(get_completed_statuses())
        active_statuses = set(get_active_statuses())
        staging_statuses = set(get_staging_statuses())
        
        staging_fixtures = []
        active_fixtures = []
        completed_fixtures = []
        
        for fixture in fixtures:
            status = fixture.get("fixture", {}).get("status", {}).get("short", "")
            fixture_id = fixture.get("fixture", {}).get("id", "unknown")
            
            if not status:
                activity.logger.warning(f"⚠️  Fixture {fixture_id} has no status, defaulting to staging")
                staging_fixtures.append(fixture)
                continue
            
            # Route based on status
            if status in completed_statuses:
                completed_fixtures.append(fixture)
            elif status in active_statuses:
                active_fixtures.append(fixture)
            elif status in staging_statuses:
                staging_fixtures.append(fixture)
            else:
                activity.logger.warning(f"⚠️  Unknown status '{status}' for fixture {fixture_id}, defaulting to staging")
                staging_fixtures.append(fixture)
        
        activity.logger.info(
            f"📊 Categorized {len(fixtures)} fixtures: "
            f"{len(staging_fixtures)} staging, "
            f"{len(active_fixtures)} active, "
            f"{len(completed_fixtures)} completed"
        )
        
        if active_fixtures:
            activity.logger.info(f"🔥 {len(active_fixtures)} fixtures already LIVE - will catch goals immediately!")
        
        if completed_fixtures:
            activity.logger.info(f"🏁 {len(completed_fixtures)} fixtures already FINISHED - skip monitoring")
        
        # Store in collections
        store = FootyMongoStore()
        staging_count = store.bulk_insert_fixtures(staging_fixtures, "fixtures_staging") if staging_fixtures else 0
        active_count = store.bulk_insert_fixtures(active_fixtures, "fixtures_active") if active_fixtures else 0
        completed_count = store.bulk_insert_fixtures(completed_fixtures, "fixtures_completed") if completed_fixtures else 0
        
        activity.logger.info(f"✅ Stored fixtures: {staging_count} staging, {active_count} active, {completed_count} completed")
        
        return {
            "staging": staging_count,
            "active": active_count,
            "completed": completed_count,
        }
    
    except Exception as e:
        activity.logger.error(f"❌ Failed to categorize/store fixtures: {e}")
        raise
