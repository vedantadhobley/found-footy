"""Update event with discovered video URLs"""
from typing import Any, Dict, List

from dagster import OpExecutionContext, op

from src.data.mongo_store import FootyMongoStore


@op(
    name="update_event_discovered_videos",
    description="Save discovered video URLs to event document in events_confirmed",
    tags={"kind": "database", "purpose": "update"}
)
def update_event_discovered_videos_op(
    context: OpExecutionContext,
    extraction_result: Dict[str, Any]
) -> Dict[str, Any]:
    """
    Update event document with discovered video URLs.
    
    Sets processing_status to 'discovered' and saves video metadata
    to discovered_videos field.
    
    Args:
        extraction_result: Dict with event_id and videos from extract_videos_op
        
    Returns:
        Result dict with update status
    """
    store = FootyMongoStore()
    
    # Extract event_id and video metadata
    event_id = extraction_result.get("event_id", "unknown")
    fixture_id = extraction_result.get("fixture_id")
    video_metadata = extraction_result.get("videos", [])
    
    if not video_metadata:
        context.log.warning(f"⚠️  No videos discovered for event {event_id}")
        
        # Update event with empty discovered_videos
        store.update_event_processing_status(
            event_id=event_id,
            status="discovered",
            discovered_videos=[]
        )
        
        return {
            "status": "no_videos",
            "event_id": event_id,
            "videos_count": 0
        }
    
    context.log.info(f"💾 Updating event {event_id} with {len(video_metadata)} discovered videos")
    
    try:
        success = store.update_event_processing_status(
            event_id=event_id,
            status="discovered",
            discovered_videos=video_metadata
        )
        
        if success:
            context.log.info(f"✅ Updated event {event_id} with {len(video_metadata)} videos")
            return {
                "status": "success",
                "event_id": event_id,
                "videos_count": len(video_metadata)
            }
        else:
            context.log.warning(f"⚠️  Event {event_id} not found or not modified")
            return {
                "status": "not_found",
                "event_id": event_id,
                "videos_count": len(video_metadata)
            }
    
    except Exception as e:
        context.log.error(f"❌ Failed to update event {event_id}: {e}")
        raise
