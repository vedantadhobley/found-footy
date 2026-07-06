"""
Ingest Workflow - Daily at 00:05 UTC

Fetches fixtures with smart lookahead to ensure frontend always has upcoming fixtures to display.

Standard behavior (tomorrow has fixtures):
  - Fetch today, tomorrow, day_after (3 days for timezone coverage)

Lookahead behavior (tomorrow is empty):
  - Fetch today + tomorrow (always needed for "today" timezone coverage)
  - Look ahead up to 30 days to find next day with fixtures
  - Fetch that day + day after (timezone coverage)

This handles international breaks and off-season gaps gracefully.

Pre-caches RAG aliases for both teams in each fixture (per-team for modularity).
Cleans up fixtures older than 14 days from MongoDB and S3.

Duplicate handling: The categorize_and_store_fixtures activity skips fixtures
that already exist in any collection (monitor workflow handles updates).
"""
from temporalio import workflow
from temporalio.common import RetryPolicy
from datetime import timedelta
from dataclasses import dataclass
from typing import Optional, List

with workflow.unsafe.imports_passed_through():
    from src.activities import ingest as ingest_activities
    from src.activities import monitor as monitor_activities
    from src.activities import rag as rag_activities
    from src.utils.footy_logging import log

MODULE = "ingest_workflow"


# Retention period for fixtures (in days)
# Ingestion runs at 00:05 UTC, so "day 1" is yesterday
# With 14 days retention: keeps 14 days before yesterday, deletes anything older
FIXTURE_RETENTION_DAYS = 14


@dataclass
class IngestWorkflowInput:
    """Optional input for IngestWorkflow - allows specifying target date or fixture IDs"""
    target_date: Optional[str] = None  # ISO format: "2025-12-26" (None = today+tomorrow)
    fixture_ids: Optional[List[int]] = None  # Specific fixture IDs to ingest (overrides date-based)


@workflow.defn
class IngestWorkflow:
    """Fetch today's and tomorrow's fixtures, pre-cache RAG aliases, and categorize by status"""
    
    @workflow.run
    async def run(self, input: Optional[IngestWorkflowInput] = None) -> dict:
        """
        Workflow:
        1. Fetch fixtures for today AND tomorrow (UTC) from API-Football
           - If fixture_ids provided: fetch only those specific fixtures
           - If target_date provided: fetch only that date (for manual/testing)
           - Otherwise: fetch today + tomorrow to handle timezone edge cases
        2. Skip fixtures that already exist (monitor workflow handles updates)
        3. Pre-cache RAG aliases for BOTH teams in each fixture (per-team calls)
        4. Route NEW fixtures to correct collection:
           - TBD/NS → fixtures_staging
           - LIVE/1H/HT/2H/ET/P/BT → fixtures_active  
           - FT/AET/PEN → fixtures_completed
        5. Cleanup old fixtures (older than 14 days)
        6. Notify frontend to refresh (for upcoming fixtures display)
        """
        # Check if specific fixture IDs were provided (manual ingest mode)
        if input and input.fixture_ids and len(input.fixture_ids) > 0:
            log.info(workflow.logger, MODULE, "manual_ingest_by_ids",
                     "Manual ingest mode: fetching specific fixtures",
                     count=len(input.fixture_ids), fixture_ids=input.fixture_ids)
            
            # Fetch specific fixtures by ID
            fixtures = await workflow.execute_activity(
                ingest_activities.fetch_fixtures_by_ids,
                input.fixture_ids,
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(
                    maximum_attempts=3,
                    initial_interval=timedelta(seconds=1),
                    maximum_interval=timedelta(seconds=10),
                ),
            )
        elif input and input.target_date:
            # Manual date-based ingest (for testing historical dates)
            # Only fetches the specified date, not tomorrow
            log.info(workflow.logger, MODULE, "manual_ingest_by_date",
                     "Manual ingest mode: fetching fixtures for date",
                     target_date=input.target_date)
            
            fixtures = await workflow.execute_activity(
                ingest_activities.fetch_todays_fixtures,
                input.target_date,
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(
                    maximum_attempts=3,
                    initial_interval=timedelta(seconds=1),
                    maximum_interval=timedelta(seconds=10),
                ),
            )
        else:
            # Standard daily ingest with lookahead for gaps in fixture schedule
            #
            # Logic:
            # 1. Always fetch TODAY (even if empty - needed for current day display)
            # 2. Always fetch TOMORROW (needed for "today" view across all timezones)
            # 3. If TOMORROW has fixtures → also fetch day_after (timezone coverage)
            # 4. If TOMORROW is empty → look ahead up to 30 days to find next day with fixtures
            #    → fetch that day + the day after (timezone coverage)
            #
            # This ensures frontend always has at least one upcoming day with fixtures,
            # even during international breaks or off-season gaps.
            
            # Use workflow.now() for determinism (not datetime.now())
            now_utc = workflow.now()
            today_utc = now_utc.date()
            tomorrow_utc = today_utc + timedelta(days=1)
            
            today_str = today_utc.isoformat()
            tomorrow_str = tomorrow_utc.isoformat()
            
            log.info(workflow.logger, MODULE, "daily_ingest_start",
                     "Starting daily fixture ingest", date=today_str)
            
            # Always fetch today
            today_fixtures = await workflow.execute_activity(
                ingest_activities.fetch_todays_fixtures,
                today_str,
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(
                    maximum_attempts=3,
                    initial_interval=timedelta(seconds=1),
                    maximum_interval=timedelta(seconds=10),
                ),
            )
            
            # Always fetch tomorrow (needed for "today" timezone coverage)
            tomorrow_fixtures = await workflow.execute_activity(
                ingest_activities.fetch_todays_fixtures,
                tomorrow_str,
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(
                    maximum_attempts=3,
                    initial_interval=timedelta(seconds=1),
                    maximum_interval=timedelta(seconds=10),
                ),
            )
            
            # Determine what additional days to fetch based on tomorrow's fixtures
            extra_fixtures = []
            
            if len(tomorrow_fixtures) > 0:
                # Tomorrow has fixtures - fetch day_after for timezone handling
                day_after_utc = today_utc + timedelta(days=2)
                day_after_str = day_after_utc.isoformat()
                
                log.info(workflow.logger, MODULE, "fetching_day_after",
                         "Tomorrow has fixtures, fetching day after",
                         tomorrow=tomorrow_str, tomorrow_count=len(tomorrow_fixtures),
                         day_after=day_after_str)
                
                day_after_fixtures = await workflow.execute_activity(
                    ingest_activities.fetch_todays_fixtures,
                    day_after_str,
                    start_to_close_timeout=timedelta(seconds=30),
                    retry_policy=RetryPolicy(
                        maximum_attempts=3,
                        initial_interval=timedelta(seconds=1),
                        maximum_interval=timedelta(seconds=10),
                    ),
                )
                extra_fixtures = day_after_fixtures
                
                log.info(workflow.logger, MODULE, "standard_fetch_complete",
                         "Standard 3-day fetch complete",
                         today_count=len(today_fixtures), tomorrow_count=len(tomorrow_fixtures),
                         day_after_count=len(day_after_fixtures))
            else:
                # Tomorrow is empty - look ahead up to 30 days to find next day with fixtures
                log.info(workflow.logger, MODULE, "looking_ahead",
                         "Tomorrow has no fixtures, looking ahead",
                         tomorrow=tomorrow_str)
                
                MAX_LOOKAHEAD_DAYS = 30
                next_match_day = None
                next_match_fixtures = []
                
                for days_ahead in range(2, MAX_LOOKAHEAD_DAYS + 2):  # Start at day+2 (we already checked tomorrow)
                    check_date = today_utc + timedelta(days=days_ahead)
                    check_date_str = check_date.isoformat()
                    
                    check_fixtures = await workflow.execute_activity(
                        ingest_activities.fetch_todays_fixtures,
                        check_date_str,
                        start_to_close_timeout=timedelta(seconds=30),
                        retry_policy=RetryPolicy(
                            maximum_attempts=3,
                            initial_interval=timedelta(seconds=1),
                            maximum_interval=timedelta(seconds=10),
                        ),
                    )
                    
                    if len(check_fixtures) > 0:
                        next_match_day = check_date
                        next_match_fixtures = check_fixtures
                        log.info(workflow.logger, MODULE, "found_fixtures_ahead",
                                 "Found fixtures",
                                 date=check_date_str, count=len(check_fixtures),
                                 days_ahead=days_ahead)
                        break
                
                if next_match_day:
                    # Found a day with fixtures - fetch the day after for timezone handling
                    next_match_day_str = next_match_day.isoformat()
                    next_match_day_after = next_match_day + timedelta(days=1)
                    next_match_day_after_str = next_match_day_after.isoformat()
                    
                    log.info(workflow.logger, MODULE, "fetching_day_after_next",
                             "Fetching day after next match day for timezone handling",
                             next_match_day_after=next_match_day_after_str)
                    
                    next_match_day_after_fixtures = await workflow.execute_activity(
                        ingest_activities.fetch_todays_fixtures,
                        next_match_day_after_str,
                        start_to_close_timeout=timedelta(seconds=30),
                        retry_policy=RetryPolicy(
                            maximum_attempts=3,
                            initial_interval=timedelta(seconds=1),
                            maximum_interval=timedelta(seconds=10),
                        ),
                    )
                    
                    extra_fixtures = next_match_fixtures + next_match_day_after_fixtures
                    
                    log.info(workflow.logger, MODULE, "lookahead_fetch_complete",
                             "Lookahead mode fetch complete",
                             today_count=len(today_fixtures), tomorrow_count=0,
                             next_match_day=next_match_day_str,
                             next_match_count=len(next_match_fixtures),
                             next_match_day_after=next_match_day_after_str,
                             next_match_day_after_count=len(next_match_day_after_fixtures))
                else:
                    log.warning(workflow.logger, MODULE, "no_fixtures_found",
                                "No fixtures found in lookahead - may be off-season",
                                max_lookahead_days=MAX_LOOKAHEAD_DAYS)
            
            # Combine and deduplicate by fixture ID
            seen_ids = set()
            fixtures = []
            for fixture in today_fixtures + tomorrow_fixtures + extra_fixtures:
                fixture_id = fixture.get("fixture", {}).get("id")
                if fixture_id and fixture_id not in seen_ids:
                    seen_ids.add(fixture_id)
                    fixtures.append(fixture)
            
            log.info(workflow.logger, MODULE, "total_unique_fixtures",
                     "Total unique fixtures to process", count=len(fixtures))
        
        # Pre-cache RAG aliases for EACH team individually
        # This is more modular - can retry per-team instead of per-fixture
        teams_processed = set()
        rag_success = 0
        rag_failed = 0
        
        for fixture in fixtures:
            teams = fixture.get("teams", {})
            league = fixture.get("league", {})
            country = league.get("country")  # e.g., "England", "Spain", "World" (for int'l)
            
            for side in ["home", "away"]:
                team = teams.get(side, {})
                team_id = team.get("id")
                team_name = team.get("name", "Unknown")
                
                # Skip if already processed this team (same team in multiple fixtures)
                if not team_id or team_id in teams_processed:
                    continue
                
                teams_processed.add(team_id)
                
                # Team type will be determined by the activity via API lookup
                # Pass country for better Wikidata search
                try:
                    await workflow.execute_activity(
                        rag_activities.get_team_aliases,
                        args=[team_id, team_name, None, country],  # None = auto-detect team type
                        start_to_close_timeout=timedelta(seconds=90),  # Single team RAG
                        retry_policy=RetryPolicy(
                            maximum_attempts=2,
                            initial_interval=timedelta(seconds=2),
                            maximum_interval=timedelta(seconds=10),
                        ),
                    )
                    rag_success += 1
                except Exception as e:
                    # Don't fail ingestion if RAG fails - aliases can be generated later
                    log.warning(workflow.logger, MODULE, "rag_precache_failed",
                                "RAG pre-cache failed",
                                team_name=team_name, team_id=team_id, error=str(e))
                    rag_failed += 1
        
        log.info(workflow.logger, MODULE, "rag_precache_complete",
                 "Pre-cached RAG aliases", success=rag_success, failed=rag_failed)
        
        # Categorize and store
        result = await workflow.execute_activity(
            ingest_activities.categorize_and_store_fixtures,
            fixtures,
            start_to_close_timeout=timedelta(seconds=30),
            retry_policy=RetryPolicy(
                maximum_attempts=3,
                initial_interval=timedelta(seconds=1),
                maximum_interval=timedelta(seconds=10),
            ),
        )
        
        # Cleanup old fixtures (older than 14 days)
        # This runs at the end of ingestion to keep the database clean
        cleanup_result = await workflow.execute_activity(
            ingest_activities.cleanup_old_fixtures,
            FIXTURE_RETENTION_DAYS,
            start_to_close_timeout=timedelta(seconds=120),  # May take time to delete many S3 objects
            retry_policy=RetryPolicy(
                maximum_attempts=2,
                initial_interval=timedelta(seconds=5),
            ),
        )
        
        # Notify frontend to refresh (shows upcoming fixtures)
        await workflow.execute_activity(
            monitor_activities.notify_frontend_refresh,
            start_to_close_timeout=timedelta(seconds=10),
            retry_policy=RetryPolicy(maximum_attempts=1),  # Don't retry - frontend may not be running
        )
        
        # Add counts for logging
        result["total_fixtures"] = len(fixtures)
        result["rag_success"] = rag_success
        result["rag_failed"] = rag_failed
        result["unique_teams"] = len(teams_processed)
        result["cleanup"] = cleanup_result
        
        log.info(workflow.logger, MODULE, "ingest_complete",
                 "Ingest complete",
                 staging=result.get('staging', 0), active=result.get('active', 0),
                 completed=result.get('completed', 0), skipped=result.get('skipped', 0),
                 rag_success=rag_success, unique_teams=len(teams_processed),
                 deleted_fixtures=cleanup_result.get('deleted_fixtures', 0))
        return result
