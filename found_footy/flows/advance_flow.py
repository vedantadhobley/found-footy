# ✅ NEW: found_footy/flows/advance_flow.py
from prefect import flow, get_run_logger
from typing import Optional

from found_footy.flows.shared_tasks import fixtures_advance_task

@flow(name="advance-flow")
def advance_flow(
    source_collection: str = "fixtures_staging", 
    destination_collection: str = "fixtures_active",
    fixture_id: Optional[int] = None
):
    """Pure fixture advancement - no goal processing, monitor handles everything"""
    logger = get_run_logger()
    
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