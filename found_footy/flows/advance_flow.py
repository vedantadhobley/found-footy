from prefect import flow, get_run_logger
from typing import Optional
from found_footy.flows.shared_tasks import fixtures_advance_task
from found_footy.flows.flow_naming import runtime_advance_flow_name

@flow(
    name="advance-flow"
    # ❌ NO flow_run_name here - will be set by triggering code
)
def advance_flow(
    source_collection: str = "fixtures_staging", 
    destination_collection: str = "fixtures_active",
    fixture_id: Optional[int] = None
):
    """Pure fixture advancement - runtime naming from parameters"""
    logger = get_run_logger()
    
    # ✅ Set name at runtime using current parameters
    logger.info(f"🏷️ Flow name: {runtime_advance_flow_name()}")
    
    logger.info(f"📋 Pure advancement: {source_collection} → {destination_collection}")
    
    if fixture_id:
        logger.info(f"🎯 Processing specific fixture: {fixture_id}")
    
    advance_result = fixtures_advance_task(source_collection, destination_collection, fixture_id)
    
    if advance_result["status"] == "success" and advance_result["advanced_count"] > 0:
        if destination_collection == "fixtures_active":
            logger.info(f"🚀 KICKOFF: {advance_result['advanced_count']} matches now live")
        elif destination_collection == "fixtures_completed":            
            logger.info(f"🏁 COMPLETED: {advance_result['advanced_count']} matches archived")
        else:
            logger.info(f"🔄 ADVANCED: {advance_result['advanced_count']} matches moved")
    
    return {
        "status": advance_result["status"],
        "source_collection": source_collection,
        "destination_collection": destination_collection,
        "fixture_id": fixture_id,
        "advanced_count": advance_result.get("advanced_count", 0),
        "note": "Pure advancement - monitor handles all live goal detection"
    }