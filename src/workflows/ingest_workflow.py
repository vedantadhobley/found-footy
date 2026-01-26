"""
Ingest Workflow - Daily at 00:05 UTC

Fetches fixtures for today, tomorrow, and day after (UTC) and routes them to correct collections.
3 days are fetched to handle timezone edge cases and allow frontend to show "tomorrow" fixtures
for users in any timezone.

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
            workflow.logger.info(f"📥 Manual ingest mode: fetching {len(input.fixture_ids)} specific fixtures: {input.fixture_ids}")
            
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
            workflow.logger.info(f"📥 Manual ingest mode: fetching fixtures for {input.target_date}")
            
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
            # Standard daily ingest: fetch today, tomorrow, and day after (UTC)
            # This allows frontend to show "tomorrow" fixtures for users in any timezone
            # Day+2 handles edge case where user's "tomorrow" is actually 2 days ahead in UTC
            
            # Use workflow.now() for determinism (not datetime.now())
            now_utc = workflow.now()
            today_utc = now_utc.date()
            tomorrow_utc = today_utc + timedelta(days=1)
            day_after_utc = today_utc + timedelta(days=2)
            
            today_str = today_utc.isoformat()
            tomorrow_str = tomorrow_utc.isoformat()
            day_after_str = day_after_utc.isoformat()
            
            workflow.logger.info(f"📥 Starting daily fixture ingest for {today_str}, {tomorrow_str}, and {day_after_str}")
            
            # Fetch fixtures for all 3 days in sequence
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
            
            # Combine and deduplicate by fixture ID
            seen_ids = set()
            fixtures = []
            for fixture in today_fixtures + tomorrow_fixtures + day_after_fixtures:
                fixture_id = fixture.get("fixture", {}).get("id")
                if fixture_id and fixture_id not in seen_ids:
                    seen_ids.add(fixture_id)
                    fixtures.append(fixture)
            
            workflow.logger.info(
                f"📊 Fetched {len(today_fixtures)} today + {len(tomorrow_fixtures)} tomorrow + "
                f"{len(day_after_fixtures)} day after = {len(fixtures)} unique fixtures"
            )
        
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
                    workflow.logger.warning(f"⚠️ RAG pre-cache failed for {team_name} ({team_id}): {e}")
                    rag_failed += 1
        
        workflow.logger.info(f"🤖 Pre-cached RAG aliases: {rag_success} teams OK, {rag_failed} failed")
        
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
        
        workflow.logger.info(
            f"✅ Ingest complete: {result.get('staging', 0)} staging, "
            f"{result.get('active', 0)} active, {result.get('completed', 0)} completed, "
            f"{result.get('skipped', 0)} skipped (already exist), "
            f"{rag_success}/{len(teams_processed)} teams RAG cached, "
            f"{cleanup_result.get('deleted_fixtures', 0)} old fixtures cleaned up"
        )
        return result
