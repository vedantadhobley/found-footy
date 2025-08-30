from datetime import datetime, timedelta
from prefect import flow, task, get_run_logger
import pendulum
from typing import Optional, List
from found_footy.api.mongo_api import (
    get_fixtures_by_date,
    populate_team_metadata,
    parse_team_ids_parameter,
    get_available_teams
)

from found_footy.flows.fixtures_flow_monitor import fixtures_flow_monitor
from found_footy.storage.mongo_store import FootyMongoStore

# Create store instance for direct access
store = FootyMongoStore()

@task(name="fetch-and-schedule-fixtures")
def fetch_and_schedule_fixtures(team_ids=None, date_str=None):
    """Fetch fixtures and add them to live monitoring schedule"""
    logger = get_run_logger()
    
    if date_str:
        query_date = datetime.strptime(date_str, "%Y%m%d").date()
    else:
        from datetime import timezone
        query_date = datetime.now(timezone.utc).date()
    
    if team_ids and team_ids.strip():
        if isinstance(team_ids, str):
            team_ids = parse_team_ids_parameter(team_ids)
        
        logger.info(f"⚽ Fetching fixtures for specific teams {team_ids} on {query_date}")
        
        available_teams = get_available_teams()
        
        valid_team_ids = []
        for team_id in team_ids:
            if team_id in available_teams:
                valid_team_ids.append(team_id)
                logger.info(f"  ⚽ {team_id}: {available_teams[team_id]['name']} ({available_teams[team_id]['country']})")
            else:
                logger.warning(f"  ⚠️ Unknown team ID: {team_id}")
        
        if not valid_team_ids:
            logger.error("❌ No valid team IDs provided")
            return []
        
        from found_footy.api.mongo_api import get_fixtures_by_teams
        fixtures = get_fixtures_by_teams(valid_team_ids, query_date)
    else:
        logger.info(f"⚽ Fetching fixtures for ALL top 25 UEFA teams on {query_date}")
        fixtures = get_fixtures_by_date(query_date)
    
    if not fixtures:
        logger.warning(f"❌ No fixtures found for top 25 teams on {query_date}")
        return []
    
    logger.info(f"✅ Found {len(fixtures)} fixtures:")
    
    # ✅ NEW: Add each fixture to live monitoring schedule
    scheduled_count = 0
    for fixture in fixtures:
        logger.info(f"  📅 {fixture['id']}: {fixture['home']} vs {fixture['away']} at {fixture['time']} ({fixture['league']})")
        
        # Add to live monitoring
        success = store.add_fixture_to_live_monitoring(fixture)
        if success:
            scheduled_count += 1
    
    logger.info(f"📋 Scheduled {scheduled_count} fixtures for live monitoring")
    return fixtures

@task(name="activate-ready-fixtures")
def activate_ready_fixtures():
    """Check for fixtures that should start monitoring and activate them"""
    logger = get_run_logger()
    
    # Get fixtures ready for monitoring (5 minutes before kickoff)
    ready_fixtures = store.get_fixtures_ready_for_monitoring(minutes_before_kickoff=5)
    
    if not ready_fixtures:
        logger.info("⏸️ No fixtures ready for monitoring activation")
        return 0
    
    logger.info(f"🎯 Found {len(ready_fixtures)} fixtures ready for monitoring:")
    
    activated_count = 0
    for fixture in ready_fixtures:
        fixture_id = fixture["fixture_id"]
        kickoff_time = fixture["kickoff_time"]
        home_team = fixture["home_team"]
        away_team = fixture["away_team"]
        
        logger.info(f"  ⚽ Activating {fixture_id}: {home_team} vs {away_team} (kickoff: {kickoff_time})")
        
        success = store.activate_fixture_monitoring(fixture_id)
        if success:
            activated_count += 1
    
    logger.info(f"✅ Activated monitoring for {activated_count} fixtures")
    return activated_count

@flow(name="fixtures-flow")
def fixtures_flow(date_str: Optional[str] = None, team_ids: str = ""):
    """✅ NEW APPROACH: Setup fixtures and launch continuous monitor"""
    logger = get_run_logger()
    logger.info("🚀 Starting NEW Found Footy Fixtures Flow (Setup + Monitor)")
    
    # Parse team IDs (if provided and not empty)
    parsed_team_ids = None
    if team_ids and team_ids.strip():
        try:
            parsed_team_ids = parse_team_ids_parameter(team_ids)
            logger.info(f"⚽ Target teams: {parsed_team_ids}")
        except Exception as e:
            logger.error(f"❌ Error parsing team_ids parameter: {e}")
            logger.info("🔄 Using all top 25 teams instead")
    else:
        logger.info("⚽ Using all top 25 UEFA teams")
    
    # ✅ Initialize team metadata WITHOUT database reset
    logger.info("⚽ Initializing team metadata (preserving existing data)...")
    populate_team_metadata(reset_first=False)
    logger.info("✅ Team metadata initialization complete")
    
    # Determine the target date for this run
    if date_str:
        target_date = date_str
        logger.info(f"📅 Running for specified date: {target_date}")
    else:
        from datetime import timezone
        today = datetime.now(timezone.utc).date()
        target_date = today.strftime("%Y%m%d")
        logger.info(f"📅 Running for today's date: {target_date}")
    
    # ✅ NEW: Fetch fixtures and schedule them for monitoring
    logger.info("📋 Step 1: Fetching and scheduling fixtures...")
    fixtures = fetch_and_schedule_fixtures(parsed_team_ids, target_date)
    
    if not fixtures:
        logger.warning("❌ No fixtures found for the specified date and teams")
        return {
            "fixtures_found": 0,
            "target_date": target_date,
            "team_ids": parsed_team_ids or "all_top_25",
            "monitor_launched": False
        }
    
    logger.info(f"✅ Scheduled {len(fixtures)} fixtures for monitoring")
    
    # ✅ NEW: Activate any fixtures that are ready now
    logger.info("🎯 Step 2: Activating fixtures ready for monitoring...")
    activated_count = activate_ready_fixtures()
    
    # ✅ NEW: Launch continuous monitoring subflow
    logger.info("🔄 Step 3: Launching continuous fixtures monitor...")
    logger.info("📡 Monitor will:")
    logger.info("  - Activate fixtures 5 minutes before kickoff")
    logger.info("  - Make batch API calls every minute for active fixtures")
    logger.info("  - Detect goal changes and trigger Twitter flows")
    logger.info("  - Complete when all fixtures are finished")
    
    # Launch the monitor as a subflow (this will run continuously)
    monitor_result = fixtures_flow_monitor()
    
    logger.info("🏁 Fixtures flow completed!")
    
    return {
        "fixtures_found": len(fixtures),
        "fixtures_activated_immediately": activated_count,
        "target_date": target_date,
        "team_ids": parsed_team_ids or "all_top_25",
        "monitor_launched": True,
        "monitor_result": monitor_result,
        "flow_completed_at": pendulum.now().isoformat()
    }