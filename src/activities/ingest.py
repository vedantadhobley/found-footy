"""Ingest activities"""
from temporalio import activity
from typing import Dict, List, Any
from datetime import date, datetime, timedelta, timezone


@activity.defn
async def fetch_todays_fixtures(target_date_str: str | None = None) -> List[Dict[str, Any]]:
    """
    Fetch fixtures for the given date (defaults to today).
    Filters to only tracked teams (50 teams: 25 UEFA + 25 FIFA).
    
    Args:
        target_date_str: ISO format date string (e.g., "2025-12-26") or None for today
    """
    if target_date_str:
        target_date = date.fromisoformat(target_date_str)
    else:
        target_date = date.today()
    
    activity.logger.info(f"🌐 Fetching fixtures for {target_date}")
    
    try:
        # Import here to avoid circular imports
        from src.api.api_client import get_fixtures_for_date
        from src.utils.team_data import get_team_ids
        
        # Get all fixtures for the date
        all_fixtures = get_fixtures_for_date(target_date)
        activity.logger.info(f"📥 Retrieved {len(all_fixtures)} total fixtures from API")
        
        # Filter to only our tracked teams
        tracked_team_ids = set(get_team_ids())
        
        filtered_fixtures = [
            fixture for fixture in all_fixtures
            if fixture.get("teams", {}).get("home", {}).get("id") in tracked_team_ids
            or fixture.get("teams", {}).get("away", {}).get("id") in tracked_team_ids
        ]
        
        activity.logger.info(
            f"✅ Filtered to {len(filtered_fixtures)} fixtures for our {len(tracked_team_ids)} tracked teams "
            f"(removed {len(all_fixtures) - len(filtered_fixtures)} irrelevant fixtures)"
        )
        
        return filtered_fixtures
    
    except Exception as e:
        activity.logger.error(f"❌ Failed to fetch fixtures: {e}")
        raise


@activity.defn
async def fetch_fixtures_by_ids(fixture_ids: List[int]) -> List[Dict[str, Any]]:
    """
    Fetch specific fixtures by their IDs.
    Used for manual ingest of specific fixtures from Temporal UI.
    
    Args:
        fixture_ids: List of fixture IDs to fetch
        
    Returns:
        List of fixture objects from API-Football
    """
    if not fixture_ids:
        activity.logger.warning("⚠️ No fixture IDs provided")
        return []
    
    activity.logger.info(f"🌐 Fetching {len(fixture_ids)} specific fixtures: {fixture_ids}")
    
    try:
        from src.api.api_client import fixtures_batch
        
        # Fetch fixtures by IDs (API supports batch fetch)
        fixtures = fixtures_batch(fixture_ids)
        activity.logger.info(f"✅ Retrieved {len(fixtures)} fixtures by ID")
        
        # Log which fixtures were found
        for fixture in fixtures:
            fixture_id = fixture.get("fixture", {}).get("id")
            home = fixture.get("teams", {}).get("home", {}).get("name", "?")
            away = fixture.get("teams", {}).get("away", {}).get("name", "?")
            status = fixture.get("fixture", {}).get("status", {}).get("short", "?")
            activity.logger.info(f"  📋 {fixture_id}: {home} vs {away} ({status})")
        
        # Warn about any IDs that weren't found
        found_ids = {f.get("fixture", {}).get("id") for f in fixtures}
        missing_ids = set(fixture_ids) - found_ids
        if missing_ids:
            activity.logger.warning(f"⚠️ Could not find fixtures: {missing_ids}")
        
        return fixtures
    
    except Exception as e:
        activity.logger.error(f"❌ Failed to fetch fixtures by ID: {e}")
        raise


@activity.defn
async def categorize_and_store_fixtures(fixtures: List[Dict]) -> Dict[str, int]:
    """
    Categorize fixtures by status and store in appropriate collections.
    
    Routes to:
    - staging: TBD, NS (not started)
    - active: LIVE, 1H, HT, 2H, ET, P, BT, SUSP, INT, PST (in progress or delayed)
    - completed: FT, AET, PEN, CANC, ABD, AWD, WO (finished)
    
    NOTE: PST (Postponed) is treated as ACTIVE to handle short delays (15-30 min).
    """
    if not fixtures:
        activity.logger.warning("⚠️  No fixtures to categorize")
        return {"staging": 0, "active": 0, "completed": 0}
    
    try:
        from src.utils.fixture_status import (
            get_completed_statuses,
            get_active_statuses,
            get_staging_statuses,
        )
        from src.data.mongo_store import FootyMongoStore
        
        # Get status sets
        completed_statuses = set(get_completed_statuses())
        active_statuses = set(get_active_statuses())
        staging_statuses = set(get_staging_statuses())
        
        staging_fixtures = []
        active_fixtures = []
        completed_fixtures = []
        
        for fixture in fixtures:
            status = fixture.get("fixture", {}).get("status", {}).get("short", "")
            fixture_id = fixture.get("fixture", {}).get("id", "unknown")
            
            if not status:
                activity.logger.warning(f"⚠️  Fixture {fixture_id} has no status, defaulting to staging")
                staging_fixtures.append(fixture)
                continue
            
            # Route based on status
            if status in completed_statuses:
                completed_fixtures.append(fixture)
            elif status in active_statuses:
                active_fixtures.append(fixture)
            elif status in staging_statuses:
                staging_fixtures.append(fixture)
            else:
                activity.logger.warning(f"⚠️  Unknown status '{status}' for fixture {fixture_id}, defaulting to staging")
                staging_fixtures.append(fixture)
        
        activity.logger.info(
            f"📊 Categorized {len(fixtures)} fixtures: "
            f"{len(staging_fixtures)} staging, "
            f"{len(active_fixtures)} active, "
            f"{len(completed_fixtures)} completed"
        )
        
        if active_fixtures:
            activity.logger.info(f"🔥 {len(active_fixtures)} fixtures already LIVE - will catch goals immediately!")
        
        if completed_fixtures:
            activity.logger.info(f"🏁 {len(completed_fixtures)} fixtures already FINISHED - skip monitoring")
        
        # Store in collections
        store = FootyMongoStore()
        staging_count = store.bulk_insert_fixtures(staging_fixtures, "fixtures_staging") if staging_fixtures else 0
        active_count = store.bulk_insert_fixtures(active_fixtures, "fixtures_active") if active_fixtures else 0
        completed_count = store.bulk_insert_fixtures(completed_fixtures, "fixtures_completed") if completed_fixtures else 0
        
        activity.logger.info(f"✅ Stored fixtures: {staging_count} staging, {active_count} active, {completed_count} completed")
        
        return {
            "staging": staging_count,
            "active": active_count,
            "completed": completed_count,
        }
    
    except Exception as e:
        activity.logger.error(f"❌ Failed to categorize/store fixtures: {e}")
        raise


@activity.defn
async def cleanup_old_fixtures(retention_days: int = 14) -> Dict[str, Any]:
    """
    Delete fixtures older than retention_days from MongoDB and S3.
    
    The retention period is calculated from "yesterday" (the day before this runs),
    since ingestion runs at 00:05 UTC when today's fixtures haven't happened yet.
    
    Example: If run on Jan 16 with retention_days=14:
    - Today = Jan 16 (doesn't count - fixtures haven't happened)
    - Day 1 = Jan 15 (yesterday, keep)
    - Day 2 = Jan 14 (keep)
    - ...
    - Day 14 = Jan 2 (keep)
    - Day 15 = Jan 1 → DELETE
    
    Formula: cutoff = today - retention_days
    - cutoff = Jan 16 - 14 = Jan 2
    - Delete fixtures with date < Jan 2 (i.e., Jan 1 and earlier)
    - Keep fixtures from Jan 2 through Jan 15 (14 days)
    
    Args:
        retention_days: Number of days of fixtures to keep (default 14)
        
    Returns:
        Dict with counts of deleted fixtures and videos
    """
    from src.data.mongo_store import FootyMongoStore
    from src.data.s3_store import FootyS3Store
    
    # Calculate cutoff: today - retention_days
    # Example: Jan 16 - 14 = Jan 2
    # Delete fixtures with date < Jan 2 (keeps Jan 2 through Jan 15 = 14 days)
    cutoff_date = datetime.now(timezone.utc).replace(hour=0, minute=0, second=0, microsecond=0) - timedelta(days=retention_days)
    
    activity.logger.info(
        f"🧹 [CLEANUP] Starting old fixture cleanup | retention={retention_days} days | "
        f"cutoff={cutoff_date.strftime('%Y-%m-%d')}"
    )
    
    store = FootyMongoStore()
    s3_store = FootyS3Store()
    
    deleted_fixtures = 0
    deleted_videos = 0
    failed_s3_deletes = 0
    
    try:
        # Find all fixtures in fixtures_completed older than cutoff
        # The date is stored as ISO string in fixture.date
        old_fixtures = list(store.fixtures_completed.find({
            "fixture.date": {"$lt": cutoff_date.isoformat()}
        }))
        
        if not old_fixtures:
            activity.logger.info(f"✅ [CLEANUP] No fixtures older than {cutoff_date.strftime('%Y-%m-%d')} found")
            return {
                "cutoff_date": cutoff_date.isoformat(),
                "deleted_fixtures": 0,
                "deleted_videos": 0,
                "failed_s3_deletes": 0,
            }
        
        activity.logger.info(f"🗑️ [CLEANUP] Found {len(old_fixtures)} fixtures to delete")
        
        for fixture in old_fixtures:
            fixture_id = fixture.get("_id") or fixture.get("fixture", {}).get("id")
            fixture_date = fixture.get("fixture", {}).get("date", "unknown")
            home_team = fixture.get("teams", {}).get("home", {}).get("name", "?")
            away_team = fixture.get("teams", {}).get("away", {}).get("name", "?")
            
            # Count videos for logging
            fixture_video_count = 0
            for event in fixture.get("events", []):
                s3_videos = event.get("_s3_videos", [])
                fixture_video_count += len(s3_videos)
                
                # Delete each video from S3
                for video in s3_videos:
                    s3_key = video.get("_s3_key", "")
                    if s3_key:
                        try:
                            s3_store.s3_client.delete_object(
                                Bucket=s3_store.bucket_name, 
                                Key=s3_key
                            )
                            deleted_videos += 1
                        except Exception as e:
                            activity.logger.warning(
                                f"⚠️ [CLEANUP] Failed to delete S3 video | key={s3_key} | error={e}"
                            )
                            failed_s3_deletes += 1
            
            # Also delete any S3 objects with the fixture prefix (catch-all for orphaned files)
            try:
                prefix = f"{fixture_id}/"
                response = s3_store.s3_client.list_objects_v2(
                    Bucket=s3_store.bucket_name,
                    Prefix=prefix
                )
                for obj in response.get("Contents", []):
                    try:
                        s3_store.s3_client.delete_object(
                            Bucket=s3_store.bucket_name,
                            Key=obj["Key"]
                        )
                        # Only count if not already counted from _s3_videos
                        if "_s3_key" not in str(obj["Key"]):
                            deleted_videos += 1
                    except Exception as e:
                        activity.logger.warning(f"⚠️ [CLEANUP] Failed to delete orphan S3 | key={obj['Key']}")
                        failed_s3_deletes += 1
            except Exception as e:
                activity.logger.warning(f"⚠️ [CLEANUP] Failed to list S3 prefix {prefix}: {e}")
            
            # Delete fixture from MongoDB
            result = store.fixtures_completed.delete_one({"_id": fixture_id})
            if result.deleted_count > 0:
                deleted_fixtures += 1
                activity.logger.info(
                    f"🗑️ [CLEANUP] Deleted fixture | id={fixture_id} | "
                    f"date={fixture_date[:10] if fixture_date else '?'} | "
                    f"{home_team} vs {away_team} | videos={fixture_video_count}"
                )
        
        activity.logger.info(
            f"✅ [CLEANUP] Complete | deleted_fixtures={deleted_fixtures} | "
            f"deleted_videos={deleted_videos} | failed_s3={failed_s3_deletes}"
        )
        
        return {
            "cutoff_date": cutoff_date.isoformat(),
            "deleted_fixtures": deleted_fixtures,
            "deleted_videos": deleted_videos,
            "failed_s3_deletes": failed_s3_deletes,
        }
        
    except Exception as e:
        activity.logger.error(f"❌ [CLEANUP] Failed: {e}")
        raise
