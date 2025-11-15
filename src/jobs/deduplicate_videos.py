"""Deduplicate videos job - Remove duplicate videos using multi-level analysis

This job:
1. Downloads all videos from S3 for a goal
2. Detects duplicates using file size, hash, and OpenCV quality analysis
3. Keeps the best quality video
4. Deletes duplicates from S3
5. Updates goal with filtered results
"""

from dagster import op, job, Config, OpExecutionContext, Out
from typing import Dict, Any, List
from datetime import datetime, timezone
import tempfile
import os
import hashlib
import cv2


class DeduplicationConfig(Config):
    """Deduplication configuration"""
    mongo_uri: str = "mongodb://ffuser:ffpass@mongo:27017/found_footy?authSource=admin"
    db_name: str = "found_footy"
    goal_id: str


@op(
    name="deduplicate_videos",
    description="Download videos from S3, deduplicate, and delete duplicates",
    out=Out(Dict[str, Any])
)
def deduplicate_videos_op(context: OpExecutionContext, config: DeduplicationConfig) -> Dict[str, Any]:
    """Download videos from S3 and remove duplicates - keep highest quality"""
    from src.data.mongo_store import FootyMongoStore
    from src.data.s3_store import FootyS3Store
    
    goal_id = config.goal_id
    
    context.log.info(f"🔍 Starting deduplication for goal: {goal_id}")
    
    store = FootyMongoStore()
    s3_store = FootyS3Store()
    
    # Get goal with completed status
    goal_doc = store.goals.find_one({"_id": goal_id, "processing_status": "completed"})
    if not goal_doc:
        context.log.warning(f"⚠️ Goal {goal_id} not found or not completed")
        return {"status": "not_found", "goal_id": goal_id}
    
    successful_uploads = goal_doc.get("successful_uploads", [])
    if not successful_uploads:
        context.log.info(f"⚠️ No videos to deduplicate for goal {goal_id}")
        return {"status": "no_videos", "goal_id": goal_id}
    
    if len(successful_uploads) == 1:
        context.log.info(f"✅ Only 1 video - no deduplication needed for goal {goal_id}")
        # Still update status to "filtered" for consistency
        store.update_goal_processing_status(
            goal_id,
            "filtered",
            filtered_uploads=successful_uploads,
            original_video_count=1,
            filtered_video_count=1,
            duplicates_removed=0,
            filter_completed_at=datetime.now(timezone.utc).isoformat()
        )
        return {
            "status": "success",
            "goal_id": goal_id,
            "original_count": 1,
            "filtered_count": 1,
            "duplicates_removed": 0,
            "method": "single_video_no_dedup_needed"
        }
    
    context.log.info(f"🔍 Deduplicating {len(successful_uploads)} videos for goal {goal_id}")
    
    # Download all videos to temp directory for analysis
    video_data = []
    with tempfile.TemporaryDirectory() as temp_dir:
        
        # Step 1: Download all videos
        context.log.info(f"📥 Downloading {len(successful_uploads)} videos for analysis...")
        for i, upload in enumerate(successful_uploads):
            s3_key = upload.get("s3_key")
            if not s3_key:
                continue
            
            local_path = os.path.join(temp_dir, f"video_{i}.mp4")
            
            try:
                # Download from S3
                s3_store.s3_client.download_file(
                    s3_store.bucket_name,
                    s3_key,
                    local_path
                )
                
                video_data.append({
                    "index": i,
                    "s3_key": s3_key,
                    "local_path": local_path,
                    "upload_info": upload,
                    "file_size": upload.get("file_size", 0)
                })
                
                context.log.info(f"  ✅ Downloaded video {i}: {s3_key} ({upload.get('file_size', 0)} bytes)")
                
            except Exception as e:
                context.log.error(f"❌ Failed to download {s3_key}: {e}")
                continue
        
        if not video_data:
            context.log.warning(f"⚠️ No videos could be downloaded for analysis")
            return {"status": "download_failed", "goal_id": goal_id}
        
        context.log.info(f"✅ Downloaded {len(video_data)} videos successfully")
        
        # Step 2: Multi-level duplicate detection
        context.log.info(f"🔍 Detecting duplicates using multi-level analysis...")
        unique_videos = detect_duplicates_advanced(video_data, context)
        
        # Step 3: Delete duplicate videos from S3
        videos_to_delete = []
        for video in video_data:
            s3_key = video["s3_key"]
            if not any(v["s3_key"] == s3_key for v in unique_videos):
                videos_to_delete.append(video)
        
        deleted_count = 0
        if videos_to_delete:
            context.log.info(f"🗑️ Deleting {len(videos_to_delete)} duplicate videos from S3...")
            for video in videos_to_delete:
                try:
                    s3_store.s3_client.delete_object(Bucket=s3_store.bucket_name, Key=video["s3_key"])
                    deleted_count += 1
                    context.log.info(f"   🗑️ Deleted duplicate: {video['s3_key']}")
                except Exception as e:
                    context.log.error(f"❌ Failed to delete {video['s3_key']}: {e}")
        else:
            context.log.info(f"✅ No duplicates found - all videos are unique!")
    
    # Update goal with filtered results
    filtered_uploads = [v["upload_info"] for v in unique_videos]
    
    store.update_goal_processing_status(
        goal_id,
        "filtered",
        filtered_uploads=filtered_uploads,
        original_video_count=len(successful_uploads),
        filtered_video_count=len(filtered_uploads),
        duplicates_removed=len(successful_uploads) - len(unique_videos),
        filter_completed_at=datetime.now(timezone.utc).isoformat()
    )
    
    context.log.info(f"✅ Deduplication complete for goal {goal_id}:")
    context.log.info(f"   📊 {len(successful_uploads)} → {len(filtered_uploads)} videos")
    context.log.info(f"   🗑️ Removed {len(successful_uploads) - len(unique_videos)} duplicates")
    context.log.info(f"   ☁️ Deleted {deleted_count} files from S3")
    
    return {
        "status": "success",
        "goal_id": goal_id,
        "original_count": len(successful_uploads),
        "filtered_count": len(filtered_uploads),
        "duplicates_removed": len(successful_uploads) - len(unique_videos),
        "s3_deletions": deleted_count,
        "method": "multi_level_deduplication"
    }


def detect_duplicates_advanced(video_data: List[Dict], context) -> List[Dict]:
    """Advanced multi-level duplicate detection"""
    
    if len(video_data) <= 1:
        return video_data
    
    context.log.info(f"🔍 Multi-level duplicate detection on {len(video_data)} videos...")
    
    # Level 1: File size grouping (quick filter)
    context.log.info("   📏 Level 1: Grouping by file size...")
    size_groups = {}
    for video in video_data:
        size = video["file_size"]
        if size not in size_groups:
            size_groups[size] = []
        size_groups[size].append(video)
    
    context.log.info(f"   📊 Found {len(size_groups)} different file sizes")
    
    unique_videos = []
    
    for size, videos in size_groups.items():
        if len(videos) == 1:
            # Only one video of this size - definitely unique
            unique_videos.extend(videos)
            context.log.info(f"   ✅ Size {size} bytes: 1 video (unique)")
            continue
        
        context.log.info(f"   🔍 Size {size} bytes: {len(videos)} videos (checking for duplicates)")
        
        # Level 2: File hash comparison
        hash_groups = {}
        for video in videos:
            try:
                with open(video["local_path"], "rb") as f:
                    # Use first 64KB + last 64KB for faster hashing of large files
                    start_chunk = f.read(65536)  # 64KB
                    f.seek(-65536, 2)  # Seek to 64KB from end
                    end_chunk = f.read()
                    
                    combined_content = start_chunk + end_chunk
                    file_hash = hashlib.md5(combined_content).hexdigest()
                
                if file_hash not in hash_groups:
                    hash_groups[file_hash] = []
                hash_groups[file_hash].append(video)
                
            except Exception as e:
                context.log.error(f"❌ Error hashing {video['local_path']}: {e}")
                # If hashing fails, assume it's unique to be safe
                unique_videos.append(video)
                continue
        
        # Level 3: For each hash group, pick best quality
        for file_hash, hash_videos in hash_groups.items():
            if len(hash_videos) == 1:
                unique_videos.append(hash_videos[0])
                context.log.info(f"      ✅ Hash {file_hash[:8]}: 1 video (unique)")
            else:
                # Level 4: Video content analysis for final decision
                best_video = select_best_quality_video(hash_videos, context)
                unique_videos.append(best_video)
                context.log.info(f"      🎯 Hash {file_hash[:8]}: {len(hash_videos)} identical → kept best quality (index {best_video['index']})")
    
    context.log.info(f"✅ Deduplication result: {len(video_data)} → {len(unique_videos)} unique videos")
    return unique_videos


def select_best_quality_video(videos: List[Dict], context) -> Dict:
    """Select the best quality video from identical files"""
    
    if len(videos) == 1:
        return videos[0]
    
    # Scoring criteria for video quality
    best_video = None
    best_score = -1
    
    for video in videos:
        score = 0
        
        try:
            # Analyze video properties using OpenCV
            cap = cv2.VideoCapture(video["local_path"])
            
            if cap.isOpened():
                # Get video properties
                width = int(cap.get(cv2.CAP_PROP_FRAME_WIDTH))
                height = int(cap.get(cv2.CAP_PROP_FRAME_HEIGHT))
                fps = cap.get(cv2.CAP_PROP_FPS)
                frame_count = int(cap.get(cv2.CAP_PROP_FRAME_COUNT))
                
                cap.release()
                
                # Score based on resolution (higher is better)
                resolution_score = width * height
                score += resolution_score / 10000  # Normalize
                
                # Score based on FPS (30fps is ideal, more is better up to 60)
                if fps > 0:
                    fps_score = min(fps, 60) / 60  # Max score of 1 for 60fps
                    score += fps_score * 100
                
                # Score based on duration (longer is usually better for goal clips)
                if fps > 0 and frame_count > 0:
                    duration = frame_count / fps
                    # Prefer 10-60 second clips
                    if 10 <= duration <= 60:
                        duration_score = 1.0
                    elif duration > 60:
                        duration_score = max(0.5, 1.0 - (duration - 60) / 120)  # Penalty for too long
                    else:
                        duration_score = duration / 10  # Penalty for too short
                    score += duration_score * 50
                
                # File size as tiebreaker (larger usually means better quality)
                size_score = video["file_size"] / 1000000  # MB
                score += size_score * 0.1
                
                context.log.info(f"      📊 Video {video['index']}: {width}x{height} @ {fps:.1f}fps, {frame_count} frames → score: {score:.1f}")
                
            else:
                context.log.warning(f"      ⚠️ Could not analyze video {video['index']}")
                # Fallback to file size only
                score = video["file_size"] / 1000000
                
        except Exception as e:
            context.log.error(f"      ❌ Error analyzing video {video['index']}: {e}")
            # Fallback to file size only
            score = video["file_size"] / 1000000
        
        if score > best_score:
            best_score = score
            best_video = video
    
    return best_video or videos[0]  # Fallback to first if all scoring fails


@job(
    name="deduplicate_videos_job",
    description="Deduplicate videos for a goal using multi-level analysis"
)
def deduplicate_videos_job():
    """
    Video deduplication pipeline:
    
    1. Download all videos from S3
    2. Group by file size (quick filter)
    3. Hash comparison for same-size videos
    4. OpenCV quality analysis for identical videos
    5. Keep best quality, delete duplicates from S3
    6. Update goal with filtered results
    """
    deduplicate_videos_op()


__all__ = ["deduplicate_videos_job"]
