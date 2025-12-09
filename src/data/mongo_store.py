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
    
    # Class variable to track if indexes have been created this session
    _indexes_created = False
    
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
        """Create indexes for all collections (only runs once per session)"""
        # Skip if already created this session
        if getattr(FootyMongoStore, '_indexes_created', False):
            return
        
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
            self.fixtures_active.create_index([("events._monitor_complete", ASCENDING)])
            
            FootyMongoStore._indexes_created = True
        except Exception as e:
            print(f"⚠️ MongoDB index error: {e}")

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
    
    def bulk_insert_fixtures(self, raw_fixtures: List[dict], collection_name: str) -> int:
        """
        Insert fixtures into specified collection (staging, active, or completed).
        Convenience method for ingestion workflow.
        """
        if not raw_fixtures:
            return 0
        
        # Map collection names to actual collections
        collection_map = {
            "fixtures_staging": self.fixtures_staging,
            "fixtures_active": self.fixtures_active,
            "fixtures_completed": self.fixtures_completed,
        }
        
        collection = collection_map.get(collection_name)
        if collection is None:
            raise ValueError(f"Unknown collection: {collection_name}")
        
        try:
            docs = []
            for fixture in raw_fixtures:
                fixture_id = self._extract_fixture_id(fixture)
                if fixture_id == 0:
                    continue
                
                doc = dict(fixture)
                doc["_id"] = fixture_id
                docs.append(doc)
            
            if docs:
                result = collection.insert_many(docs, ordered=False)
                return len(result.inserted_ids)
            return 0
        except Exception as e:
            print(f"❌ Error inserting to {collection_name}: {e}")
            return 0
    
    def fixtures_insert_staging(self, raw_fixtures: List[dict]) -> int:
        """Insert fixtures into staging collection"""
        return self.bulk_insert_fixtures(raw_fixtures, "fixtures_staging")

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
                    # Get player, team, type for unique event ID
                    player_id = event.get("player", {}).get("id", 0)
                    team_id = event.get("team", {}).get("id", 0)
                    event_type = event.get("type", "Unknown")
                    
                    # Increment counter per player+event_type (handles multiple goals by same player)
                    counter_key = f"{player_id}_{event_type}"
                    team_event_counters[counter_key] = team_event_counters.get(counter_key, 0) + 1
                    sequence = team_event_counters[counter_key]
                    
                    # Generate event ID: {fixture_id}_{team_id}_{player_id}_{event_type}_{#}
                    # Player ID makes it unique - no hash needed for VAR scenarios!
                    event["_event_id"] = f"{fixture_id}_{team_id}_{player_id}_{event_type}_{sequence}"
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
        Quick check to determine if EventWorkflow needs to run for this fixture.
        Uses set operations on pre-generated _event_id fields (cheap comparison).
        
        Algorithm:
        1. Build set of event_ids from fixtures_live (single iteration)
        2. Build set of event_ids from fixtures_active (single iteration)
        3. Check for differences using Python set operations:
           - NEW: live_ids - active_ids (in live, not in active)
           - REMOVED: active_ids - live_ids (in active, not in live)
           - INCOMPLETE: iterate live_ids, check if _monitor_complete=False
        
        Triggers EventWorkflow if ANY of these cases has events:
        - NEW events appeared
        - INCOMPLETE events exist (still debouncing)
        - REMOVED events (VAR disallowed)
        
        This is a cheap pre-check. The actual hash comparison happens
        inside the debounce_fixture_events activity (EventWorkflow).
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
            
            # CASE 2: INCOMPLETE - Events in both but not monitor complete
            active_map = {e.get("_event_id"): e for e in active_events if e.get("_event_id")}
            incomplete_count = 0
            for event_id in live_event_ids:
                if event_id in active_map and not active_map[event_id].get("_monitor_complete", False):
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
        - _monitor_count: Number of consecutive unchanged polls
        - _monitor_complete: True when monitor_count reaches threshold
        - _twitter_complete: True when videos downloaded
        - _first_seen: Timestamp when first detected
        - _snapshots: List of hash snapshots over time
        - _score_before: Score before this event
        - _score_after: Score after this event
        - _twitter_search: Search string for Twitter
        
        Also updates fixture-level _last_event timestamp for UI sorting.
        """
        try:
            now = datetime.now(timezone.utc)
            result = self.fixtures_active.update_one(
                {"_id": fixture_id},
                {
                    "$push": {"events": event_with_enhancements},
                    "$set": {"_last_event": now}
                }
            )
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error adding event to active fixture {fixture_id}: {e}")
            return False
    
    def update_event_stable_count(self, fixture_id: int, event_id: str, 
                                   stable_count: int, snapshot: dict | None = None) -> bool:
        """
        Update existing event's monitor_count.
        
        Note: snapshot parameter kept for backwards compatibility but ignored.
        With player_id in event_id, we don't need hash/snapshot tracking!
        
        Also updates fixture-level _last_event timestamp for UI sorting.
        """
        try:
            now = datetime.now(timezone.utc)
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, "events._event_id": event_id},
                {
                    "$set": {
                        "events.$._monitor_count": stable_count,
                        "_last_event": now
                    }
                }
            )
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error updating monitor count for event {event_id}: {e}")
            return False
    
    def mark_event_monitor_complete(self, fixture_id: int, event_id: str) -> bool:
        """
        Mark event as monitor complete (monitor_count reached threshold).
        Called by monitor activity when event is stable.
        Sets _twitter_count to 1 to begin Twitter attempts.
        """
        try:
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, "events._event_id": event_id},
                {"$set": {
                    "events.$._monitor_complete": True,
                    "events.$._twitter_count": 1
                }}
            )
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error marking event {event_id} monitor complete: {e}")
            return False
    
    def update_event_twitter_count(self, fixture_id: int, event_id: str, new_count: int, is_complete: bool) -> bool:
        """
        Update Twitter attempt counter and completion flag.
        Called by monitor before triggering additional Twitter searches.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID  
            new_count: New twitter_count value (1, 2, or 3)
            is_complete: True if new_count >= 3 (final attempt)
        """
        try:
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, "events._event_id": event_id},
                {"$set": {
                    "events.$._twitter_count": new_count,
                    "events.$._twitter_complete": is_complete
                }}
            )
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error updating twitter count for {event_id}: {e}")
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
        
        IMPORTANT: Merges new videos with existing ones (dedup by URL).
        This handles retry scenarios where we've already discovered some videos.
        
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
            
            # Merge new videos with existing (dedup by URL) - same logic as retry
            if discovered_videos is not None:
                # Fetch existing videos to merge
                existing_videos = []
                fixture = self.fixtures_active.find_one({"_id": fixture_id})
                if fixture:
                    for evt in fixture.get("events", []):
                        if evt.get("_event_id") == event_id:
                            existing_videos = evt.get("_discovered_videos", [])
                            break
                
                # Create URL set from existing videos (Twitter uses video_page_url)
                existing_urls = {v.get("video_page_url") or v.get("url") for v in existing_videos if v.get("video_page_url") or v.get("url")}
                
                # Add new videos that aren't already in the list
                merged_videos = existing_videos.copy()
                for video in discovered_videos:
                    video_url = video.get("video_page_url") or video.get("url")
                    if video_url not in existing_urls:
                        merged_videos.append(video)
                
                update_doc["events.$._discovered_videos"] = merged_videos
                update_doc["events.$._video_count"] = len(merged_videos)
            
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, "events._event_id": event_id},
                {"$set": update_doc}
            )
            if result.modified_count == 0:
                msg = f"❌ FATAL: Failed to mark event {event_id} twitter complete (0 documents modified)"
                print(msg)
                raise RuntimeError(msg)
            return True
        except Exception as e:
            print(f"❌ FATAL: Error marking event {event_id} twitter complete: {e}")
            raise  # Re-raise - this is critical for pipeline integrity
    

    def _generate_event_display_titles(self, fixture: Dict[str, Any], event: Dict[str, Any]) -> tuple:
        """
        Generate display titles for frontend.
        
        Title: "Manchester City 0 - (3) Liverpool" (home team first, scoring team score in parens)
        Subtitle: "90+2 - Florian Wirtz (Dominik Szoboszlai)" (minute - scorer (assister))
        
        Args:
            fixture: Fixture data with teams, score
            event: Event data with time, player, assist
        
        Returns:
            tuple: (title, subtitle)
        """
        # Extract teams and scores
        home_team = fixture.get("teams", {}).get("home", {}).get("name", "Unknown")
        away_team = fixture.get("teams", {}).get("away", {}).get("name", "Unknown")
        home_score = fixture.get("goals", {}).get("home", 0)
        away_score = fixture.get("goals", {}).get("away", 0)
        
        # Determine which team scored (for highlighting)
        scoring_team = event.get("team", {}).get("name", "")
        
        # Build title with scoring team's score in parentheses
        # Format: "Home 0 - (3) Away" or "Away 2 - (1) Home" (home team first if exists)
        if home_team and home_team != "Unknown":
            # Home team exists - show home first
            if scoring_team == home_team:
                title = f"{home_team} ({home_score}) - {away_score} {away_team}"
            else:
                title = f"{home_team} {home_score} - ({away_score}) {away_team}"
        else:
            # No home team (neutral venue) - show scoring team first
            if scoring_team == away_team:
                title = f"{away_team} ({away_score}) - {home_score} {home_team}"
            else:
                title = f"{home_team} ({home_score}) - {away_score} {away_team}"
        
        # Build subtitle with time and players
        minute = event.get("time", {}).get("elapsed", 0)
        extra = event.get("time", {}).get("extra")
        scorer = event.get("player", {}).get("name", "Unknown")
        assister = event.get("assist", {}).get("player", {}).get("name", "")
        
        # Format time: "90+2" or just "45"
        time_str = f"{minute}+{extra}" if extra else str(minute)
        
        # Format subtitle: "90+2 - Florian Wirtz (Dominik Szoboszlai)"
        if assister:
            subtitle = f"{time_str}' - {scorer} ({assister})"
        else:
            subtitle = f"{time_str}' - {scorer}"
        
        return title, subtitle
    
    def sync_fixture_data(self, fixture_id: int) -> bool:
        """
        Sync fixture top-level data from fixtures_live to fixtures_active.
        Preserves enhanced events array - only updates fixture metadata.
        Called after successful debounce to keep fixture data fresh.
        
        Updates: fixture, teams, league, goals, score, status, etc.
        Preserves: events array (with all enhancement fields)
        
        Also regenerates display titles for all events (low-cost operation).
        """
        try:
            live_fixture = self.get_live_fixture(fixture_id)
            if not live_fixture:
                return False
            
            # Get current events from active (preserve enhancements)
            active_fixture = self.get_fixture_from_active(fixture_id)
            if not active_fixture:
                return False
            
            # Build update document with all top-level fields from live
            update_doc = dict(live_fixture)
            update_doc["_id"] = fixture_id
            
            # Preserve enhanced events from active and regenerate display titles
            enhanced_events = active_fixture.get("events", [])
            for event in enhanced_events:
                # Regenerate display titles based on current fixture state
                title, subtitle = self._generate_event_display_titles(update_doc, event)
                event["_display_title"] = title
                event["_display_subtitle"] = subtitle
            
            update_doc["events"] = enhanced_events
            
            # Replace entire document (preserving events)
            result = self.fixtures_active.replace_one(
                {"_id": fixture_id},
                update_doc
            )
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error syncing fixture data for {fixture_id}: {e}")
            return False
    
    def mark_event_download_complete(
        self,
        fixture_id: int,
        event_id: str,
        s3_urls: List[str],
        perceptual_hashes: List[str]
    ) -> bool:
        """
        Save S3 URLs and perceptual hashes after download completes.
        Called by download workflow after videos uploaded.
        
        Note: Monitor workflow controls _twitter_count and _twitter_complete.
        This just appends new results to existing arrays.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            s3_urls: List of S3 URLs for uploaded videos (this attempt only)
            perceptual_hashes: List of perceptual hashes for cross-resolution dedup
        """
        try:
            # If no new videos (all were duplicates), just verify event exists
            if not s3_urls and not perceptual_hashes:
                # Verify event exists in active collection
                fixture = self.fixtures_active.find_one(
                    {"_id": fixture_id, "events._event_id": event_id}
                )
                if not fixture:
                    msg = f"❌ FATAL: Event {event_id} not found in fixtures_active"
                    print(msg)
                    raise RuntimeError(msg)
                print(f"✅ Download complete for {event_id} (0 new videos - all were duplicates)")
                return True
            
            # Append new S3 URLs and hashes to existing arrays
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, "events._event_id": event_id},
                {
                    "$push": {
                        "events.$._s3_urls": {"$each": s3_urls},
                        "events.$._perceptual_hashes": {"$each": perceptual_hashes}
                    }
                }
            )
            
            if result.modified_count == 0:
                msg = f"❌ FATAL: Failed to mark event {event_id} download complete (0 documents modified)"
                print(msg)
                raise RuntimeError(msg)
            return True
        except Exception as e:
            print(f"❌ FATAL: Error marking event {event_id} download complete: {e}")
            raise  # Re-raise - this is critical for pipeline integrity

    # === Completion Operations ===
    
    def complete_fixture(self, fixture_id: int) -> bool:
        """
        Move fixture from active to completed (when status is FT/AET/PEN AND all events processed).
        Also removes from fixtures_live.
        
        CRITICAL: Only completes if ALL valid enhanced events have:
        - _monitor_complete: true
        - _twitter_complete: true (stays false until 5 videos OR 3 attempts reached)
        
        Ignores:
        - Removed events (_removed: true)
        - Events without player ID (player_id=None, can't generate stable event_id)
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
                # Filter out events that should be ignored for completion
                # 1. Removed events (VAR disallowed, etc.)
                # 2. Events with player_id=None (can't generate stable event_id)
                valid_events = [
                    e for e in enhanced_events 
                    if not e.get("_removed", False) 
                    and "None" not in e.get("_event_id", "")
                ]
                
                if not valid_events:
                    # All events removed or invalid - can complete
                    pass
                else:
                    # Has valid events - must check if all processing is complete
                    all_monitored = all(e.get("_monitor_complete", False) for e in valid_events)
                    
                    # For Twitter: MUST have reached 3 attempts (_twitter_count >= 3)
                    # Derive completion from attempt counter instead of boolean flag
                    all_twitter_done = all(e.get("_twitter_count", 0) >= 3 for e in valid_events)
                    
                    if not (all_monitored and all_twitter_done):
                        # Events still being processed - cannot complete yet
                        monitored = sum(1 for e in valid_events if e.get("_monitor_complete"))
                        twitter_done = sum(1 for e in valid_events if e.get("_twitter_count", 0) >= 3)
                        print(
                            f"⏳ Fixture {fixture_id} waiting for event processing: "
                            f"monitored={monitored}/{len(valid_events)}, "
                            f"twitter={twitter_done}/{len(valid_events)}"
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
