"""Video object methods on event _s3_videos arrays."""

import os
from datetime import datetime, timezone
from typing import Any, Dict, List, Optional

from pymongo import ASCENDING, MongoClient, ReturnDocument
from pymongo.errors import DuplicateKeyError

from src.data.models import (
    FixtureFields,
    EventFields,
    FixtureStatus,
    TeamAliasFields,
    create_activation_fields,
)
from src.data._helpers import _log_info, _log_warning, _log_error
from src.utils.footy_logging import log, get_fallback_logger


class VideosMixin:
    """Mixin: video add / rank recalculation / popularity bump."""

    def add_videos_to_event(
        self,
        fixture_id: int,
        event_id: str,
        video_objects: List[Dict[str, Any]]
    ) -> bool:
        """
        Add video objects to _s3_videos array.
        Deduplicates by URL to prevent duplicate entries.
        If a video already exists, updates its popularity to the MAX of existing and new.
        
        Works on BOTH fixtures_active AND fixtures_completed to handle the race condition
        where fixture moves to completed while downloads are still running.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            video_objects: List of {url, perceptual_hash, resolution_score, file_size, popularity, rank}
        
        Returns:
            True if successful
        """
        # Try fixtures_active first, then fixtures_completed
        for collection_name, collection in [
            ("active", self.fixtures_active),
            ("completed", self.fixtures_completed)
        ]:
            try:
                if not video_objects:
                    # Verify event exists
                    fixture = collection.find_one(
                        {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id}
                    )
                    if not fixture:
                        continue  # Try next collection
                    _log_info("no_new_videos", "No new videos to add", event_id=event_id, collection=collection_name)
                    return True
                
                # Get existing videos to check for duplicates
                fixture = collection.find_one(
                    {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id}
                )
                if not fixture:
                    continue  # Try next collection
                
                _log_info("fixture_found", "Found fixture for event", event_id=event_id, collection=collection_name)
                
                # Find the event and build URL -> existing video map
                existing_videos_by_url = {}
                event_idx = None
                for idx, evt in enumerate(fixture.get("events", [])):
                    if evt.get(EventFields.EVENT_ID) == event_id:
                        event_idx = idx
                        for video in evt.get(EventFields.S3_VIDEOS, []):
                            url = video.get("url", "")
                            if url:
                                existing_videos_by_url[url] = video
                        break
                
                # Separate new videos from duplicates that need popularity updates
                new_videos = []
                popularity_updates = []  # List of (url, new_popularity)
                
                for v in video_objects:
                    url = v.get("url", "")
                    new_pop = v.get("popularity", 1)
                    
                    if url in existing_videos_by_url:
                        # Video already exists - check if we should bump popularity
                        existing_pop = existing_videos_by_url[url].get("popularity", 1)
                        if new_pop > existing_pop:
                            popularity_updates.append((url, new_pop))
                            _log_info("popularity_bump", "Existing video popularity bump", 
                                     video=url.split('/')[-1], old_pop=existing_pop, new_pop=new_pop)
                    else:
                        new_videos.append(v)
                
                # Apply popularity updates to existing videos
                if popularity_updates:
                    # Get current videos array
                    for evt in fixture.get("events", []):
                        if evt.get(EventFields.EVENT_ID) == event_id:
                            videos = evt.get(EventFields.S3_VIDEOS, [])
                            # Update popularity for matching URLs
                            for video in videos:
                                for url, new_pop in popularity_updates:
                                    if video.get("url") == url:
                                        video["popularity"] = new_pop
                            # Save back
                            collection.update_one(
                                {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                                {"$set": {f"events.$.{EventFields.S3_VIDEOS}": videos}}
                            )
                            _log_info("popularity_updated", "Updated popularity for existing videos",
                                     event_id=event_id, count=len(popularity_updates), collection=collection_name)
                            break
                
                if not new_videos:
                    if popularity_updates:
                        _log_info("videos_updated_no_new", "Updated existing videos, no new videos",
                                 event_id=event_id, updated_count=len(popularity_updates))
                    else:
                        _log_info("videos_all_exist", "All videos already exist, skipping duplicates",
                                 event_id=event_id, video_count=len(video_objects))
                    return True
                
                if len(new_videos) < len(video_objects):
                    skipped = len(video_objects) - len(new_videos) - len(popularity_updates)
                    if skipped > 0:
                        _log_warning("duplicate_videos_filtered", "Filtered out duplicate videos", 
                                    event_id=event_id, skipped=skipped)
                
                # Append new video objects
                result = collection.update_one(
                    {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                    {
                        "$push": {
                            f"events.$.{EventFields.S3_VIDEOS}": {"$each": new_videos}
                        }
                    }
                )
                
                if result.modified_count == 0:
                    _log_warning("no_docs_modified", "0 documents modified when adding videos",
                                event_id=event_id, collection=collection_name)
                    continue  # Try next collection
                
                _log_info("videos_added", "Added videos to event", 
                         event_id=event_id, count=len(new_videos), collection=collection_name)
                return True
                
            except Exception as e:
                _log_warning("add_videos_error", "Error adding videos", 
                            event_id=event_id, collection=collection_name, error=str(e))
                continue
        
        # Not found in either collection
        _log_error("event_not_found", "Event not found in fixtures_active or fixtures_completed", event_id=event_id)
        raise RuntimeError(f"Event {event_id} not found in fixtures_active or fixtures_completed")

    def recalculate_video_ranks(self, fixture_id: int, event_id: str) -> bool:
        """
        Recalculate ranks for all videos in an event.
        Sorts by popularity (desc) then file_size (desc) - larger files = better quality.
        Rank 1 = best video.
        
        Only checks fixtures_active since _download_complete is only set after ALL
        batches are processed by UploadWorkflow (on idle timeout). This ensures
        ranking is complete before fixture can move to completed.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            
        Returns:
            True if successful
        """
        try:
            fixture = self.fixtures_active.find_one({"_id": fixture_id})
            if not fixture:
                _log_warning("fixture_not_found", "Fixture not found in fixtures_active", fixture_id=fixture_id)
                return False
            
            # Find the event
            event = None
            event_idx = None
            for idx, evt in enumerate(fixture.get("events", [])):
                if evt.get(EventFields.EVENT_ID) == event_id:
                    event = evt
                    event_idx = idx
                    break
            
            if event is None:
                _log_warning("event_not_found", "Event not found", event_id=event_id)
                return False
            
            # Get videos and sort them
            videos = event.get(EventFields.S3_VIDEOS, [])
            if not videos:
                return True
            
            # Sort by: verified first, then popularity (desc), then file_size (desc)
            # Verified videos always rank above unverified — they're confirmed correct minute
            videos_sorted = sorted(
                videos, 
                key=lambda v: (
                    v.get("timestamp_verified", False),  # Verified > unverified
                    v.get("popularity", 1),
                    v.get("file_size", 0),
                ),
                reverse=True
            )
            
            # Assign sequential ranks (1 = best)
            for rank, video in enumerate(videos_sorted, start=1):
                video["rank"] = rank
            
            # Update in fixtures_active
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                {"$set": {f"events.$.{EventFields.S3_VIDEOS}": videos_sorted}}
            )
            
            _log_info("ranks_recalculated", "Recalculated ranks for videos", event_id=event_id, count=len(videos))
            return True
        except Exception as e:
            _log_error("recalculate_ranks_error", "Error recalculating video ranks", error=str(e), exc=e)
            return False

    def update_video_popularity(
        self,
        fixture_id: int,
        event_id: str,
        s3_url: str,
        new_popularity: int
    ) -> bool:
        """
        Update popularity for a specific video and recalculate ranks.
        Called when we find a duplicate of an existing video.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            s3_url: URL of video to update
            new_popularity: New popularity value
            
        Returns:
            True if successful
        """
        try:
            # Update the specific video's popularity
            result = self.fixtures_active.update_one(
                {
                    "_id": fixture_id,
                    "events._event_id": event_id,
                    "events._s3_videos.url": s3_url
                },
                {
                    "$set": {"events.$[evt]._s3_videos.$[vid].popularity": new_popularity}
                },
                array_filters=[
                    {"evt._event_id": event_id},
                    {"vid.url": s3_url}
                ]
            )
            
            if result.modified_count > 0:
                _log_info("popularity_updated", "Updated popularity for video", s3_url=s3_url, new_popularity=new_popularity)
                # Recalculate ranks
                self.recalculate_video_ranks(fixture_id, event_id)
                return True
            else:
                _log_warning("video_not_found_popularity", "Video not found for popularity update", s3_url=s3_url)
                return False
        except Exception as e:
            _log_error("update_popularity_error", "Error updating video popularity", error=str(e), exc=e)
            return False
