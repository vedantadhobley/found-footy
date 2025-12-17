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
        
        Does NOT set _last_activity here - that only happens when:
        - A goal is confirmed (in mark_event_monitor_complete)
        
        This ensures fixtures are sorted by actual events, not by when
        they happened to be activated or when the match started.
        A 0-0 match will never have _last_activity set.
        """
        try:
            staging_doc = self.fixtures_staging.find_one({"_id": fixture_id})
            if not staging_doc:
                return False
            
            # Initialize with empty events array
            staging_doc["events"] = []
            
            # Don't set _last_activity here - will be set when match actually starts
            # or when first goal is confirmed. This prevents a delayed 0-0 match
            # from jumping above a match with actual goals.
            # Use null - frontend sorts fixtures with null _last_activity by kickoff time
            staging_doc["_last_activity"] = None
            
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
        
        Event IDs use sequence per player+type: {fixture_id}_{team_id}_{player_id}_{event_type}_{sequence}
        Sequence-based IDs handle minute drift during debounce (44'→45' doesn't change ID).
        VAR'd events are deleted entirely, freeing the sequence slot for new goals.
        """
        try:
            from src.utils.event_config import should_track_event
            
            doc = dict(api_data)
            doc["_id"] = fixture_id
            
            # Filter events and generate IDs in single pass
            raw_events = doc.get("events", [])
            filtered_events = []
            player_event_counters = {}  # Track sequence per player+event_type
            
            for event in raw_events:
                if should_track_event(event):
                    # Get player, team, type for unique event ID
                    player_id = event.get("player", {}).get("id", 0)
                    team_id = event.get("team", {}).get("id", 0)
                    event_type = event.get("type", "Unknown")
                    
                    # Increment counter per player+event_type (handles multiple goals by same player)
                    counter_key = f"{player_id}_{event_type}"
                    player_event_counters[counter_key] = player_event_counters.get(counter_key, 0) + 1
                    sequence = player_event_counters[counter_key]
                    
                    # Generate event ID: {fixture_id}_{team_id}_{player_id}_{event_type}_{sequence}
                    # Sequence-based to handle minute drift; removed events handled separately in debounce
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
    
    def add_event_to_active(self, fixture_id: int, event_with_enhancements: dict, event_first_seen: datetime) -> bool:
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
        
        """
        try:
            # Use $max to ensure _last_activity only moves forward
            result = self.fixtures_active.update_one(
                {"_id": fixture_id},
                {
                    "$push": {"events": event_with_enhancements},
                    "$max": {"_last_activity": event_first_seen}
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
        """
        try:
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, "events._event_id": event_id},
                {"$set": {"events.$._monitor_count": stable_count}}
            )
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error updating monitor count for event {event_id}: {e}")
            return False
    
    def mark_event_monitor_complete(self, fixture_id: int, event_id: str, event_first_seen: datetime = None) -> bool:
        """
        Mark event as monitor complete (monitor_count reached threshold).
        Called by monitor activity when event is stable.
        Sets _twitter_count to 1 to begin Twitter attempts.
        Updates _last_activity to event's _first_seen (when goal was first detected).
        
        NOTE: We use _first_seen (not datetime.now()) because:
        - It reflects when the goal actually happened (within ~1 min of real time)
        - Combined with activate_fixture using scheduled kickoff time,
          this ensures fixtures are sorted by when goals occurred
        """
        try:
            update_ops = {
                "$set": {
                    "events.$._monitor_complete": True,
                    "events.$._twitter_count": 1
                }
            }
            
            # Update _last_activity to when the goal was first detected
            if event_first_seen:
                update_ops["$max"] = {"_last_activity": event_first_seen}
            
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, "events._event_id": event_id},
                update_ops
            )
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error marking event {event_id} monitor complete: {e}")
            return False
    
    def update_event_twitter_count(self, fixture_id: int, event_id: str, new_count: int) -> bool:
        """
        Update Twitter attempt counter.
        Called by monitor BEFORE triggering Twitter workflow.
        
        NOTE: Does NOT set _twitter_complete - that's set by Twitter workflow when it finishes.
        This ensures we track "attempts started" vs "attempts completed" separately.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID  
            new_count: New twitter_count value (1, 2, or 3)
        """
        try:
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, "events._event_id": event_id},
                {"$set": {
                    "events.$._twitter_count": new_count
                }}
            )
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error updating twitter count for {event_id}: {e}")
            return False
    
    def mark_event_removed(self, fixture_id: int, event_id: str) -> bool:
        """
        Remove event completely when VAR disallows it.
        
        This DELETES the event from MongoDB and S3 (not just marks it removed).
        Why: Sequence-based event IDs (e.g., ..._Goal_1) would collide if the
        same player scores again after VAR. Deleting frees the ID slot.
        
        Steps:
        1. Get event's S3 videos from MongoDB
        2. Delete videos from S3
        3. Remove event from MongoDB events array
        """
        from src.data.s3_store import FootyS3Store
        
        try:
            # Step 1: Get event data to find S3 videos
            fixture = self.fixtures_active.find_one(
                {"_id": fixture_id, "events._event_id": event_id},
                {"events.$": 1}
            )
            
            if fixture and fixture.get("events"):
                event = fixture["events"][0]
                # Support both old (_s3_urls) and new (_s3_videos) schema
                s3_videos = event.get("_s3_videos", [])
                s3_urls = [v.get("url") for v in s3_videos if v.get("url")]
                # Fallback to old schema
                if not s3_urls:
                    s3_urls = event.get("_s3_urls", [])
                
                # Step 2: Delete videos from S3
                if s3_urls:
                    s3_store = FootyS3Store()
                    for s3_url in s3_urls:
                        # Extract S3 key from relative path
                        # Format: /video/footy-videos/{key}
                        try:
                            if not s3_url.startswith("/video/footy-videos/"):
                                print(f"⚠️ Unexpected S3 URL format: {s3_url}")
                                continue
                            key = s3_url.replace("/video/footy-videos/", "")
                            s3_store.delete_video(key)
                            print(f"🗑️ Deleted S3 video for VAR'd goal: {key}")
                        except Exception as e:
                            print(f"⚠️ Failed to delete S3 video {s3_url}: {e}")
            
            # Step 3: Remove event from MongoDB array entirely
            result = self.fixtures_active.update_one(
                {"_id": fixture_id},
                {"$pull": {"events": {"_event_id": event_id}}}
            )
            
            if result.modified_count > 0:
                print(f"🗑️ Removed VAR'd event from MongoDB: {event_id}")
                return True
            return False
            
        except Exception as e:
            print(f"❌ Error removing event {event_id}: {e}")
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
        Generate display titles for frontend with highlight markers.
        
        Title: "<<Liverpool (3)>> - 0 Arsenal" (scoring team highlighted with <<>>)
        Subtitle: "88' Goal - <<A. Grimaldo>> (Assister)" (scorer highlighted with <<>>)
        
        Frontend should parse <<text>> markers and render them as highlighted text.
        
        Args:
            fixture: Fixture data with teams
            event: Event data with time, player, assist, _score_after
        
        Returns:
            tuple: (title, subtitle)
        """
        # Extract teams
        home_team = fixture.get("teams", {}).get("home", {}).get("name", "Unknown")
        away_team = fixture.get("teams", {}).get("away", {}).get("name", "Unknown")
        
        # Use _score_after (score after this goal) instead of fixture.goals (final score)
        score_after = event.get("_score_after", {})
        home_score = score_after.get("home", 0)
        away_score = score_after.get("away", 0)
        
        # Fallback to fixture goals if _score_after not set (shouldn't happen)
        if not score_after:
            home_score = fixture.get("goals", {}).get("home", 0)
            away_score = fixture.get("goals", {}).get("away", 0)
        
        # Determine which team scored (for highlighting)
        scoring_team = event.get("_scoring_team", "")
        
        # Build title with scoring team highlighted using <<>> markers
        # Format: "<<Home (1)>> - 0 Away" or "Home 0 - <<(1) Away>>"
        if scoring_team == "home":
            title = f"<<{home_team} ({home_score})>> - {away_score} {away_team}"
        else:
            title = f"{home_team} {home_score} - <<({away_score}) {away_team}>>"
        
        # Build subtitle with time and players
        minute = event.get("time", {}).get("elapsed", 0)
        extra = event.get("time", {}).get("extra")
        scorer = event.get("player", {}).get("name", "Unknown")
        detail = event.get("detail", "")
        assister = event.get("assist", {}).get("name", "")
        
        # Format time: "90+2" or just "45"
        time_str = f"{minute}+{extra}" if extra else str(minute)
        
        # Format: "88' Goal - <<Scorer>> (Assister)" with scorer highlighted
        # Possible detail values: "Normal Goal", "Own Goal", "Penalty", "Missed Penalty"
        if detail == "Own Goal":
            goal_type = "Own Goal"
        elif detail == "Penalty":
            goal_type = "Penalty Goal"
        elif detail == "Missed Penalty":
            goal_type = "Missed Penalty"
        else:
            goal_type = "Goal"
        
        if assister:
            subtitle = f"{time_str}' {goal_type} - <<{scorer}>> ({assister})"
        else:
            subtitle = f"{time_str}' {goal_type} - <<{scorer}>>"
        
        return title, subtitle
    
    def sync_fixture_data(self, fixture_id: int) -> bool:
        """
        Sync fixture top-level data from fixtures_live to fixtures_active.
        Also updates raw event fields from API while preserving our enhanced fields.
        Called after successful debounce to keep fixture data fresh.
        
        Updates: fixture, teams, league, goals, score, status, etc.
        Updates in events: assist, detail, comments, time (raw API fields)
        Preserves in events: all _ prefixed fields (our enhancements)
        
        Also regenerates display titles for all events.
        
        Note: _last_activity is only set when an event (goal) is confirmed,
        not when the match starts. This is handled by mark_event_monitor_complete.
        """
        from src.utils.fixture_status import get_staging_statuses, get_active_statuses
        
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
            
            # Check if match just started (NS/TBD → active status)
            old_status = active_fixture.get("fixture", {}).get("status", {}).get("short", "")
            new_status = live_fixture.get("fixture", {}).get("status", {}).get("short", "")
            
            staging_statuses = get_staging_statuses()  # ["NS", "TBD"]
            active_statuses = get_active_statuses()    # ["1H", "HT", "2H", etc.]
            
            if old_status in staging_statuses and new_status in active_statuses:
                # Match just started - but don't set _last_activity yet
                # It will only be set when an actual event (goal) happens
                print(f"⚽ Match {fixture_id} started! ({old_status} → {new_status})")
            
            # Preserve existing _last_activity (only set by mark_event_monitor_complete)
            if "_last_activity" in active_fixture:
                update_doc["_last_activity"] = active_fixture["_last_activity"]
            
            # Build lookup of live events by event_id for merging
            # Live events already have _event_id from store_live_fixture()
            live_events = live_fixture.get("events", [])
            live_events_by_id = {}
            for live_event in live_events:
                event_id = live_event.get("_event_id")
                if event_id:
                    live_events_by_id[event_id] = live_event
            
            # Merge API updates into enhanced events while preserving our fields
            enhanced_events = active_fixture.get("events", [])
            for event in enhanced_events:
                event_id = event.get("_event_id")
                if event_id and event_id in live_events_by_id:
                    live_event = live_events_by_id[event_id]
                    # Update raw API fields (non _ prefixed) from live
                    for key, value in live_event.items():
                        if not key.startswith("_"):
                            event[key] = value
                
                # Regenerate display titles based on updated data
                title, subtitle = self._generate_event_display_titles(update_doc, event)
                event["_display_title"] = title
                event["_display_subtitle"] = subtitle
            
            update_doc["events"] = enhanced_events
            
            # Replace entire document (preserving events)
            result = self.fixtures_active.replace_one(
                {"_id": fixture_id},
                update_doc
            )
            if result.modified_count > 0:
                live_status = live_fixture.get("fixture", {}).get("status", {}).get("short")
                print(f"🔄 Synced fixture {fixture_id}: status={live_status}")
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error syncing fixture data for {fixture_id}: {e}")
            return False
    
    def add_videos_to_event(
        self,
        fixture_id: int,
        event_id: str,
        video_objects: List[Dict[str, Any]]
    ) -> bool:
        """
        Add video objects to _s3_videos array.
        Deduplicates by URL to prevent duplicate entries.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            video_objects: List of {url, perceptual_hash, resolution_score, file_size, popularity, rank}
        
        Returns:
            True if successful
        """
        try:
            if not video_objects:
                # Verify event exists
                fixture = self.fixtures_active.find_one(
                    {"_id": fixture_id, "events._event_id": event_id}
                )
                if not fixture:
                    msg = f"❌ FATAL: Event {event_id} not found in fixtures_active"
                    print(msg)
                    raise RuntimeError(msg)
                print(f"✅ No new videos to add for {event_id}")
                return True
            
            # Get existing video URLs to avoid duplicates
            fixture = self.fixtures_active.find_one(
                {"_id": fixture_id, "events._event_id": event_id}
            )
            if not fixture:
                msg = f"❌ FATAL: Event {event_id} not found in fixtures_active"
                print(msg)
                raise RuntimeError(msg)
            
            # Find the event and get existing URLs
            existing_urls = set()
            for evt in fixture.get("events", []):
                if evt.get("_event_id") == event_id:
                    for video in evt.get("_s3_videos", []):
                        existing_urls.add(video.get("url", ""))
                    break
            
            # Filter out videos that already exist
            new_videos = [v for v in video_objects if v.get("url", "") not in existing_urls]
            
            if not new_videos:
                print(f"✅ All {len(video_objects)} videos already exist for {event_id}, skipping duplicates")
                return True
            
            if len(new_videos) < len(video_objects):
                print(f"⚠️ Filtered out {len(video_objects) - len(new_videos)} duplicate videos for {event_id}")
            
            # Append new video objects
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, "events._event_id": event_id},
                {
                    "$push": {
                        "events.$._s3_videos": {"$each": new_videos}
                    }
                }
            )
            
            if result.modified_count == 0:
                msg = f"❌ FATAL: Failed to add videos to event {event_id} (0 documents modified)"
                print(msg)
                raise RuntimeError(msg)
            
            print(f"✅ Added {len(video_objects)} videos to {event_id}")
            return True
        except Exception as e:
            print(f"❌ FATAL: Error adding videos to event {event_id}: {e}")
            raise
    
    def recalculate_video_ranks(self, fixture_id: int, event_id: str) -> bool:
        """
        Recalculate ranks for all videos in an event.
        Sorts by popularity (desc) then file_size (desc) - larger files = better quality.
        Rank 1 = best video.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            
        Returns:
            True if successful
        """
        try:
            # Get current fixture
            fixture = self.fixtures_active.find_one({"_id": fixture_id})
            if not fixture:
                print(f"⚠️ Fixture {fixture_id} not found")
                return False
            
            # Find the event
            event = None
            event_idx = None
            for idx, evt in enumerate(fixture.get("events", [])):
                if evt.get("_event_id") == event_id:
                    event = evt
                    event_idx = idx
                    break
            
            if event is None:
                print(f"⚠️ Event {event_id} not found")
                return False
            
            # Get videos and sort them
            videos = event.get("_s3_videos", [])
            if not videos:
                return True
            
            # Sort by popularity (desc) then file_size (desc) - bigger file = better quality
            videos_sorted = sorted(
                videos, 
                key=lambda v: (v.get("popularity", 1), v.get("file_size", 0)),
                reverse=True
            )
            
            # Assign ranks (1 = best)
            for rank, video in enumerate(videos_sorted, start=1):
                video["rank"] = rank
            
            # Update in MongoDB
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, "events._event_id": event_id},
                {"$set": {f"events.$.s3_videos": videos_sorted}}
            )
            
            # Also try the underscore version
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, "events._event_id": event_id},
                {"$set": {"events.$._s3_videos": videos_sorted}}
            )
            
            print(f"📊 Recalculated ranks for {len(videos)} videos in {event_id}")
            return True
        except Exception as e:
            print(f"❌ Error recalculating video ranks: {e}")
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
                print(f"📈 Updated popularity to {new_popularity} for {s3_url}")
                # Recalculate ranks
                self.recalculate_video_ranks(fixture_id, event_id)
                return True
            else:
                print(f"⚠️ Video not found for popularity update: {s3_url}")
                return False
        except Exception as e:
            print(f"❌ Error updating video popularity: {e}")
            return False

    # === Completion Operations ===
    
    def complete_fixture(self, fixture_id: int) -> bool:
        """
        Move fixture from active to completed (when status is FT/AET/PEN AND all events processed).
        Also removes from fixtures_live.
        
        CRITICAL: Only completes if ALL valid enhanced events have:
        - _monitor_complete: true (debounce finished)
        - _twitter_complete: true (set by Twitter workflow when done)
        
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
                    
                    # Twitter complete is set by Twitter workflow when it finishes (after downloads)
                    all_twitter_done = all(e.get("_twitter_complete", False) for e in valid_events)
                    
                    if not (all_monitored and all_twitter_done):
                        # Events still being processed - cannot complete yet
                        monitored = sum(1 for e in valid_events if e.get("_monitor_complete"))
                        twitter_done = sum(1 for e in valid_events if e.get("_twitter_complete"))
                        print(
                            f"⏳ Fixture {fixture_id} waiting for event processing: "
                            f"monitored={monitored}/{len(valid_events)}, "
                            f"twitter_complete={twitter_done}/{len(valid_events)}"
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
