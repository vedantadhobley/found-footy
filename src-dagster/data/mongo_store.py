import os
from datetime import datetime, timezone
from typing import Dict, List, Any

from pymongo import ASCENDING, MongoClient


class FootyMongoStore:
    """
    MongoDB storage for 4-collection architecture:
    - fixtures_staging: Waiting to activate (TBD, NS)
    - fixtures_live: Raw API data (temporary, for comparison)
    - fixtures_active: Enhanced with debounce data (never overwritten)
    - fixtures_completed: Archive (FT, AET, PEN)
    """
    
    def __init__(self, connection_url=None):
        if connection_url is None:
            connection_url = os.getenv('MONGODB_URI') or os.getenv('MONGODB_URL', 'mongodb://localhost:27017/')
        
        self.client = MongoClient(connection_url)
        self.db = self.client.found_footy
        
        # 4 Collections
        self.fixtures_staging = self.db.fixtures_staging
        self.fixtures_live = self.db.fixtures_live
        self.fixtures_active = self.db.fixtures_active
        self.fixtures_completed = self.db.fixtures_completed
        
        self._create_indexes()

    def _create_indexes(self):
        """Create indexes for all collections"""
        try:
            # Status indexes
            self.fixtures_staging.create_index([("fixture.status.short", ASCENDING)])
            self.fixtures_live.create_index([("fixture.status.short", ASCENDING)])
            self.fixtures_active.create_index([("fixture.status.short", ASCENDING)])
            self.fixtures_completed.create_index([("fixture.status.short", ASCENDING)])
            
            # Date indexes
            self.fixtures_staging.create_index([("fixture.date", ASCENDING)])
            self.fixtures_live.create_index([("fixture.date", ASCENDING)])
            self.fixtures_active.create_index([("fixture.date", ASCENDING)])
            self.fixtures_completed.create_index([("fixture.date", ASCENDING)])

            # Team indexes
            for collection in [self.fixtures_staging, self.fixtures_live, self.fixtures_active, self.fixtures_completed]:
                collection.create_index([("teams.home.id", ASCENDING)])
                collection.create_index([("teams.away.id", ASCENDING)])
            
            # Enhanced event field indexes (fixtures_active only)
            self.fixtures_active.create_index([("events._event_id", ASCENDING)])
            self.fixtures_active.create_index([("events._debounce_complete", ASCENDING)])
            
            print("✅ MongoDB indexes created successfully")
        except Exception as e:
            print(f"⚠️ Error creating indexes: {e}")

    # === Helper Methods ===
    
    @staticmethod
    def _extract_fixture_id(raw_item: dict) -> int:
        """Extract fixture.id from raw API schema"""
        try:
            return int(raw_item["fixture"]["id"])
        except Exception:
            return 0

    @staticmethod
    def _extract_status(raw_item: dict) -> str:
        """Extract status from raw API schema"""
        try:
            return raw_item["fixture"]["status"]["short"]
        except Exception:
            return "NS"

    @staticmethod
    def _extract_current_goals(raw_item: dict) -> dict:
        """Extract goals from raw API schema with null handling"""
        try:
            goals = raw_item.get("goals")
            if goals is None:
                return {"home": 0, "away": 0}
            
            home = goals.get("home")
            away = goals.get("away")
            
            return {
                "home": home if home is not None else 0,
                "away": away if away is not None else 0
            }
        except Exception:
            return {"home": 0, "away": 0}
    


    # === Staging Operations ===
    
    def fixtures_insert_staging(self, raw_fixtures: List[dict]) -> int:
        """Insert fixtures into staging collection"""
        try:
            if not raw_fixtures:
                return 0
            
            docs = []
            for fixture in raw_fixtures:
                fixture_id = self._extract_fixture_id(fixture)
                if fixture_id == 0:
                    continue
                
                doc = dict(fixture)
                doc["_id"] = fixture_id
                docs.append(doc)
            
            if docs:
                result = self.fixtures_staging.insert_many(docs, ordered=False)
                return len(result.inserted_ids)
            return 0
        except Exception as e:
            print(f"❌ Error inserting to staging: {e}")
            return 0

    def get_staging_fixtures(self) -> List[dict]:
        """Get all fixtures in staging"""
        try:
            return list(self.fixtures_staging.find({}))
        except Exception as e:
            print(f"❌ Error getting staging fixtures: {e}")
            return []

    def activate_fixture(self, fixture_id: int) -> bool:
        """
        Move fixture from staging to active with EMPTY events array.
        This preserves all fixture metadata but starts with no events.
        """
        try:
            staging_doc = self.fixtures_staging.find_one({"_id": fixture_id})
            if not staging_doc:
                return False
            
            # Initialize with empty events array
            staging_doc["events"] = []
            staging_doc["activated_at"] = datetime.now(timezone.utc)
            
            self.fixtures_active.replace_one(
                {"_id": fixture_id},
                staging_doc,
                upsert=True
            )
            self.fixtures_staging.delete_one({"_id": fixture_id})
            return True
        except Exception as e:
            print(f"❌ Error activating fixture {fixture_id}: {e}")
            return False

    # === Live Operations (Temporary Raw Data Storage) ===
    
    def store_live_fixture(self, fixture_id: int, api_data: dict) -> bool:
        """
        Store raw API data in fixtures_live for comparison (gets overwritten each poll).
        Filters events to only include trackable ones (Goals only, per event_config).
        Generates _event_id for each event to enable easy comparison in debounce.
        
        Event IDs are sequential per team+event_type: {fixture_id}_{team_id}_{event_type}_{#}
        """
        try:
            from src.utils.event_config import should_track_event
            
            doc = dict(api_data)
            doc["_id"] = fixture_id
            doc["stored_at"] = datetime.now(timezone.utc)
            
            # Filter events and generate IDs in single pass
            raw_events = doc.get("events", [])
            filtered_events = []
            team_event_counters = {}  # Track sequence per team+event_type: "team_id_event_type" -> count
            
            for event in raw_events:
                if should_track_event(event):
                    # Increment counter for this team+event_type combo
                    team_id = event.get("team", {}).get("id", 0)
                    event_type = event.get("type", "Unknown")
                    counter_key = f"{team_id}_{event_type}"
                    team_event_counters[counter_key] = team_event_counters.get(counter_key, 0) + 1
                    sequence = team_event_counters[counter_key]
                    
                    # Generate and attach event ID: {fixture_id}_{team_id}_{event_type}_{#}
                    event["_event_id"] = f"{fixture_id}_{team_id}_{event_type}_{sequence}"
                    filtered_events.append(event)
            
            doc["events"] = filtered_events
            
            self.fixtures_live.replace_one(
                {"_id": fixture_id},
                doc,
                upsert=True
            )
            return True
        except Exception as e:
            print(f"❌ Error storing live fixture {fixture_id}: {e}")
            return False
    
    def get_live_fixture(self, fixture_id: int) -> dict | None:
        """Get raw API data from fixtures_live"""
        try:
            return self.fixtures_live.find_one({"_id": fixture_id})
        except Exception as e:
            print(f"❌ Error getting live fixture {fixture_id}: {e}")
            return None

    # === Active Operations ===
    
    def get_fixture_from_active(self, fixture_id: int) -> dict | None:
        """Get fixture document from fixtures_active"""
        try:
            return self.fixtures_active.find_one({"_id": fixture_id})
        except Exception as e:
            print(f"❌ Error getting fixture {fixture_id} from active: {e}")
            return None
    
    def get_active_fixtures(self) -> List[dict]:
        """Get all active fixtures for monitoring"""
        try:
            return list(self.fixtures_active.find({}))
        except Exception as e:
            print(f"❌ Error getting active fixtures: {e}")
            return []
    
    def get_active_fixture_ids(self) -> List[int]:
        """Get all active fixture IDs"""
        try:
            fixtures = list(self.fixtures_active.find({}, {"_id": 1}))
            return [f["_id"] for f in fixtures]
        except Exception as e:
            print(f"❌ Error getting active fixture IDs: {e}")
            return []

    # === Comparison Logic (3 Cases for Debounce Trigger) ===
    
    def compare_live_vs_active(self, fixture_id: int) -> Dict[str, Any]:
        """
        Compare fixtures_live (raw API) vs fixtures_active (enhanced).
        Uses pre-generated _event_id fields for clean comparison.
        
        Returns which events need debounce based on 3 cases:
        1. NEW: Event exists in live but NOT in active
        2. INCOMPLETE: Event exists in BOTH but _debounce_complete=false in active
        3. REMOVED: Event exists in active but NOT in live (VAR disallowed goal, API removed it)
        
        Any of these 3 cases triggers immediate debounce for this fixture.
        """
        try:
            live = self.get_live_fixture(fixture_id)
            active = self.get_fixture_from_active(fixture_id)
            
            if not live or "events" not in live:
                return {"needs_debounce": False, "reason": "no_live_events"}
            
            live_events = live.get("events", [])
            active_events = active.get("events", []) if active else []
            
            # Build sets of event IDs (already generated in store_live_fixture)
            live_event_ids = {e.get("_event_id") for e in live_events if e.get("_event_id")}
            active_event_ids = {e.get("_event_id") for e in active_events if e.get("_event_id")}
            
            # CASE 1: NEW - Events in live but not in active
            new_event_ids = live_event_ids - active_event_ids
            
            # CASE 2: INCOMPLETE - Events in both but not debounce complete
            active_map = {e.get("_event_id"): e for e in active_events if e.get("_event_id")}
            incomplete_count = 0
            for event_id in live_event_ids:
                if event_id in active_map and not active_map[event_id].get("_debounce_complete", False):
                    incomplete_count += 1
            
            # CASE 3: REMOVED - Events in active but not in live anymore
            removed_event_ids = active_event_ids - live_event_ids
            
            # Trigger debounce if ANY case has events
            needs_debounce = (
                len(new_event_ids) > 0 or 
                incomplete_count > 0 or 
                len(removed_event_ids) > 0
            )
            
            return {
                "needs_debounce": needs_debounce,
                "new_events": len(new_event_ids),
                "incomplete_events": incomplete_count,
                "removed_events": len(removed_event_ids),
                "total_live_events": len(live_events),
                "total_active_events": len(active_events)
            }
        except Exception as e:
            print(f"❌ Error comparing live vs active for fixture {fixture_id}: {e}")
            import traceback
            traceback.print_exc()
            return {"needs_debounce": False, "reason": "error", "error": str(e)}

    # === Event Enhancement Operations (In-Place Updates to fixtures_active) ===
    
    def add_event_to_active(self, fixture_id: int, event_with_enhancements: dict) -> bool:
        """
        Add NEW event to fixtures_active events array with enhancement fields.
        Called by debounce job when detecting a new event (CASE 1).
        
        Enhancement fields in event:
        - _event_id: Unique event identifier
        - _stable_count: Number of consecutive unchanged polls
        - _debounce_complete: True when stable_count reaches threshold
        - _twitter_complete: True when videos downloaded
        - _first_seen: Timestamp when first detected
        - _snapshots: List of hash snapshots over time
        - _score_before: Score before this event
        - _score_after: Score after this event
        - _twitter_search: Search string for Twitter
        """
        try:
            result = self.fixtures_active.update_one(
                {"_id": fixture_id},
                {"$push": {"events": event_with_enhancements}}
            )
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error adding event to active fixture {fixture_id}: {e}")
            return False
    
    def update_event_stable_count(self, fixture_id: int, event_id: str, 
                                   stable_count: int, snapshot: dict) -> bool:
        """
        Update existing event's stable_count and add snapshot.
        Called by debounce job for CASE 2 (incomplete events).
        """
        try:
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, "events._event_id": event_id},
                {
                    "$set": {"events.$._stable_count": stable_count},
                    "$push": {"events.$._snapshots": snapshot}
                }
            )
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error updating stable count for event {event_id}: {e}")
            return False
    
    def mark_event_debounce_complete(self, fixture_id: int, event_id: str) -> bool:
        """
        Mark event as debounce complete (stable_count reached threshold).
        Called by debounce job when event is stable.
        """
        try:
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, "events._event_id": event_id},
                {"$set": {"events.$._debounce_complete": True}}
            )
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error marking event {event_id} debounce complete: {e}")
            return False
    
    def mark_event_removed(self, fixture_id: int, event_id: str) -> bool:
        """
        Mark event as removed (no longer in API data).
        Called by debounce job for CASE 3 (removed events).
        """
        try:
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, "events._event_id": event_id},
                {
                    "$set": {
                        "events.$._removed": True,
                        "events.$._removed_at": datetime.now(timezone.utc)
                    }
                }
            )
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error marking event {event_id} as removed: {e}")
            return False
    
    def mark_event_twitter_started(self, fixture_id: int, event_id: str) -> bool:
        """
        Mark event as twitter search started.
        Called by twitter job when search begins.
        """
        try:
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, "events._event_id": event_id},
                {
                    "$set": {
                        "events.$._twitter_started": True,
                        "events.$._twitter_started_at": datetime.now(timezone.utc)
                    }
                }
            )
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error marking event {event_id} twitter started: {e}")
            return False
    
    def mark_event_twitter_complete(
        self, 
        fixture_id: int, 
        event_id: str, 
        discovered_videos: List[Dict[str, Any]] | None = None
    ) -> bool:
        """
        Mark event as twitter complete and save discovered video URLs.
        Called by twitter job when search completes.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            discovered_videos: List of video dicts with metadata (tweet_url, tweet_text, etc.)
        """
        try:
            update_doc = {
                "events.$._twitter_complete": True,
                "events.$._twitter_completed_at": datetime.now(timezone.utc)
            }
            
            # Add discovered videos if provided
            if discovered_videos is not None:
                update_doc["events.$._discovered_videos"] = discovered_videos
                update_doc["events.$._video_count"] = len(discovered_videos)
            
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, "events._event_id": event_id},
                {"$set": update_doc}
            )
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error marking event {event_id} twitter complete: {e}")
            return False
    
    def mark_event_download_complete(
        self,
        fixture_id: int,
        event_id: str,
        s3_urls: List[str]
    ) -> bool:
        """
        Mark event as download complete and save S3 URLs.
        Called by download job when videos uploaded.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            s3_urls: List of S3 URLs for uploaded videos
        """
        try:
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, "events._event_id": event_id},
                {
                    "$set": {
                        "events.$._download_complete": True,
                        "events.$._s3_urls": s3_urls,
                        "events.$._s3_count": len(s3_urls),
                        "events.$._download_completed_at": datetime.now(timezone.utc)
                    }
                }
            )
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error marking event {event_id} download complete: {e}")
            return False

    # === Completion Operations ===
    
    def complete_fixture(self, fixture_id: int) -> bool:
        """
        Move fixture from active to completed (when status is FT/AET/PEN AND all events processed).
        Also removes from fixtures_live.
        
        CRITICAL: Only completes if ALL enhanced events have:
        - _debounce_complete: true
        - _twitter_complete: true
        """
        try:
            fixture_doc = self.fixtures_active.find_one({"_id": fixture_id})
            if not fixture_doc:
                print(f"⚠️ Fixture {fixture_id} not found in active")
                return False
            
            # Check if fixture has enhanced events
            events = fixture_doc.get("events", [])
            enhanced_events = [e for e in events if e.get("_event_id")]
            
            if enhanced_events:
                # Has events - must check if all processing is complete
                all_debounced = all(e.get("_debounce_complete", False) for e in enhanced_events)
                all_twitter_done = all(e.get("_twitter_complete", False) for e in enhanced_events)
                
                if not (all_debounced and all_twitter_done):
                    # Events still being processed - cannot complete yet
                    debounced = sum(1 for e in enhanced_events if e.get("_debounce_complete"))
                    twitter_done = sum(1 for e in enhanced_events if e.get("_twitter_complete"))
                    print(
                        f"⏳ Fixture {fixture_id} waiting for event processing: "
                        f"debounced={debounced}/{len(enhanced_events)}, "
                        f"twitter={twitter_done}/{len(enhanced_events)}"
                    )
                    return False
            
            # Either no events OR all events fully processed - can complete
            # Add completion timestamp
            fixture_doc["completed_at"] = datetime.now(timezone.utc)
            
            # Insert to completed
            self.fixtures_completed.insert_one(fixture_doc)
            
            # Remove from active and live
            self.fixtures_active.delete_one({"_id": fixture_id})
            self.fixtures_live.delete_one({"_id": fixture_id})
            
            return True
        except Exception as e:
            print(f"❌ Error completing fixture {fixture_id}: {e}")
            return False
    
    def get_completed_fixtures(self) -> List[dict]:
        """Get all completed fixtures"""
        try:
            return list(self.fixtures_completed.find({}))
        except Exception as e:
            print(f"❌ Error getting completed fixtures: {e}")
            return []
