from datetime import datetime, timedelta
from prefect import flow, task, get_run_logger
from prefect.events import emit_event
import pendulum
import time
import asyncio
from concurrent.futures import ThreadPoolExecutor
from found_footy.api import (
    get_fixtures_by_date,
    get_fixture_details,
    get_fixture_events,
    populate_teams_table,
    store_fixture_result,
    store_fixture_events
)

# Convert API functions to async versions
async def async_get_fixture_details(fixture_id):
    """Async wrapper for getting fixture details"""
    loop = asyncio.get_event_loop()
    with ThreadPoolExecutor() as executor:
        return await loop.run_in_executor(executor, get_fixture_details, fixture_id)

async def async_get_fixture_events(fixture_id):
    """Async wrapper for getting fixture events"""
    loop = asyncio.get_event_loop()
    with ThreadPoolExecutor() as executor:
        return await loop.run_in_executor(executor, get_fixture_events, fixture_id)

async def async_store_fixture_result(fixture_data, table_name="fixtures_2526"):
    """Async wrapper for storing fixture results"""
    loop = asyncio.get_event_loop()
    with ThreadPoolExecutor() as executor:
        return await loop.run_in_executor(executor, store_fixture_result, fixture_data, table_name)

async def async_store_fixture_events(fixture_id, events_data, table_name="events_2526"):
    """Async wrapper for storing fixture events"""
    loop = asyncio.get_event_loop()
    with ThreadPoolExecutor() as executor:
        return await loop.run_in_executor(executor, store_fixture_events, fixture_id, events_data, table_name)

@task(name="fetch-fixtures")
def fetch_fixtures(date_str=None):
    """Fetch fixtures for a given date and return them"""
    logger = get_run_logger()
    
    if date_str:
        query_date = datetime.strptime(date_str, "%Y%m%d").date()
    else:
        from datetime import timezone
        query_date = datetime.now(timezone.utc).date()
    
    logger.info(f"⚽ Fetching fixtures for {query_date}")
    fixtures = get_fixtures_by_date(query_date=query_date)
    
    if fixtures:
        logger.info(f"✅ Found {len(fixtures)} fixtures:")
        for fixture in fixtures:
            logger.info(f"  📅 {fixture['id']}: {fixture['home']} vs {fixture['away']} at {fixture['time']} ({fixture['league']})")
    else:
        logger.warning(f"❌ No fixtures found for {query_date}")
    
    return fixtures

async def check_fixture_status_async(fixture_id):
    """Async version of fixture status checking"""
    detailed_data = await async_get_fixture_details(fixture_id)
    
    if not detailed_data:
        return None, "No data found", "No data"
    
    fixture = detailed_data[0]
    status = fixture.get("fixture", {}).get("status", {}).get("short")
    home_team = fixture.get("teams", {}).get("home", {}).get("name", "Unknown")
    away_team = fixture.get("teams", {}).get("away", {}).get("name", "Unknown")
    home_goals = fixture.get("goals", {}).get("home")
    away_goals = fixture.get("goals", {}).get("away")
    
    finished_statuses = {"FT", "AET", "PEN"}
    dropped_statuses = {"CANC", "ABD", "AWD", "WO", "PST"}
    
    if status in finished_statuses:
        return "COMPLETED", status, f"{home_team} {home_goals} - {away_goals} {away_team}", fixture
    elif status in dropped_statuses:
        return "DROPPED", status, f"{home_team} vs {away_team}", fixture
    else:
        score_info = ""
        if home_goals is not None and away_goals is not None:
            score_info = f" ({home_team} {home_goals} - {away_goals} {away_team})"
        return "IN_PROGRESS", status, f"{home_team} vs {away_team}{score_info}", fixture

@task(name="monitor-fixture", retries=100, retry_delay_seconds=300)
async def monitor_fixture_async(fixture_id, fixture_start_time):
    """Async version of fixture monitoring"""
    logger = get_run_logger()
    
    # Calculate when to start monitoring (2 hours AFTER kick-off)
    start_monitoring_time = pendulum.parse(fixture_start_time).add(hours=2)
    current_time = pendulum.now("UTC")
    
    # If it's not time to start monitoring yet, wait
    if current_time < start_monitoring_time:
        wait_seconds = (start_monitoring_time - current_time).total_seconds()
        logger.info(f"⏰ Waiting {wait_seconds/60:.1f} minutes until 2 hours after kick-off for fixture {fixture_id}")
        await asyncio.sleep(wait_seconds)
    
    logger.info(f"🔍 Starting to monitor fixture {fixture_id} (2+ hours after kick-off)")
    
    # Check fixture status using async helper function
    result, status, match_info, fixture_data = await check_fixture_status_async(fixture_id)
    
    if result == "COMPLETED":
        logger.info(f"🎉 Fixture {fixture_id} completed: {match_info} ({status})")
        return {
            "fixture_id": fixture_id, 
            "status": status, 
            "result": "COMPLETED", 
            "match_info": match_info,
            "fixture_data": fixture_data
        }
    elif result == "DROPPED":
        logger.warning(f"🚫 Fixture {fixture_id} was dropped: {match_info} ({status})")
        return {
            "fixture_id": fixture_id, 
            "status": status, 
            "result": "DROPPED", 
            "match_info": match_info,
            "fixture_data": fixture_data
        }
    else:
        logger.info(f"⏳ Fixture {fixture_id} still in progress: {match_info} ({status}), will retry in 5 minutes...")
        raise Exception(f"Fixture {fixture_id} not yet completed (status: {status})")

@task(name="process-completed-fixture-async")
async def process_completed_fixture_async(fixture_id, status, match_info, fixture_data):
    """Async version of completed fixture processing with parallel operations"""
    logger = get_run_logger()
    
    logger.info(f"🎉 Processing completed fixture {fixture_id}: {match_info}")
    
    # Run multiple operations in parallel using asyncio.gather
    async def store_fixture_data_task():
        """Store fixture result"""
        try:
            success = await async_store_fixture_result(fixture_data, table_name="fixtures_2526")
            if success:
                logger.info(f"💾 Fixture {fixture_id} result stored in fixtures_2526 table")
                
                # Emit event for YouTube processing
                emit_event(
                    event="fixture.completed",
                    resource={"prefect.resource.id": f"fixture.{fixture_id}"},
                    payload={
                        "fixture_id": fixture_id,
                        "home_team": fixture_data.get("teams", {}).get("home", {}).get("name"),
                        "away_team": fixture_data.get("teams", {}).get("away", {}).get("name"),
                        "status": fixture_data.get("fixture", {}).get("status", {}).get("short")
                    }
                )
                logger.info(f"📡 Emitted completion event for fixture {fixture_id}")
            return success
        except Exception as e:
            logger.error(f"❌ Error storing fixture {fixture_id} result: {e}")
            return False
    
    async def store_events_data_task():
        """Fetch and store events"""
        try:
            logger.info(f"📊 Fetching events for fixture {fixture_id}")
            events_data = await async_get_fixture_events(fixture_id)
            
            if not events_data:
                logger.warning(f"⚠️ No events found for fixture {fixture_id}")
                return {"total_events": 0, "goal_events_stored": 0, "status": "no_events"}
            
            goal_events = [event for event in events_data if event.get("type") == "Goal"]
            logger.info(f"⚽ Found {len(goal_events)} goal events out of {len(events_data)} total events")
            
            goal_events_stored = await async_store_fixture_events(fixture_id, events_data, table_name="events_2526")
            
            return {
                "total_events": len(events_data),
                "goal_events_found": len(goal_events),
                "goal_events_stored": goal_events_stored,
                "status": "completed"
            }
        except Exception as e:
            logger.error(f"❌ Error fetching/storing events for fixture {fixture_id}: {e}")
            return {"total_events": 0, "goal_events_stored": 0, "status": "failed", "error": str(e)}
    
    async def trigger_youtube_processing():
        """Trigger YouTube flow asynchronously"""
        try:
            logger.info(f"🎬 Triggering YouTube processing for fixture {fixture_id}")
            # Import YouTube flow and trigger it
            from found_footy.flows.youtube_flow import youtube_flow
            
            # Run YouTube flow in parallel (this will be handled by Prefect's event system)
            logger.info(f"✅ YouTube flow trigger ready for fixture {fixture_id}")
            return {"youtube_triggered": True, "fixture_id": fixture_id}
        except Exception as e:
            logger.error(f"❌ Error triggering YouTube flow for fixture {fixture_id}: {e}")
            return {"youtube_triggered": False, "error": str(e)}
    
    # Run all tasks in parallel
    logger.info(f"🚀 Running parallel processing for fixture {fixture_id}")
    fixture_stored, events_result, youtube_result = await asyncio.gather(
        store_fixture_data_task(),
        store_events_data_task(),
        trigger_youtube_processing(),
        return_exceptions=True
    )
    
    logger.info(f"✅ Parallel processing completed for fixture {fixture_id}")
    
    return {
        "fixture_id": fixture_id,
        "final_status": status,
        "match_info": match_info,
        "fixture_stored": fixture_stored,
        "events_result": events_result,
        "youtube_result": youtube_result,
        "processing_type": "parallel_async"
    }

@task(name="submit-fixture-monitoring-async")
async def submit_fixture_monitoring_async(fixtures_batch):
    """Process multiple fixtures in parallel using asyncio"""
    logger = get_run_logger()
    
    logger.info(f"🚀 Processing {len(fixtures_batch)} fixtures in parallel")
    
    async def process_single_fixture(fixture):
        """Process a single fixture completely"""
        fixture_id = fixture["id"]
        fixture_time = fixture["time"]
        home_team = fixture["home"]
        away_team = fixture["away"]
        
        logger.info(f"📡 Starting monitoring for fixture {fixture_id}: {home_team} vs {away_team}")
        
        try:
            # Monitor fixture until completion
            monitor_result = await monitor_fixture_async(fixture_id, fixture_time)
            
            if monitor_result["result"] == "COMPLETED":
                logger.info(f"✅ Fixture {fixture_id} completed - starting parallel processing")
                
                # Process completed fixture with parallel operations
                process_result = await process_completed_fixture_async(
                    monitor_result["fixture_id"],
                    monitor_result["status"], 
                    monitor_result["match_info"],
                    monitor_result["fixture_data"]
                )
                
                return {
                    "fixture_id": fixture_id,
                    "monitor_result": monitor_result,
                    "process_result": process_result,
                    "status": "completed_and_processed"
                }
            else:
                logger.info(f"⚠️ Fixture {fixture_id} was {monitor_result['result']}")
                return {
                    "fixture_id": fixture_id,
                    "monitor_result": monitor_result,
                    "process_result": None,
                    "status": f"skipped_{monitor_result['result'].lower()}"
                }
                
        except Exception as e:
            logger.error(f"❌ Error in fixture {fixture_id} pipeline: {e}")
            return {
                "fixture_id": fixture_id,
                "monitor_result": None,
                "process_result": None,
                "status": "failed",
                "error": str(e)
            }
    
    # Process all fixtures in parallel
    results = await asyncio.gather(
        *[process_single_fixture(fixture) for fixture in fixtures_batch],
        return_exceptions=True
    )
    
    return results

@flow(name="fixtures-flow")
def fixtures_flow(date_str: str = None):
    """
    Main flow that fetches fixtures and processes them with TRUE PARALLELIZATION
    """
    logger = get_run_logger()
    logger.info("🚀 Starting Found Footy Fixtures Flow with ASYNC PARALLEL processing")
    
    # Ensure the teams table is populated
    populate_teams_table(2025)
    
    # Determine the target date for this run
    if date_str:
        target_date = date_str
        logger.info(f"📅 Running for specified date: {target_date}")
    else:
        from datetime import timezone
        today = datetime.now(timezone.utc).date()
        target_date = today.strftime("%Y%m%d")
        logger.info(f"📅 Running for today's date: {target_date}")
    
    # Fetch fixtures for the target date
    fixtures = fetch_fixtures(target_date)
    
    if not fixtures:
        logger.warning("❌ No fixtures found for the specified date")
        return {
            "fixtures_monitored": 0,
            "target_date": target_date,
            "results": []
        }
    
    logger.info(f"🎯 Starting PARALLEL processing for {len(fixtures)} fixtures")
    
    # Process all fixtures in parallel using async
    results = asyncio.run(submit_fixture_monitoring_async(fixtures))
    
    # Collect statistics
    successful_results = [r for r in results if isinstance(r, dict) and r.get("status") == "completed_and_processed"]
    failed_results = [r for r in results if isinstance(r, Exception) or (isinstance(r, dict) and "error" in r)]
    
    # Final summary
    logger.info("=" * 50)
    logger.info(f"⚽ ASYNC PARALLEL PROCESSING COMPLETE - {target_date}")
    logger.info("=" * 50)
    logger.info(f"📅 Target Date: {target_date}")
    logger.info(f"🔢 Total Fixtures: {len(fixtures)}")
    logger.info(f"✅ Successful Completions: {len(successful_results)}")
    logger.info(f"❌ Failed/Errors: {len(failed_results)}")
    logger.info("=" * 50)
    
    return {
        "fixtures_monitored": len(fixtures),
        "results_completed": len(results),
        "successful_completions": len(successful_results),
        "failed_completions": len(failed_results),
        "target_date": target_date,
        "results": results,
        "processing_type": "async_parallel"
    }