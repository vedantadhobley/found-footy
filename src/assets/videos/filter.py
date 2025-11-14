"""Video filtering asset - deduplicates videos using OpenCV"""

import logging
from dagster import asset, AssetExecutionContext, Output
from src.data.mongo_store import FootyMongoStore

logger = logging.getLogger(__name__)


@asset(
    name="filter_videos",
    description="Deduplicate videos using multi-level analysis (duration, resolution, frame comparison)",
    group_name="videos",
    compute_kind="processing"
)
def filter_videos_asset(
    context: AssetExecutionContext,
    goal_id: str
) -> Output:
    """
    Deduplicate videos - migrated from filter_flow.py
    
    Steps:
    1. Get all downloaded videos for a goal
    2. Analyze video properties (duration, resolution, bitrate)
    3. Compare frames using OpenCV histogram comparison
    4. Select best quality unique video
    5. Mark duplicates for deletion
    
    TODO: Implement full deduplication logic from filter_flow.py
    """
    store = FootyMongoStore()
    
    # Get goal document
    goal_doc = store.goals.find_one({"_id": goal_id})
    
    if not goal_doc:
        context.log.error(f"Goal {goal_id} not found")
        return Output(
            value={"goal_id": goal_id, "videos_filtered": 0},
            metadata={"status": "error", "error": "Goal not found"}
        )
    
    video_count = goal_doc.get("video_count", 0)
    
    if video_count == 0:
        context.log.warning(f"No videos to filter for {goal_id}")
        return Output(
            value={"goal_id": goal_id, "videos_filtered": 0},
            metadata={"status": "no_videos"}
        )
    
    context.log.info(f"🔍 Filtering {video_count} videos for {goal_id}")
    
    # TODO: Implement full deduplication logic:
    # 1. Get all video documents from MongoDB
    # 2. Group by similar properties (duration ±2s, resolution)
    # 3. Compare frames using OpenCV histogram
    # 4. Select best quality from each group
    # 5. Mark duplicates
    
    # Placeholder for now
    duplicates_found = 0
    unique_videos = video_count
    
    store.update_goal_processing_status(
        goal_id, 
        "videos_filtered",
        unique_videos=unique_videos,
        duplicates_found=duplicates_found
    )
    
    return Output(
        value={
            "goal_id": goal_id,
            "total_videos": video_count,
            "unique_videos": unique_videos,
            "duplicates": duplicates_found
        },
        metadata={
            "goal_id": goal_id,
            "total_videos": video_count,
            "unique_videos": unique_videos,
            "duplicates_removed": duplicates_found
        }
    )
