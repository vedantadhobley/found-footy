"""
Ingest Workflow - Daily at 00:05 UTC

Fetches today's fixtures and routes them to correct collections based on status.
Pre-caches RAG aliases for both teams in each fixture (per-team for modularity).
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


@dataclass
class IngestWorkflowInput:
    """Optional input for IngestWorkflow - allows specifying target date or fixture IDs"""
    target_date: Optional[str] = None  # ISO format: "2025-12-26" (None = today)
    fixture_ids: Optional[List[int]] = None  # Specific fixture IDs to ingest (overrides date-based)


@workflow.defn
class IngestWorkflow:
    """Fetch today's fixtures, pre-cache RAG aliases, and categorize by status"""
    
    @workflow.run
    async def run(self, input: Optional[IngestWorkflowInput] = None) -> dict:
        """
        Workflow:
        1. Fetch fixtures for today from API-Football (or specific IDs if provided)
        2. Pre-cache RAG aliases for BOTH teams in each fixture (per-team calls)
        3. Route to correct collection:
           - TBD/NS → fixtures_staging
           - LIVE/1H/HT/2H/ET/P/BT → fixtures_active  
           - FT/AET/PEN → fixtures_completed
        4. Notify frontend to refresh (for upcoming fixtures display)
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
        else:
            # Standard date-based ingest
            # Parse target date if provided (for testing historical dates)
            # Pass as ISO string to activity (date objects aren't JSON serializable)
            target_date_str = None
            if input and input.target_date:
                target_date_str = input.target_date  # Already ISO format
                workflow.logger.info(f"📥 Starting fixture ingest for {input.target_date}")
            else:
                workflow.logger.info("📥 Starting daily fixture ingest (today)")
            
            # Fetch fixtures for target date (or today if not specified)
            fixtures = await workflow.execute_activity(
                ingest_activities.fetch_todays_fixtures,
                target_date_str,  # Pass date string to activity (None = today)
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(
                    maximum_attempts=3,
                    initial_interval=timedelta(seconds=1),
                    maximum_interval=timedelta(seconds=10),
                ),
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
        
        workflow.logger.info(
            f"✅ Ingest complete: {result.get('staging', 0)} staging, "
            f"{result.get('active', 0)} active, {result.get('completed', 0)} completed, "
            f"{rag_success}/{len(teams_processed)} teams RAG cached"
        )
        return result
