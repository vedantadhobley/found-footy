"""Professional flow triggering service with rich naming"""
import asyncio
import json
from datetime import datetime, timedelta
from prefect import get_client
from prefect.deployments import run_deployment
from prefect.states import Scheduled  # ✅ FIXED: Correct import path for Prefect 3
from found_footy.flows.flow_naming import FlowNamingService

class FlowTriggerService:
    """Professional flow triggering with centralized naming"""
    
    @staticmethod
    def trigger_fixtures_ingest(date_str=None, team_ids=None, run_now=True):
        """Trigger fixtures ingest with rich naming"""
        flow_name = FlowNamingService.get_fixtures_ingest_name(date_str)
        
        return run_deployment(
            name="fixtures-ingest-manual/fixtures-ingest-manual",
            parameters={
                "date_str": date_str,
                "team_ids": team_ids
            },
            flow_run_name=flow_name,
            timeout=0 if run_now else None
        )
    
    @staticmethod
    def trigger_fixtures_monitor(run_now=True):
        """Trigger monitor with timestamp"""
        flow_name = FlowNamingService.get_fixtures_monitor_name()
        
        return run_deployment(
            name="fixtures-monitor-flow/fixtures-monitor-flow",
            parameters={},
            flow_run_name=flow_name,
            timeout=0 if run_now else None
        )
    
    @staticmethod
    async def schedule_fixtures_advance_async(
        source_collection,
        destination_collection, 
        fixture_id,
        scheduled_time=None,
        run_now=False
    ):
        """NON-BLOCKING: Use async client with CORRECT Prefect 3 scheduling"""
        try:
            async with get_client() as client:
                # Get deployment
                deployment = await client.read_deployment_by_name(
                    "fixtures-advance-flow/fixtures-advance-flow"
                )
                
                # Generate rich flow name
                flow_name = FlowNamingService.get_fixtures_advance_name(
                    source_collection, destination_collection, fixture_id
                )
                
                if scheduled_time and not run_now:
                    # ✅ CORRECT: Create flow run with Scheduled state
                    flow_run = await client.create_flow_run_from_deployment(
                        deployment.id,
                        parameters={
                            "source_collection": source_collection,
                            "destination_collection": destination_collection,
                            "fixture_id": fixture_id
                        },
                        name=flow_name,
                        state=Scheduled(scheduled_time=scheduled_time)  # ✅ CORRECT: Using proper import
                    )
                    
                    seconds_until = int((scheduled_time - datetime.now(scheduled_time.tzinfo)).total_seconds())
                    print(f"✅ ASYNC: Scheduled {flow_name} in {seconds_until}s")
                    return {"status": "scheduled", "flow_run_id": str(flow_run.id), "seconds_until": seconds_until}
                else:
                    # Immediate run
                    flow_run = await client.create_flow_run_from_deployment(
                        deployment.id,
                        parameters={
                            "source_collection": source_collection,
                            "destination_collection": destination_collection,
                            "fixture_id": fixture_id
                        },
                        name=flow_name
                    )
                    return {"status": "immediate", "flow_run_id": str(flow_run.id)}
                    
        except Exception as e:
            print(f"❌ Error in async scheduling: {e}")
            return {"status": "error", "error": str(e)}

    @staticmethod
    def trigger_twitter_search(goal_id):
        """Trigger Twitter search with goal context"""
        flow_name = FlowNamingService.get_twitter_search_name(goal_id)
        
        return run_deployment(
            name="twitter-search-flow/twitter-search-flow",
            parameters={"goal_id": goal_id},
            flow_run_name=flow_name,
            timeout=0
        )

# ✅ CONVENIENCE FUNCTIONS
def trigger_ingest(date_str=None, team_ids=None):
    return FlowTriggerService.trigger_fixtures_ingest(date_str, team_ids)

def trigger_monitor():
    return FlowTriggerService.trigger_fixtures_monitor()

def schedule_advance(source, destination, fixture_id, scheduled_time=None):
    """Clean non-blocking wrapper using async client"""
    try:
        return asyncio.run(FlowTriggerService.schedule_fixtures_advance_async(
            source, destination, fixture_id, scheduled_time, run_now=False
        ))
    except RuntimeError as e:
        if "cannot be called from a running event loop" in str(e):
            # Handle case where event loop is already running (in flow context)
            import nest_asyncio
            nest_asyncio.apply()
            return asyncio.run(FlowTriggerService.schedule_fixtures_advance_async(
                source, destination, fixture_id, scheduled_time, run_now=False
            ))
        else:
            raise e

def trigger_twitter(goal_id):
    return FlowTriggerService.trigger_twitter_search(goal_id)