"""
Ingest Workflow - Daily at 00:05 UTC

Fetches today's fixtures and routes them to correct collections based on status.
"""
from temporalio import workflow
from temporalio.common import RetryPolicy
from datetime import timedelta

with workflow.unsafe.imports_passed_through():
    from src.activities import ingest as ingest_activities


@workflow.defn
class IngestWorkflow:
    """Fetch today's fixtures and categorize by status"""
    
    @workflow.run
    async def run(self) -> dict:
        """
        Workflow:
        1. Fetch fixtures for today from API-Football
        2. Route to correct collection:
           - TBD/NS → fixtures_staging
           - LIVE/1H/HT/2H/ET/P/BT → fixtures_active  
           - FT/AET/PEN → fixtures_completed
        """
        
        # Fetch today's fixtures
        fixtures = await workflow.execute_activity(
            ingest_activities.fetch_todays_fixtures,
            start_to_close_timeout=timedelta(seconds=30),
            retry_policy=RetryPolicy(
                maximum_attempts=3,
                initial_interval=timedelta(seconds=1),
                maximum_interval=timedelta(seconds=10),
            ),
        )
        
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
        
        # Add total fixture count for logging
        result["total_fixtures"] = len(fixtures)
        return result
