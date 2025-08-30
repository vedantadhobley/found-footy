from prefect import flow, task, get_run_logger
import pendulum
import time
from found_footy.api.mongo_api import get_fixture_details_batch, get_fixture_events_batch
from found_footy.flows.twitter_flow import twitter_flow
from found_footy.storage.mongo_store import FootyMongoStore

store = FootyMongoStore()

@task(name="activate-ready-fixtures-monitor")
def activate_ready_fixtures_monitor():
    """Check and activate fixtures ready for monitoring"""
    logger = get_run_logger()
    
    ready_fixtures = store.get_fixtures_ready_for_monitoring(minutes_before_kickoff=5)
    
    if not ready_fixtures:
        return 0
    
    logger.info(f"🎯 Activating {len(ready_fixtures)} fixtures for monitoring:")
    
    activated_count = 0
    for fixture in ready_fixtures:
        fixture_id = fixture["fixture_id"]
        home_team = fixture["home_team"]
        away_team = fixture["away_team"]
        
        logger.info(f"  ⚽ Activating {fixture_id}: {home_team} vs {away_team}")
        
        success = store.activate_fixture_monitoring(fixture_id)
        if success:
            activated_count += 1
    
    return activated_count

@task(name="batch-monitor-active-fixtures")
def batch_monitor_active_fixtures():
    """Make batch API call for all active fixtures and detect changes"""
    logger = get_run_logger()
    
    # Get all active fixtures
    active_fixtures = store.get_active_live_fixtures()
    
    if not active_fixtures:
        logger.info("⏸️ No active fixtures to monitor")
        return {"active_fixtures": 0, "goals_detected": 0}
    
    fixture_ids = [fixture["fixture_id"] for fixture in active_fixtures]
    logger.info(f"🔍 Batch monitoring {len(fixture_ids)} active fixtures: {fixture_ids}")
    
    try:
        # ✅ Make single batch API call
        logger.info("📡 Making batch API calls...")
        all_fixture_details = get_fixture_details_batch(fixture_ids)
        all_fixture_events = get_fixture_events_batch(fixture_ids)
        
        logger.info(f"✅ Got {len(all_fixture_details)} fixture details")
        logger.info(f"✅ Got events for {len(all_fixture_events)} fixtures")
        
        # Create lookup for easy access
        details_lookup = {fixture["fixture"]["id"]: fixture for fixture in all_fixture_details}
        
        # Process each active fixture
        total_goals_detected = 0
        completed_fixtures = []
        
        for active_fixture in active_fixtures:
            fixture_id = active_fixture["fixture_id"]
            home_team = active_fixture["home_team"]
            away_team = active_fixture["away_team"]
            
            # Get API data for this fixture
            fixture_api_data = details_lookup.get(fixture_id, {})
            events_data = all_fixture_events.get(fixture_id, [])
            
            if not fixture_api_data:
                logger.warning(f"⚠️ No API data for fixture {fixture_id}")
                continue
            
            # ✅ Update live fixture and detect changes
            changes = store.update_live_fixture_data(fixture_id, fixture_api_data, events_data)
            
            if changes.get("goals_changed", False):
                logger.info(f"🚨 GOALS DETECTED for {fixture_id}: {home_team} vs {away_team}")
                
                # ✅ Trigger Twitter flows for each new goal
                new_home_goals = changes.get("new_home_goals", 0)
                new_away_goals = changes.get("new_away_goals", 0)
                
                current_home_goals = fixture_api_data.get("goals", {}).get("home", 0) or 0
                current_away_goals = fixture_api_data.get("goals", {}).get("away", 0) or 0
                
                goal_count = 0
                
                # Home team goals
                for goal_num in range(new_home_goals):
                    goal_count += 1
                    total_goals_detected += 1
                    
                    goal_info = {
                        "fixture_id": fixture_id,
                        "goal_team": home_team,
                        "goal_number": current_home_goals - new_home_goals + goal_num + 1,
                        "score": f"{current_home_goals}-{current_away_goals}",
                        "home_team": home_team,
                        "away_team": away_team
                    }
                    
                    logger.info(f"🐦 Triggering Twitter for {home_team} goal #{goal_info['goal_number']}")
                    try:
                        twitter_flow.submit(goal_info=goal_info)
                        logger.info(f"✅ Twitter flow triggered")
                    except Exception as e:
                        logger.warning(f"⚠️ Failed to trigger Twitter: {e}")
                
                # Away team goals
                for goal_num in range(new_away_goals):
                    goal_count += 1
                    total_goals_detected += 1
                    
                    goal_info = {
                        "fixture_id": fixture_id,
                        "goal_team": away_team,
                        "goal_number": current_away_goals - new_away_goals + goal_num + 1,
                        "score": f"{current_home_goals}-{current_away_goals}",
                        "home_team": home_team,
                        "away_team": away_team
                    }
                    
                    logger.info(f"🐦 Triggering Twitter for {away_team} goal #{goal_info['goal_number']}")
                    try:
                        twitter_flow.submit(goal_info=goal_info)
                        logger.info(f"✅ Twitter flow triggered")
                    except Exception as e:
                        logger.warning(f"⚠️ Failed to trigger Twitter: {e}")
                
                logger.info(f"🎯 Triggered {goal_count} Twitter flows for fixture {fixture_id}")
            
            # Track completed fixtures
            if changes.get("fixture_completed", False):
                completed_fixtures.append(fixture_id)
                logger.info(f"🏁 Fixture {fixture_id} completed")
        
        logger.info(f"✅ Batch monitoring complete - {total_goals_detected} goals detected")
        if completed_fixtures:
            logger.info(f"🏁 {len(completed_fixtures)} fixtures completed: {completed_fixtures}")
        
        return {
            "active_fixtures": len(active_fixtures),
            "goals_detected": total_goals_detected,
            "completed_fixtures": len(completed_fixtures),
            "completed_fixture_ids": completed_fixtures
        }
        
    except Exception as e:
        logger.error(f"❌ Batch monitoring failed: {e}")
        import traceback
        traceback.print_exc()
        return {"active_fixtures": len(active_fixtures), "goals_detected": 0, "error": str(e)}

@task(name="cleanup-completed-fixtures")
def cleanup_completed_fixtures():
    """Remove completed fixtures from live monitoring"""
    logger = get_run_logger()
    
    removed_count = store.remove_completed_fixtures_from_monitoring()
    
    if removed_count > 0:
        logger.info(f"🧹 Cleaned up {removed_count} completed fixtures")
    
    return removed_count

@flow(name="fixtures-flow-monitor")
def fixtures_flow_monitor():
    """✅ NEW: Continuous monitoring of live fixtures"""
    logger = get_run_logger()
    
    logger.info("🔄 Starting continuous fixtures monitor...")
    logger.info("📡 Will monitor live fixtures and detect goals every minute")
    
    total_goals_detected = 0
    total_monitoring_cycles = 0
    max_cycles = 1440  # 24 hours worth of minutes
    
    try:
        while total_monitoring_cycles < max_cycles:
            cycle_start = pendulum.now()
            total_monitoring_cycles += 1
            
            logger.info(f"🔄 Monitoring cycle #{total_monitoring_cycles} - {cycle_start.format('HH:mm:ss')}")
            
            # ✅ Step 1: Activate any fixtures ready for monitoring
            activated_count = activate_ready_fixtures_monitor()
            if activated_count > 0:
                logger.info(f"🎯 Activated {activated_count} new fixtures for monitoring")
            
            # ✅ Step 2: Monitor all active fixtures
            monitor_result = batch_monitor_active_fixtures()
            goals_this_cycle = monitor_result.get("goals_detected", 0)
            active_fixtures_count = monitor_result.get("active_fixtures", 0)
            
            total_goals_detected += goals_this_cycle
            
            if goals_this_cycle > 0:
                logger.info(f"⚽ GOALS THIS CYCLE: {goals_this_cycle} (total: {total_goals_detected})")
            
            # ✅ Step 3: Cleanup completed fixtures
            cleanup_completed_fixtures()
            
            # ✅ Check if we should continue monitoring
            if active_fixtures_count == 0:
                # Check if there are any scheduled fixtures for today
                ready_fixtures = store.get_fixtures_ready_for_monitoring(minutes_before_kickoff=60)  # Check next hour
                
                if not ready_fixtures:
                    logger.info("✅ No active or upcoming fixtures - ending monitoring")
                    break
                else:
                    logger.info(f"⏳ No active fixtures, but {len(ready_fixtures)} fixtures upcoming")
            
            logger.info(f"📊 Cycle complete - Active: {active_fixtures_count}, Goals: {goals_this_cycle}")
            
            # ✅ Wait 1 minute before next cycle
            logger.info(f"⏱️ Waiting 60 seconds until next monitoring cycle...")
            time.sleep(60)
        
        logger.info(f"🏁 Monitoring completed after {total_monitoring_cycles} cycles")
        logger.info(f"⚽ Total goals detected: {total_goals_detected}")
        
        return {
            "monitoring_cycles": total_monitoring_cycles,
            "total_goals_detected": total_goals_detected,
            "completed_at": pendulum.now().isoformat(),
            "reason": "no_more_fixtures" if total_monitoring_cycles < max_cycles else "max_cycles_reached"
        }
        
    except Exception as e:
        logger.error(f"❌ Monitoring error: {e}")
        import traceback
        traceback.print_exc()
        raise