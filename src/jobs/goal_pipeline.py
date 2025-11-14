"""Goal Pipeline - Complete goal processing from detection to filtered videos

This matches your Prefect flow structure exactly:
- goal_flow → stores goals
- twitter_flow → searches Twitter for each goal
- download_flow → downloads videos for each goal  
- filter_flow → deduplicates videos for each goal

Each op processes ALL pending work for its stage (like Prefect flows).
No nested dynamics - just sequential processing with clear stages.
"""

from dagster import job, op, Config
from typing import Dict, Any, List
from pymongo import MongoClient
from bson import ObjectId
from datetime import datetime
import subprocess
import tempfile
from pathlib import Path
import cv2
import numpy as np
import hashlib


class GoalPipelineConfig(Config):
    """Configuration for goal pipeline."""
    fixture_id: str
    goal_events: List[Dict[str, Any]]


class MongoConfig(Config):
    """MongoDB connection configuration."""
    mongo_uri: str = "mongodb://localhost:27017"
    db_name: str = "found_footy"


@op(
    name="process_goals",
    description="Store goals in MongoDB (matches goal_flow.py)"
)
def process_goals_op(context, config: MongoConfig, fixture_id: str, goal_events: List[Dict]) -> Dict[str, Any]:
    """
    Process goal events for a fixture - matches goal_flow.py from Prefect.
    Stores goals in MongoDB and returns count of new goals.
    """
    client = MongoClient(config.mongo_uri)
    db = client[config.db_name]
    
    # Get fixture data
    fixture = db.fixtures.find_one({"_id": ObjectId(fixture_id)})
    if not fixture:
        context.log.error(f"Fixture {fixture_id} not found")
        client.close()
        return {"new_goals": 0, "updated_goals": 0}
    
    home_team = fixture["teams"]["home"]["name"]
    away_team = fixture["teams"]["away"]["name"]
    
    context.log.info(f"Processing {len(goal_events)} goals for {home_team} vs {away_team}")
    
    new_goal_ids = []
    updated_goal_ids = []
    
    for goal_event in goal_events:
        # Check if goal exists
        existing = db.goals.find_one({
            "fixture_id": ObjectId(fixture_id),
            "time.elapsed": goal_event["time"]["elapsed"]
        })
        
        if not existing:
            # NEW GOAL - store it
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
            new_goal_ids.append(goal_id)
            
            player_name = goal_event["player"]["name"]
            minute = goal_event["time"]["elapsed"]
            context.log.info(f"🆕 NEW GOAL: {player_name} ({minute}') [{goal_id}]")
        
        else:
            # EXISTING GOAL - just update
            goal_id = str(existing["_id"])
            db.goals.update_one(
                {"_id": existing["_id"]},
                {"$set": goal_event}
            )
            updated_goal_ids.append(goal_id)
            context.log.info(f"🔄 UPDATED: {goal_id}")
    
    client.close()
    
    context.log.info(f"✅ {len(new_goal_ids)} new, {len(updated_goal_ids)} updated")
    
    return {
        "new_goals": len(new_goal_ids),
        "updated_goals": len(updated_goal_ids),
        "new_goal_ids": new_goal_ids,
        "updated_goal_ids": updated_goal_ids
    }


@op(
    name="scrape_twitter",
    description="Search Twitter for all goals needing scraping (matches twitter_flow.py)"
)
def scrape_twitter_op(context, config: MongoConfig, process_result: Dict) -> Dict[str, Any]:
    """
    Search Twitter for ALL goals that need scraping - matches twitter_flow.py.
    """
    from found_footy.services.twitter_session_isolated import TwitterSessionManager
    
    client = MongoClient(config.mongo_uri)
    db = client[config.db_name]
    
    # Find all goals needing Twitter scraping
    goals = db.goals.find({"processing_status.twitter_scraped": False})
    
    twitter = TwitterSessionManager()
    goals_scraped = 0
    total_videos = 0
    
    for goal in goals:
        goal_id = str(goal["_id"])
        
        # Get fixture for query building
        fixture = db.fixtures.find_one({"_id": goal["fixture_id"]})
        if not fixture:
            continue
        
        home_team = fixture["teams"]["home"]["name"]
        away_team = fixture["teams"]["away"]["name"]
        player_name = goal["player"]["name"]
        
        # Build query (player last name + teams)
        player_last_name = player_name.split()[-1] if " " in player_name else player_name
        query = f"{player_last_name} {home_team} {away_team}"
        
        context.log.info(f"Searching Twitter for: {query}")
        
        try:
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
            
            goals_scraped += 1
            total_videos += len(videos)
            context.log.info(f"Found {len(videos)} videos for {goal_id}")
        
        except Exception as e:
            context.log.error(f"Twitter search failed for {goal_id}: {e}")
    
    client.close()
    
    context.log.info(f"✅ Scraped {goals_scraped} goals, found {total_videos} videos")
    
    return {
        "goals_scraped": goals_scraped,
        "total_videos": total_videos
    }


@op(
    name="download_and_upload_videos",
    description="Download and upload ALL pending videos (matches download_flow.py)"
)
def download_and_upload_videos_op(context, config: MongoConfig, scrape_result: Dict) -> Dict[str, Any]:
    """
    Download ALL pending videos and upload to S3 - matches download_flow.py.
    """
    from found_footy.storage.s3_store import S3Store
    
    client = MongoClient(config.mongo_uri)
    db = client[config.db_name]
    s3 = S3Store()
    
    # Get ALL pending videos
    videos = list(db.videos.find({"download_status": "pending"}))
    
    context.log.info(f"Downloading {len(videos)} videos")
    
    downloaded_count = 0
    failed_count = 0
    
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
                            "uploaded_at": datetime.utcnow()
                        }}
                    )
                    
                    downloaded_count += 1
                    context.log.info(f"Downloaded {video_id}")
                else:
                    db.videos.update_one(
                        {"_id": video["_id"]},
                        {"$set": {"download_status": "failed"}}
                    )
                    failed_count += 1
        
        except Exception as e:
            context.log.error(f"Failed to download {video_id}: {e}")
            db.videos.update_one(
                {"_id": video["_id"]},
                {"$set": {"download_status": "failed"}}
            )
            failed_count += 1
    
    # Update goal statuses
    goals_with_downloads = db.videos.distinct("goal_id", {"download_status": "completed"})
    for goal_id in goals_with_downloads:
        db.goals.update_one(
            {"_id": goal_id},
            {"$set": {"processing_status.videos_downloaded": True}}
        )
    
    client.close()
    
    context.log.info(f"✅ Downloaded {downloaded_count}, failed {failed_count}")
    
    return {
        "downloaded": downloaded_count,
        "failed": failed_count
    }


@op(
    name="filter_videos",
    description="Deduplicate videos for ALL goals (matches filter_flow.py)"
)
def filter_videos_op(context, config: MongoConfig, download_result: Dict) -> Dict[str, Any]:
    """
    Deduplicate videos for ALL goals - matches filter_flow.py.
    """
    from found_footy.storage.s3_store import S3Store
    
    client = MongoClient(config.mongo_uri)
    db = client[config.db_name]
    s3 = S3Store()
    
    # Get all goals needing filtering
    goals = db.goals.find({
        "processing_status.videos_downloaded": True,
        "processing_status.videos_filtered": False
    })
    
    goals_filtered = 0
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
            goals_filtered += 1
            continue
        
        context.log.info(f"Deduplicating {len(videos)} videos for {goal_id}")
        
        # Download for comparison
        video_data = []
        with tempfile.TemporaryDirectory() as tmpdir:
            for video in videos:
                local_path = Path(tmpdir) / f"{video['_id']}.mp4"
                s3.download_file(video["s3_key"], str(local_path))
                
                file_size = local_path.stat().st_size
                with open(local_path, 'rb') as f:
                    file_hash = hashlib.sha256(f.read()).hexdigest()
                
                video_data.append({
                    "id": str(video["_id"]),
                    "path": str(local_path),
                    "hash": file_hash
                })
            
            # Hash dedup
            unique_by_hash = {}
            for vid in video_data:
                if vid["hash"] not in unique_by_hash:
                    unique_by_hash[vid["hash"]] = vid
            
            remaining = list(unique_by_hash.values())
            
            # OpenCV dedup
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
                            
                            if frames1 and frames2:
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
                            context.log.error(f"Comparison failed: {e}")
                    
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
                        {"$set": {"duplicate": True}}
                    )
                    removed_count += 1
        
        # Mark goal as complete
        db.goals.update_one(
            {"_id": goal_id},
            {"$set": {
                "processing_status.videos_filtered": True,
                "processing_status.completed": True
            }}
        )
        
        total_kept += len(remaining)
        total_removed += removed_count
        goals_filtered += 1
    
    client.close()
    
    context.log.info(f"✅ Filtered {goals_filtered} goals: kept {total_kept}, removed {total_removed}")
    
    return {
        "goals_filtered": goals_filtered,
        "videos_kept": total_kept,
        "videos_removed": total_removed
    }


@job(
    name="goal_pipeline",
    description="Complete goal pipeline: process → scrape → download → filter (matches Prefect flows)"
)
def goal_pipeline_job():
    """
    Main pipeline with 4 ops, matching your Prefect flow structure:
    
    1. process_goals - Store goals (goal_flow.py)
    2. scrape_twitter - Search Twitter for all goals (twitter_flow.py)
    3. download_and_upload_videos - Download all videos (download_flow.py)
    4. filter_videos - Deduplicate all videos (filter_flow.py)
    
    No nested dynamics - each op processes ALL pending work for its stage.
    """
    process_result = process_goals_op()
    scrape_result = scrape_twitter_op(process_result)
    download_result = download_and_upload_videos_op(scrape_result)
    filter_videos_op(download_result)
