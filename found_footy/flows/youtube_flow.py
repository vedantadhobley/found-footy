from datetime import datetime
from prefect import flow, task, get_run_logger
import requests
import time
import os
from found_footy.api import get_fixture_details

@task(name="store-match-results")
def store_match_results(fixture_data):
    """Store final match results in database"""
    logger = get_run_logger()
    logger.info(f"💾 Storing results for fixture {fixture_data['fixture']['id']}")
    # Add your database storage logic here
    return {"stored": True, "fixture_id": fixture_data['fixture']['id']}

@task(name="search-youtube")
def search_youtube_highlights(fixture_data):
    """Search YouTube for match highlights"""
    logger = get_run_logger()
    home_team = fixture_data["teams"]["home"]["name"]
    away_team = fixture_data["teams"]["away"]["name"]
    league = fixture_data["league"]["name"]
    fixture_id = fixture_data["fixture"]["id"]
    
    # Build search query
    search_query = f"{home_team} vs {away_team} highlights {league}"
    logger.info(f"🔍 Searching YouTube for: {search_query}")
    
    # TODO: Implement actual YouTube API search
    # You'll need to add YouTube API credentials and implementation
    # For now, this is a placeholder
    
    # Example structure:
    # youtube_api_key = "YOUR_YOUTUBE_API_KEY"
    # youtube_url = f"https://www.googleapis.com/youtube/v3/search"
    # params = {
    #     "part": "snippet",
    #     "q": search_query,
    #     "key": youtube_api_key,
    #     "type": "video",
    #     "maxResults": 10,
    #     "order": "relevance"
    # }
    # response = requests.get(youtube_url, params=params)
    # return response.json()
    
    return {
        "fixture_id": fixture_id,
        "search_query": search_query,
        "videos_found": [
            {
                "video_id": f"video_{fixture_id}_1",
                "title": f"{home_team} vs {away_team} - All Goals & Highlights",
                "channel": "Official Channel",
                "duration": "PT8M30S",
                "url": f"https://youtube.com/watch?v=video_{fixture_id}_1"
            },
            {
                "video_id": f"video_{fixture_id}_2", 
                "title": f"{home_team} vs {away_team} - Extended Highlights",
                "channel": "League Official",
                "duration": "PT12M15S",
                "url": f"https://youtube.com/watch?v=video_{fixture_id}_2"
            }
        ]
    }

# @task(name="monitor-youtube", retries=20, retry_delay_seconds=600)
@task(name="monitor-youtube")
def monitor_youtube(fixture_id, search_results):
    """Monitor YouTube to check if match highlights video is available"""
    logger = get_run_logger()
    logger.info(f"👀 Monitoring YouTube for highlights of fixture {fixture_id}")
    
    # Check if we have any videos from the search
    videos = search_results.get("videos_found", [])
    
    if not videos:
        logger.info(f"❌ No highlight videos found yet for fixture {fixture_id}, will retry in 10 minutes...")
        raise Exception(f"No highlights available yet for fixture {fixture_id}")
    
    # Check if videos are actually available (not just search results)
    available_videos = []
    for video in videos:
        video_id = video.get("video_id")
        video_url = video.get("url")
        
        logger.info(f"🔗 Checking availability of video: {video['title']}")
        
        try:
            # For testing/demo purposes, make videos available after a certain time
            # In real implementation, you would check actual YouTube API or URL accessibility
            
            # Simulate that videos become available over time
            # Use a deterministic approach based on fixture_id for testing
            import hashlib
            
            # Create a deterministic "availability" based on fixture_id
            hash_value = int(hashlib.md5(f"{fixture_id}_{video_id}".encode()).hexdigest()[:8], 16)
            
            # Videos become "available" based on hash modulo (makes it deterministic)
            # This simulates videos becoming available over time
            if hash_value % 3 == 0:  # 33% chance, but deterministic
                available_videos.append(video)
                logger.info(f"✅ Video available: {video['title']}")
            else:
                logger.warning(f"⚠️ Video not accessible yet: {video['title']}")
                
        except Exception as e:
            logger.warning(f"⚠️ Could not check video {video['title']}: {e}")
    
    # If no videos are available yet, but we have search results, keep retrying
    if not available_videos and videos:
        logger.info(f"⏳ No accessible highlight videos found for fixture {fixture_id}, retrying...")
        raise Exception(f"No accessible highlights available for fixture {fixture_id}")
    
    # If we still have no videos at all, return empty result instead of failing
    if not available_videos:
        logger.warning(f"⚠️ No videos found for fixture {fixture_id} after monitoring")
        return {
            "fixture_id": fixture_id,
            "available_videos": [],
            "total_found": 0,
            "status": "no_videos_found"
        }
    
    logger.info(f"🎬 Found {len(available_videos)} available highlight videos for fixture {fixture_id}")
    return {
        "fixture_id": fixture_id,
        "available_videos": available_videos,
        "total_found": len(available_videos),
        "status": "videos_found"
    }

@task(name="download-youtube")
def download_youtube(fixture_id, available_videos):
    """Download YouTube highlight videos"""
    logger = get_run_logger()
    
    # Handle case where no videos are available
    if available_videos.get("status") == "no_videos_found" or not available_videos.get("available_videos"):
        logger.warning(f"📥 No videos to download for fixture {fixture_id}")
        return {
            "fixture_id": fixture_id,
            "downloaded_files": [],
            "successful_downloads": 0,
            "total_videos": 0,
            "download_dir": None,
            "status": "no_videos_to_download"
        }
    
    videos = available_videos["available_videos"]
    logger.info(f"📥 Starting download of {len(videos)} videos for fixture {fixture_id}")
    
    downloaded_files = []
    download_dir = f"/app/downloads/fixture_{fixture_id}"
    
    # Create download directory
    os.makedirs(download_dir, exist_ok=True)
    
    for i, video in enumerate(videos):
        video_id = video.get("video_id")
        video_title = video.get("title", f"highlight_{i}")
        video_url = video.get("url")
        
        # Sanitize filename
        safe_filename = "".join(c for c in video_title if c.isalnum() or c in (' ', '-', '_')).rstrip()
        safe_filename = safe_filename.replace(' ', '_')
        file_path = f"{download_dir}/{safe_filename}_{video_id}.mp4"
        
        logger.info(f"📹 Downloading: {video_title}")
        
        try:
            # TODO: Implement actual YouTube download
            # You can use yt-dlp or similar library
            # For now, this is a placeholder
            
            # Example using yt-dlp (you'd need to add it to requirements.txt):
            # import yt_dlp
            # ydl_opts = {
            #     'outtmpl': file_path,
            #     'format': 'best[height<=720]',  # Download up to 720p
            # }
            # with yt_dlp.YoutubeDL(ydl_opts) as ydl:
            #     ydl.download([video_url])
            
            # Placeholder: create a dummy file to simulate download
            with open(file_path, 'w') as f:
                f.write(f"Placeholder download for video: {video_title}\nURL: {video_url}\nDownloaded at: {datetime.now()}")
            
            downloaded_files.append({
                "video_id": video_id,
                "title": video_title,
                "file_path": file_path,
                "url": video_url,
                "status": "downloaded",
                "file_size": os.path.getsize(file_path)
            })
            
            logger.info(f"✅ Downloaded: {video_title} -> {file_path}")
            
        except Exception as e:
            logger.error(f"❌ Failed to download {video_title}: {e}")
            downloaded_files.append({
                "video_id": video_id,
                "title": video_title,
                "file_path": None,
                "url": video_url,
                "status": "failed",
                "error": str(e)
            })
    
    successful_downloads = len([f for f in downloaded_files if f["status"] == "downloaded"])
    logger.info(f"📊 Download summary for fixture {fixture_id}: {successful_downloads}/{len(videos)} successful")
    
    return {
        "fixture_id": fixture_id,
        "downloaded_files": downloaded_files,
        "successful_downloads": successful_downloads,
        "total_videos": len(videos),
        "download_dir": download_dir,
        "status": "completed" if successful_downloads > 0 else "failed"
    }

@task(name="filter-highlights")
def filter_highlights(youtube_results, fixture_id):
    """Filter YouTube results for actual highlights and store them"""
    logger = get_run_logger()
    logger.info(f"🔍 Processing and filtering highlights for fixture {fixture_id}")
    
    # TODO: Implement filtering logic
    # - Filter by video duration (highlights are usually 3-15 minutes)
    # - Check channel credibility
    # - Look for keywords like "highlights", "goals", "extended"
    # - Store in database
    
    videos = youtube_results.get("videos_found", [])
    filtered_videos = []
    
    for video in videos:
        duration = video.get("duration", "PT0S")
        title = video.get("title", "").lower()
        
        # Simple filtering criteria
        if ("highlights" in title or "goals" in title) and "PT" in duration:
            filtered_videos.append(video)
            logger.info(f"✅ Filtered video accepted: {video['title']}")
        else:
            logger.info(f"❌ Filtered video rejected: {video['title']}")
    
    logger.info(f"🎯 Filtered {len(filtered_videos)} videos from {len(videos)} for fixture {fixture_id}")
    
    return {
        "fixture_id": fixture_id,
        "original_count": len(videos),
        "filtered_count": len(filtered_videos),
        "filtered_videos": filtered_videos
    }

def process_highlights_for_fixture(fixture_id):
    """Process highlights for a specific fixture - can be called from tasks"""
    # Your existing post-match processing logic here
    # This should contain the core logic without the @flow decorator
    print(f"Processing highlights for fixture {fixture_id}")
    # Return some result
    return {"processed": True, "fixture_id": fixture_id}

@flow(name="youtube-flow")
def youtube_flow(fixture_id):
    """Comprehensive YouTube highlights processing flow"""
    logger = get_run_logger()
    logger.info(f"🎬 Starting YouTube highlights processing for fixture {fixture_id}")
    
    # Get detailed fixture data
    detailed_data = get_fixture_details(fixture_id)
    
    if not detailed_data:
        logger.error(f"❌ No data found for fixture {fixture_id}")
        return {"error": "No fixture data", "fixture_id": fixture_id}
    
    fixture_data = detailed_data[0]
    
    # Log match result
    home_team = fixture_data["teams"]["home"]["name"]
    away_team = fixture_data["teams"]["away"]["name"]
    home_goals = fixture_data["goals"]["home"]
    away_goals = fixture_data["goals"]["away"]
    
    logger.info(f"⚽ Match Result: {home_team} {home_goals} - {away_goals} {away_team}")
    
    # Step 1: Store match results (async)
    store_task = store_match_results.with_options(
        task_run_name=f"store-{fixture_id}"
    ).submit(fixture_data)
    
    # Step 2: Search YouTube for highlights
    search_results = search_youtube_highlights.with_options(
        task_run_name=f"search-{fixture_id}"
    )(fixture_data)
    
    # Step 3: Monitor YouTube for available videos (this will retry until videos are found)
    available_videos_result = monitor_youtube.with_options(
        task_run_name=f"monitor-youtube-{fixture_id}"
    )(fixture_id, search_results)
    
    # Step 4: Download the available videos
    download_result = download_youtube.with_options(
        task_run_name=f"download-{fixture_id}"
    )(fixture_id, available_videos_result)
    
    # Step 5: Filter and process highlights
    filtered_result = filter_highlights.with_options(
        task_run_name=f"filter-{fixture_id}"
    )(search_results, fixture_id)
    
    # Wait for storage task to complete
    store_result = store_task.result()
    
    logger.info(f"🎉 YouTube processing completed for fixture {fixture_id}")
    
    return {
        "fixture_id": fixture_id,
        "match_result": f"{home_team} {home_goals} - {away_goals} {away_team}",
        "store_result": store_result,
        "search_results": search_results,
        "monitoring_result": available_videos_result,
        "download_result": download_result,
        "filtered_result": filtered_result,
        "status": "completed"
    }