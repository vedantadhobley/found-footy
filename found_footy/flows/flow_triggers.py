"""Professional flow triggering service with rich naming and scheduling"""
import asyncio
from datetime import datetime, timedelta
from prefect import get_client
from prefect.states import Scheduled
from found_footy.flows.flow_naming import get_advance_flow_name, get_twitter_flow_name

async def schedule_advance_flow_async(source_collection, destination_collection, fixture_id, scheduled_time=None):
    """NON-BLOCKING: Use async client with RICH team context naming"""
    try:
        async with get_client() as client:
            deployment = await client.read_deployment_by_name("advance-flow/advance-flow")
            
            # ✅ Generate name with CURRENT fixture data (for scheduled flows)
            # This gets the team names NOW while we have the data
            flow_name = get_advance_flow_name(source_collection, destination_collection, fixture_id)
            
            if scheduled_time:
                flow_run = await client.create_flow_run_from_deployment(
                    deployment.id,
                    parameters={
                        "source_collection": source_collection,
                        "destination_collection": destination_collection,
                        "fixture_id": fixture_id
                    },
                    name=flow_name,  # ✅ Set rich name at scheduling time
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
                    name=flow_name  # ✅ Set rich name immediately
                )
                return {"status": "immediate", "flow_run_id": str(flow_run.id)}
                
    except Exception as e:
        return {"status": "error", "error": str(e)}

async def schedule_twitter_flow_async(goal_id, delay_minutes=0):
    """NON-BLOCKING: Schedule Twitter flow with optional delay"""
    try:
        async with get_client() as client:
            deployment = await client.read_deployment_by_name("twitter-flow/twitter-flow")
            
            # ✅ Generate rich name with goal context NOW
            flow_name = get_twitter_flow_name(goal_id)
            
            if delay_minutes > 0:
                scheduled_time = datetime.now() + timedelta(minutes=delay_minutes)
                
                flow_run = await client.create_flow_run_from_deployment(
                    deployment.id,
                    parameters={"goal_id": goal_id},
                    name=f"{flow_name} [⏰ +{delay_minutes}min]",  # ✅ Indicate delay in name
                    state=Scheduled(scheduled_time=scheduled_time)
                )
                return {
                    "status": "scheduled", 
                    "flow_run_id": str(flow_run.id),
                    "scheduled_time": scheduled_time.isoformat(),
                    "delay_minutes": delay_minutes
                }
            else:
                flow_run = await client.create_flow_run_from_deployment(
                    deployment.id,
                    parameters={"goal_id": goal_id},
                    name=flow_name
                )
                return {"status": "immediate", "flow_run_id": str(flow_run.id)}
                
    except Exception as e:
        return {"status": "error", "error": str(e)}

def schedule_advance_flow(source, destination, fixture_id, scheduled_time=None):
    """Clean wrapper for advance flow scheduling - RENAMED from schedule_advance"""
    try:
        return asyncio.run(schedule_advance_flow_async(source, destination, fixture_id, scheduled_time))
    except RuntimeError:
        import nest_asyncio
        nest_asyncio.apply()
        return asyncio.run(schedule_advance_flow_async(source, destination, fixture_id, scheduled_time))

def schedule_twitter_flow(goal_id, delay_minutes=0):
    """Clean wrapper for Twitter flow scheduling with delay"""
    try:
        return asyncio.run(schedule_twitter_flow_async(goal_id, delay_minutes))
    except RuntimeError:
        import nest_asyncio
        nest_asyncio.apply()
        return asyncio.run(schedule_twitter_flow_async(goal_id, delay_minutes))