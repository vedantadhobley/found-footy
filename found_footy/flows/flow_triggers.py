"""Professional flow triggering service with rich naming"""
import asyncio
from datetime import datetime
from prefect import get_client
from prefect.states import Scheduled
from found_footy.flows.flow_naming import get_fixtures_advance_name  # ✅ ADD: Import your naming service

async def schedule_fixtures_advance_async(source_collection, destination_collection, fixture_id, scheduled_time=None):
    """NON-BLOCKING: Use async client with RICH team context naming"""
    try:
        async with get_client() as client:
            deployment = await client.read_deployment_by_name("fixtures-advance-flow/fixtures-advance-flow")
            
            # ✅ UPDATED: Uses correct collection name in naming
            flow_name = get_fixtures_advance_name(source_collection, destination_collection, fixture_id)
            
            if scheduled_time:
                flow_run = await client.create_flow_run_from_deployment(
                    deployment.id,
                    parameters={
                        "source_collection": source_collection,
                        "destination_collection": destination_collection,
                        "fixture_id": fixture_id
                    },
                    name=flow_name,  # ✅ RICH NAME: Shows team names
                    state=Scheduled(scheduled_time=scheduled_time)
                )
                return {"status": "scheduled", "flow_run_id": str(flow_run.id)}
            else:
                flow_run = await client.create_flow_run_from_deployment(
                    deployment.id,
                    parameters={
                        "source_collection": source_collection,
                        "destination_collection": destination_collection,
                        "fixture_id": fixture_id
                    },
                    name=flow_name  # ✅ RICH NAME: Shows team names
                )
                return {"status": "immediate", "flow_run_id": str(flow_run.id)}
                
    except Exception as e:
        return {"status": "error", "error": str(e)}

def schedule_advance(source, destination, fixture_id, scheduled_time=None):
    """Clean wrapper"""
    try:
        return asyncio.run(schedule_fixtures_advance_async(source, destination, fixture_id, scheduled_time))
    except RuntimeError:
        import nest_asyncio
        nest_asyncio.apply()
        return asyncio.run(schedule_fixtures_advance_async(source, destination, fixture_id, scheduled_time))