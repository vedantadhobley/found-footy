"""Complete goal processing pipeline - simplified without nested dynamics

To avoid Dagster's "downstream of multiple dynamics" error, this pipeline
runs all work sequentially in ops without nested .map() calls.
"""

from dagster import job, op, Config
from typing import Dict, Any
from pymongo import MongoClient
from bson import ObjectId
from datetime import datetime


class MongoConfig(Config):
    """MongoDB connection configuration."""
    mongo_uri: str = "mongodb://localhost:27017"
    db_name: str = "found_footy"


@op(
    name="monitor_process_and_scrape",
    description="Monitor fixtures, process goals, and scrape Twitter"
)
def monitor_process_and_scrape_op(context, config: MongoConfig) -> Dict[str, Any]:
    """
    Monitor fixtures, process all goals, and scrape Twitter.
    Returns summary of goals that need video downloads.
    """
    from found_footy.services.twitter_session_isolated import TwitterSessionManager
    
    client = MongoClient(config.mongo_uri)
    db = client[config.db_name]
    
    # Find fixtures with goals that need processing
    fixtures = db.fixtures.find({
        "goals": {"$exists": True, "$ne": []},
        "fixture.status.short": {"$in": ["FT", "AET", "PEN"]}
    })
    
    twitter = TwitterSessionManager()
    fixtures_processed = 0
    goals_processed = 0
    videos_found = 0
    
    for fixture in fixtures:
        fixture_id = str(fixture["_id"])
        goals = fixture.get("goals", [])
        
        if not goals:
            continue
        
        home_team = fixture["teams"]["home"]["name"]
        away_team = fixture["teams"]["away"]["name"]
        
        context.log.info(f"Processing fixture {fixture_id} with {len(goals)} goals")
        fixtures_processed += 1
        
        for goal_event in goals:
            # Check if goal already exists
            existing = db.goals.find_one({
                "fixture_id": ObjectId(fixture_id),
                "time.elapsed": goal_event["time"]["elapsed"]
            })
            
            if not existing:
                # Create new goal
                goal_doc = {
                    "fixture_id": ObjectId(fixture_id),
                    "time": goal_event["time"],
                    "team": goal_event["team"],
                    "player": goal_event["player"],
                    "assist": goal_event.get("assist"),
                    "type": goal_event["type"],
                    "detail": goal_event["detail"],
                    "comments": goal_event.get("comments"),
                    "processing_status": {
                        "twitter_scraped": False,
                        "videos_downloaded": False,
                        "videos_filtered": False,
                        "completed": False
                    },
                    "created_at": datetime.utcnow()
                }
                
                result = db.goals.insert_one(goal_doc)
                goal_id = str(result.inserted_id)
                context.log.info(f"Created new goal {goal_id}")
            else:
                goal_id = str(existing["_id"])
                status = existing.get("processing_status", {})
                
                # Skip if already completed
                if status.get("completed", False):
                    continue
            
            goals_processed += 1
            
            # Scrape Twitter for this goal
            scorer = goal_event["player"]["name"]
            minute = goal_event["time"]["elapsed"]
            query = f"{home_team} {away_team} {scorer} {minute}'"
            
            try:
                context.log.info(f"Searching Twitter for: {query}")
                videos = twitter.search_videos(query, max_results=10)
                
                # Store video metadata
                for video in videos:
                    video_doc = {
                        "goal_id": ObjectId(goal_id),
                        "tweet_url": video["tweet_url"],
                        "video_url": video["video_url"],
                        "author": video["author"],
                        "text": video["text"],
                        "created_at": video["created_at"],
                        "download_status": "pending",
                        "discovered_at": datetime.utcnow()
                    }
                    db.videos.update_one(
                        {"goal_id": ObjectId(goal_id), "tweet_url": video["tweet_url"]},
                        {"$set": video_doc},
                        upsert=True
                    )
                
                # Update goal status
                db.goals.update_one(
                    {"_id": ObjectId(goal_id)},
                    {"$set": {"processing_status.twitter_scraped": True}}
                )
                
                videos_found += len(videos)
                context.log.info(f"Found {len(videos)} videos for goal {goal_id}")
            
            except Exception as e:
                context.log.error(f"Twitter search failed for goal {goal_id}: {e}")
    
    client.close()
    
    summary = {
        "fixtures_processed": fixtures_processed,
        "goals_processed": goals_processed,
        "videos_found": videos_found
    }
    
    context.log.info(f"Completed: {summary}")
    return summary


@op(
    name="download_all_videos",
    description="Download all pending videos"
)
def download_all_videos_op(context, config: MongoConfig, summary: Dict) -> Dict[str, Any]:
    """
    Download all videos with download_status='pending' using yt-dlp.
    """
    import subprocess
    import tempfile
    from pathlib import Path
    from found_footy.storage.s3_store import S3Store
    
    client = MongoClient(config.mongo_uri)
    db = client[config.db_name]
    
    # Get all videos to download
    videos = list(db.videos.find({"download_status": "pending"}))
    
    if not videos:
        context.log.info("No videos to download")
        client.close()
        return {"downloaded": 0}
    
    context.log.info(f"Downloading {len(videos)} videos")
    
    s3 = S3Store()
    downloaded_count = 0
    
    for video in videos:
        video_url = video["video_url"]
        video_id = str(video["_id"])
        goal_id = str(video["goal_id"])
        
        try:
            with tempfile.TemporaryDirectory() as tmpdir:
                output_path = Path(tmpdir) / f"{video_id}.mp4"
                
                # Download with yt-dlp
                cmd = [
                    "yt-dlp",
                    "-f", "best",
                    "-o", str(output_path),
                    video_url
                ]
                
                result = subprocess.run(cmd, capture_output=True, text=True, timeout=120)
                
                if result.returncode == 0 and output_path.exists():
                    # Upload to S3
                    s3_key = f"goals/{goal_id}/{video_id}.mp4"
                    s3.upload_file(str(output_path), s3_key)
                    
                    # Update video record
                    db.videos.update_one(
                        {"_id": video["_id"]},
                        {"$set": {
                            "download_status": "completed",
                            "s3_key": s3_key,
                            "downloaded_at": datetime.utcnow()
                        }}
                    )
                    
                    downloaded_count += 1
                    context.log.info(f"Downloaded video {video_id}")
                else:
                    context.log.error(f"yt-dlp failed for {video_url}")
                    db.videos.update_one(
                        {"_id": video["_id"]},
                        {"$set": {"download_status": "failed"}}
                    )
        
        except Exception as e:
            context.log.error(f"Failed to download video {video_id}: {e}")
            db.videos.update_one(
                {"_id": video["_id"]},
                {"$set": {"download_status": "failed"}}
            )
    
    # Update goal statuses
    goals_with_downloads = db.videos.distinct("goal_id", {"download_status": "completed"})
    for goal_id in goals_with_downloads:
        db.goals.update_one(
            {"_id": goal_id},
            {"$set": {"processing_status.videos_downloaded": True}}
        )
    
    client.close()
    context.log.info(f"Downloaded {downloaded_count}/{len(videos)} videos")
    
    return {"downloaded": downloaded_count, "total": len(videos)}


@op(
    name="filter_all_videos",
    description="Deduplicate videos for all goals"
)
def filter_all_videos_op(context, config: MongoConfig, download_result: Dict) -> Dict[str, Any]:
    """
    Deduplicate videos for all goals using OpenCV analysis.
    """
    import cv2
    import numpy as np
    import tempfile
    from pathlib import Path
    from found_footy.storage.s3_store import S3Store
    import hashlib
    
    client = MongoClient(config.mongo_uri)
    db = client[config.db_name]
    s3 = S3Store()
    
    # Get all goals that need filtering
    goals = db.goals.find({
        "processing_status.twitter_scraped": True,
        "processing_status.videos_downloaded": True,
        "processing_status.videos_filtered": False
    })
    
    goals_processed = 0
    total_kept = 0
    total_removed = 0
    
    for goal in goals:
        goal_id = goal["_id"]
        
        # Get all videos for this goal
        videos = list(db.videos.find({
            "goal_id": goal_id,
            "download_status": "completed",
            "s3_key": {"$exists": True}
        }))
        
        if len(videos) <= 1:
            # No deduplication needed
            db.goals.update_one(
                {"_id": goal_id},
                {"$set": {
                    "processing_status.videos_filtered": True,
                    "processing_status.completed": True
                }}
            )
            total_kept += len(videos)
            goals_processed += 1
            continue
        
        context.log.info(f"Deduplicating {len(videos)} videos for goal {goal_id}")
        
        # Download all videos locally for comparison
        video_data = []
        with tempfile.TemporaryDirectory() as tmpdir:
            for video in videos:
                local_path = Path(tmpdir) / f"{video['_id']}.mp4"
                s3.download_file(video["s3_key"], str(local_path))
                
                # Get file size
                file_size = local_path.stat().st_size
                
                # Compute hash
                with open(local_path, 'rb') as f:
                    file_hash = hashlib.sha256(f.read()).hexdigest()
                
                video_data.append({
                    "id": str(video["_id"]),
                    "path": str(local_path),
                    "size": file_size,
                    "hash": file_hash
                })
            
            # Level 1: Remove exact duplicates by hash
            unique_by_hash = {}
            for vid in video_data:
                if vid["hash"] not in unique_by_hash:
                    unique_by_hash[vid["hash"]] = vid
            
            remaining = list(unique_by_hash.values())
            context.log.info(f"After hash dedup: {len(remaining)}/{len(videos)} videos remain")
            
            # Level 2: OpenCV similarity comparison
            if len(remaining) > 1:
                to_keep = [remaining[0]]
                
                for candidate in remaining[1:]:
                    is_duplicate = False
                    
                    for keeper in to_keep:
                        try:
                            cap1 = cv2.VideoCapture(keeper["path"])
                            cap2 = cv2.VideoCapture(candidate["path"])
                            
                            frames1, frames2 = [], []
                            
                            for _ in range(10):
                                ret1, frame1 = cap1.read()
                                ret2, frame2 = cap2.read()
                                
                                if ret1 and ret2:
                                    frames1.append(frame1)
                                    frames2.append(frame2)
                            
                            cap1.release()
                            cap2.release()
                            
                            if len(frames1) > 0 and len(frames2) > 0:
                                similarities = []
                                for f1, f2 in zip(frames1, frames2):
                                    f1_resized = cv2.resize(f1, (320, 240))
                                    f2_resized = cv2.resize(f2, (320, 240))
                                    
                                    hist1 = cv2.calcHist([f1_resized], [0, 1, 2], None, [8, 8, 8], [0, 256] * 3)
                                    hist2 = cv2.calcHist([f2_resized], [0, 1, 2], None, [8, 8, 8], [0, 256] * 3)
                                    
                                    similarity = cv2.compareHist(hist1, hist2, cv2.HISTCMP_CORREL)
                                    similarities.append(similarity)
                                
                                avg_similarity = np.mean(similarities)
                                
                                if avg_similarity > 0.95:
                                    is_duplicate = True
                                    break
                        
                        except Exception as e:
                            context.log.error(f"Failed to compare videos: {e}")
                    
                    if not is_duplicate:
                        to_keep.append(candidate)
                
                remaining = to_keep
            
            # Mark duplicates
            kept_ids = {vid["id"] for vid in remaining}
            removed_count = 0
            
            for video in videos:
                video_id = str(video["_id"])
                if video_id not in kept_ids:
                    db.videos.update_one(
                        {"_id": video["_id"]},
                        {"$set": {"duplicate": True, "filtered_at": datetime.utcnow()}}
                    )
                    removed_count += 1
        
        # Mark goal as filtered and completed
        db.goals.update_one(
            {"_id": goal_id},
            {"$set": {
                "processing_status.videos_filtered": True,
                "processing_status.completed": True
            }}
        )
        
        total_kept += len(remaining)
        total_removed += removed_count
        goals_processed += 1
        context.log.info(f"Goal {goal_id}: kept {len(remaining)}, removed {removed_count}")
    
    client.close()
    context.log.info(f"Processed {goals_processed} goals, kept {total_kept} videos, removed {total_removed} duplicates")
    
    return {
        "goals_processed": goals_processed,
        "videos_kept": total_kept,
        "videos_removed": total_removed
    }


@job(
    name="goal_processing_pipeline",
    description="Complete sequential pipeline: monitor+process+scrape → download → filter"
)
def goal_processing_pipeline():
    """
    Main pipeline that processes all goals sequentially.
    
    This version avoids Dagster's "nested dynamics" limitation by running
    all work sequentially in three ops without any .map() calls.
    
    Flow:
    1. Monitor fixtures + process goals + scrape Twitter (all goals)
    2. Download all pending videos
    3. Filter/deduplicate videos for all goals
    """
    summary = monitor_process_and_scrape_op()
    downloads = download_all_videos_op(summary)
    filter_all_videos_op(downloads)
