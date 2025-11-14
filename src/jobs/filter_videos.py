"""Filter videos op - deduplicates videos for a single goal using OpenCV"""

from dagster import op, Config
from typing import Dict, Any
from pymongo import MongoClient
from bson import ObjectId
from datetime import datetime
import cv2
import numpy as np
import hashlib
import tempfile
from pathlib import Path


class MongoConfig(Config):
    """MongoDB connection configuration."""
    mongo_uri: str = "mongodb://localhost:27017"
    db_name: str = "found_footy"


@op(
    name="filter_videos",
    description="Deduplicate videos for a single goal using hash and OpenCV analysis"
)
def filter_videos_op(context, config: MongoConfig, upload_result: Dict) -> Dict[str, Any]:
    """
    Deduplicate videos for ONE goal - matches filter_flow.py.
    
    Uses two-stage deduplication:
    1. Hash-based: Remove exact file duplicates
    2. OpenCV-based: Remove visually similar videos (95%+ similarity)
    """
    from found_footy.storage.s3_store import S3Store
    
    goal_id = upload_result["goal_id"]
    player = upload_result["player"]
    minute = upload_result["minute"]
    
    client = MongoClient(config.mongo_uri)
    db = client[config.db_name]
    s3 = S3Store()
    
    # Get all uploaded videos for THIS goal
    videos = list(db.videos.find({
        "goal_id": ObjectId(goal_id),
        "upload_status": "completed",
        "s3_key": {"$exists": True}
    }))
    
    if len(videos) <= 1:
        # No deduplication needed
        if videos:
            db.goals.update_one(
                {"_id": ObjectId(goal_id)},
                {"$set": {
                    "processing_status.videos_filtered": True,
                    "processing_status.completed": True
                }}
            )
        
        client.close()
        context.log.info(f"Only {len(videos)} video(s), no deduplication needed")
        
        return {
            "goal_id": goal_id,
            "player": player,
            "minute": minute,
            "videos_kept": len(videos),
            "videos_removed": 0
        }
    
    context.log.info(f"🔍 Deduplicating {len(videos)} videos for {player} ({minute}')")
    
    # Download all videos for comparison
    video_data = []
    with tempfile.TemporaryDirectory() as tmpdir:
        for video in videos:
            local_path = Path(tmpdir) / f"{video['_id']}.mp4"
            
            try:
                s3.download_file(video["s3_key"], str(local_path))
                
                # Compute hash
                with open(local_path, 'rb') as f:
                    file_hash = hashlib.sha256(f.read()).hexdigest()
                
                video_data.append({
                    "id": str(video["_id"]),
                    "path": str(local_path),
                    "hash": file_hash
                })
            except Exception as e:
                context.log.error(f"Failed to download {video['_id']} for filtering: {e}")
        
        # Stage 1: Hash-based deduplication
        unique_by_hash = {}
        for vid in video_data:
            if vid["hash"] not in unique_by_hash:
                unique_by_hash[vid["hash"]] = vid
        
        remaining = list(unique_by_hash.values())
        context.log.info(f"After hash dedup: {len(remaining)}/{len(videos)} videos remain")
        
        # Stage 2: OpenCV similarity comparison (only if multiple videos remain)
        if len(remaining) > 1:
            to_keep = [remaining[0]]
            
            for candidate in remaining[1:]:
                is_duplicate = False
                
                for keeper in to_keep:
                    try:
                        cap1 = cv2.VideoCapture(keeper["path"])
                        cap2 = cv2.VideoCapture(candidate["path"])
                        
                        frames1, frames2 = [], []
                        for _ in range(10):  # Sample 10 frames
                            ret1, frame1 = cap1.read()
                            ret2, frame2 = cap2.read()
                            if ret1 and ret2:
                                frames1.append(frame1)
                                frames2.append(frame2)
                        
                        cap1.release()
                        cap2.release()
                        
                        if frames1 and frames2:
                            similarities = []
                            for f1, f2 in zip(frames1, frames2):
                                # Resize for faster comparison
                                f1_resized = cv2.resize(f1, (320, 240))
                                f2_resized = cv2.resize(f2, (320, 240))
                                
                                # Compute color histograms
                                hist1 = cv2.calcHist([f1_resized], [0, 1, 2], None, [8, 8, 8], [0, 256] * 3)
                                hist2 = cv2.calcHist([f2_resized], [0, 1, 2], None, [8, 8, 8], [0, 256] * 3)
                                
                                # Compare histograms
                                similarity = cv2.compareHist(hist1, hist2, cv2.HISTCMP_CORREL)
                                similarities.append(similarity)
                            
                            avg_similarity = np.mean(similarities)
                            
                            if avg_similarity > 0.95:
                                is_duplicate = True
                                context.log.info(f"Video {candidate['id']} is duplicate of {keeper['id']} (similarity: {avg_similarity:.3f})")
                                break
                    
                    except Exception as e:
                        context.log.error(f"Failed to compare videos: {e}")
                
                if not is_duplicate:
                    to_keep.append(candidate)
            
            remaining = to_keep
        
        # Mark duplicates in MongoDB
        kept_ids = {vid["id"] for vid in remaining}
        removed_count = 0
        
        for video in videos:
            video_id = str(video["_id"])
            if video_id not in kept_ids:
                db.videos.update_one(
                    {"_id": video["_id"]},
                    {"$set": {
                        "duplicate": True,
                        "filtered_at": datetime.utcnow()
                    }}
                )
                removed_count += 1
                context.log.info(f"Marked {video_id} as duplicate")
    
    # Mark goal as filtered and completed
    db.goals.update_one(
        {"_id": ObjectId(goal_id)},
        {"$set": {
            "processing_status.videos_filtered": True,
            "processing_status.completed": True,
            "completed_at": datetime.utcnow()
        }}
    )
    
    client.close()
    
    context.log.info(f"✅ Kept {len(remaining)} videos, removed {removed_count} duplicates")
    
    return {
        "goal_id": goal_id,
        "player": player,
        "minute": minute,
        "videos_kept": len(remaining),
        "videos_removed": removed_count
    }
