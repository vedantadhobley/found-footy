"""
MongoDB Storage Layer
=====================

This module provides the data access layer for Found Footy's 4-collection architecture.
All MongoDB operations are centralized here for consistency and maintainability.

See src/data/models.py for complete data model documentation.
"""

import os
from datetime import datetime, timezone
from typing import Dict, List, Any

from pymongo import ASCENDING, MongoClient

from src.data.models import (
    FixtureFields,
    EventFields,
    FixtureStatus,
    TeamAliasFields,
    create_activation_fields,
)
from src.utils.config import MONGODB_URI


class FootyMongoStore:
    """
    MongoDB storage for 5-collection architecture:
    - fixtures_staging: Waiting to activate (TBD, NS)
    - fixtures_live: Raw API data (temporary, for comparison)
    - fixtures_active: Enhanced with debounce data (never overwritten)
    - fixtures_completed: Archive (FT, AET, PEN)
    - team_aliases: Cached team name aliases for Twitter search (RAG pipeline)
    """
    
    # Class variable to track if indexes have been created this session
    _indexes_created = False
    
    def __init__(self, connection_url=None):
        # Use centralized config (can be overridden via parameter or env)
        if connection_url is None:
            connection_url = os.getenv('MONGODB_URI') or os.getenv('MONGODB_URL') or MONGODB_URI
        
        self.client = MongoClient(connection_url)
        self.db = self.client.found_footy
        
        # 5 Collections
        self.fixtures_staging = self.db.fixtures_staging
        self.fixtures_live = self.db.fixtures_live
        self.fixtures_active = self.db.fixtures_active
        self.fixtures_completed = self.db.fixtures_completed
        self.team_aliases = self.db.team_aliases
        
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
            
            # Team aliases indexes (for lookup by team name)
            self.team_aliases.create_index([(TeamAliasFields.TEAM_NAME, ASCENDING)])
            
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

    def get_staging_fixture_ids(self) -> List[int]:
        """Get all staging fixture IDs"""
        try:
            fixtures = list(self.fixtures_staging.find({}, {"_id": 1}))
            return [f["_id"] for f in fixtures]
        except Exception as e:
            print(f"❌ Error getting staging fixture IDs: {e}")
            return []

    def update_staging_fixture(self, fixture_id: int, api_data: dict) -> bool:
        """
        Update fixture in staging with fresh API data.
        Preserves _id, updates everything else (status, times, teams, etc.)
        """
        try:
            doc = dict(api_data)
            doc["_id"] = fixture_id
            
            result = self.fixtures_staging.replace_one(
                {"_id": fixture_id},
                doc,
                upsert=False  # Don't create if doesn't exist
            )
            return result.modified_count > 0 or result.matched_count > 0
        except Exception as e:
            print(f"❌ Error updating staging fixture {fixture_id}: {e}")
            return False

    def activate_fixture_with_data(self, fixture_id: int, api_data: dict) -> bool:
        """
        Move fixture from staging to active using fresh API data.
        
        Called when we detect a status change from NS/TBD to any other status.
        Uses create_activation_fields() from models.py to set tracking fields.
        
        See src/data/models.py for complete field documentation.
        """
        try:
            # Remove from staging first
            staging_result = self.fixtures_staging.delete_one({"_id": fixture_id})
            if staging_result.deleted_count == 0:
                print(f"⚠️ Fixture {fixture_id} not found in staging")
                # Continue anyway - might be a race condition
            
            # Build active document from fresh API data
            doc = dict(api_data)
            doc["_id"] = fixture_id
            doc["events"] = []  # Start with empty events array
            
            # Add fixture-level tracking fields from models.py
            doc.update(create_activation_fields())
            
            self.fixtures_active.replace_one(
                {"_id": fixture_id},
                doc,
                upsert=True
            )
            return True
        except Exception as e:
            print(f"❌ Error activating fixture with data {fixture_id}: {e}")
            return False

    def activate_fixture(self, fixture_id: int) -> bool:
        """
        DEPRECATED: Use activate_fixture_with_data() instead.
        
        Move fixture from staging to active with EMPTY events array.
        This is the old time-based activation method, kept for backwards compatibility.
        """
        try:
            staging_doc = self.fixtures_staging.find_one({"_id": fixture_id})
            if not staging_doc:
                return False
            
            # Initialize with empty events array
            staging_doc["events"] = []
            
            # Add fixture-level tracking fields from models.py
            staging_doc.update(create_activation_fields())
            
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
                    event[EventFields.EVENT_ID] = f"{fixture_id}_{team_id}_{player_id}_{event_type}_{sequence}"
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
            live_event_ids = {e.get(EventFields.EVENT_ID) for e in live_events if e.get(EventFields.EVENT_ID)}
            active_event_ids = {e.get(EventFields.EVENT_ID) for e in active_events if e.get(EventFields.EVENT_ID)}
            
            # CASE 1: NEW - Events in live but not in active
            new_event_ids = live_event_ids - active_event_ids
            
            # CASE 2: INCOMPLETE - Events in both but not monitor complete
            active_map = {e.get(EventFields.EVENT_ID): e for e in active_events if e.get(EventFields.EVENT_ID)}
            incomplete_count = 0
            for event_id in live_event_ids:
                if event_id in active_map and not active_map[event_id].get(EventFields.MONITOR_COMPLETE, False):
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
        - _score_after: Score after this event
        - _scoring_team: Which team scored ("home" or "away")
        - _twitter_search: Search string for Twitter
        
        """
        try:
            # Use $max to ensure _last_activity only moves forward
            result = self.fixtures_active.update_one(
                {"_id": fixture_id},
                {
                    "$push": {"events": event_with_enhancements},
                    "$max": {FixtureFields.LAST_ACTIVITY: event_first_seen}
                }
            )
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error adding event to active fixture {fixture_id}: {e}")
            return False
    
    def update_event_stable_count(self, fixture_id: int, event_id: str, 
                                   stable_count: int, live_event: dict | None = None) -> bool:
        """
        Update existing event's monitor_count AND sync API data changes.
        
        The live_event parameter contains the latest API data for this event.
        We update fields that may change between API polls:
        - time (minute/extra time may shift)
        - assist (assister may be added/changed)
        - comments (additional info)
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            stable_count: New monitor count value
            live_event: Latest event data from API (optional, for syncing changes)
        """
        try:
            update_ops = {f"events.$.{EventFields.MONITOR_COUNT}": stable_count}
            
            # Sync API data changes if live_event provided
            if live_event:
                # Update time (minute may drift)
                if "time" in live_event:
                    update_ops["events.$.time"] = live_event["time"]
                # Update assist (may be added after initial detection)
                if "assist" in live_event:
                    update_ops["events.$.assist"] = live_event["assist"]
                # Update comments
                if "comments" in live_event:
                    update_ops["events.$.comments"] = live_event["comments"]
            
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                {"$set": update_ops}
            )
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error updating event {event_id}: {e}")
            return False
    
    def mark_event_monitor_complete(self, fixture_id: int, event_id: str, event_first_seen: datetime = None) -> bool:
        """
        Mark event as monitor complete (monitor_count reached threshold).
        Called by monitor activity when event is stable.
        Updates _last_activity to event's _first_seen (when goal was first detected).
        
        NOTE: _twitter_count stays at 0 until incremented.
        It gets INCREMENTED by DownloadWorkflow at END of each Twitter attempt.
        
        NOTE: We use _first_seen (not datetime.now()) because:
        - It reflects when the goal actually happened (within ~1 min of real time)
        - Combined with activate_fixture using scheduled kickoff time,
          this ensures fixtures are sorted by when goals occurred
        """
        try:
            update_ops = {
                "$set": {
                    f"events.$.{EventFields.MONITOR_COMPLETE}": True,
                }
            }
            
            # Update _last_activity to when the goal was first detected
            if event_first_seen:
                update_ops["$max"] = {FixtureFields.LAST_ACTIVITY: event_first_seen}
            
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                update_ops
            )
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error marking event {event_id} monitor complete: {e}")
            return False
    
    def increment_twitter_count_and_check_complete(
        self, 
        fixture_id: int, 
        event_id: str,
        total_attempts: int = 10
    ) -> dict:
        """
        DEPRECATED: Use check_and_mark_twitter_complete instead.
        
        This function uses counter-based tracking which has race condition issues.
        The new workflow-ID-based tracking (add_download_workflow + check_and_mark_twitter_complete)
        is idempotent and handles retries correctly.
        
        Keeping this for backward compatibility during transition.
        
        Atomically increment _twitter_count and set _twitter_complete when count reaches total.
        
        Called by:
        - DownloadWorkflow when a download completes
        - TwitterWorkflow when a search finds no videos (no download triggered)
        
        This ensures _twitter_complete is only set after ALL downloads finish,
        preventing the race condition where fixture moves to completed while
        downloads are still running.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            total_attempts: Total attempts expected (default 10)
        
        Returns:
            Dict with success, new_count, marked_complete
        """
        from datetime import datetime, timezone
        
        try:
            # Atomically increment the count
            result = self.fixtures_active.find_one_and_update(
                {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                {"$inc": {f"events.$.{EventFields.TWITTER_COUNT}": 1}},
                return_document=True  # Return the document AFTER update
            )
            
            if not result:
                print(f"❌ Event {event_id} not found in fixtures_active")
                return {"success": False, "new_count": 0, "marked_complete": False}
            
            # Find the event to get the new count
            new_count = 0
            for evt in result.get("events", []):
                if evt.get(EventFields.EVENT_ID) == event_id:
                    new_count = evt.get(EventFields.TWITTER_COUNT, 0)
                    break
            
            print(f"📊 Incremented _twitter_count to {new_count}/{total_attempts} for {event_id}")
            
            # Check if we should mark complete
            marked_complete = False
            if new_count >= total_attempts:
                self.fixtures_active.update_one(
                    {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                    {"$set": {
                        f"events.$.{EventFields.TWITTER_COMPLETE}": True,
                        f"events.$.{EventFields.TWITTER_COMPLETED_AT}": datetime.now(timezone.utc)
                    }}
                )
                marked_complete = True
                print(f"✅ Marked _twitter_complete=true for {event_id}")
            
            return {
                "success": True,
                "new_count": new_count,
                "marked_complete": marked_complete
            }
            
        except Exception as e:
            print(f"❌ Error incrementing twitter count for {event_id}: {e}")
            return {"success": False, "new_count": 0, "marked_complete": False}

    # ==========================================================================
    # WORKFLOW TRACKING (Workflow-ID-based)
    # ==========================================================================
    
    def add_monitor_workflow(self, fixture_id: int, event_id: str, workflow_id: str) -> bool:
        """
        Add a MonitorWorkflow ID to the event's _monitor_workflows array.
        Uses $addToSet for idempotency - adding the same ID twice is a no-op.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            workflow_id: The MonitorWorkflow ID (e.g., "monitor-27_01_2026-15:30")
            
        Returns:
            True if document was modified (new ID added), False otherwise
        """
        try:
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                {"$addToSet": {f"events.$.{EventFields.MONITOR_WORKFLOWS}": workflow_id}}
            )
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error adding monitor workflow {workflow_id} to {event_id}: {e}")
            return False

    def add_download_workflow(self, fixture_id: int, event_id: str, workflow_id: str) -> bool:
        """
        Add a DownloadWorkflow ID to the event's _download_workflows array.
        Uses $addToSet for idempotency - adding the same ID twice is a no-op.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID  
            workflow_id: The DownloadWorkflow ID (e.g., "download1-Everton-Barry-1379194_45_343684_Goal_1")
            
        Returns:
            True if document was modified (new ID added), False otherwise
        """
        try:
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                {"$addToSet": {f"events.$.{EventFields.DOWNLOAD_WORKFLOWS}": workflow_id}}
            )
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error adding download workflow {workflow_id} to {event_id}: {e}")
            return False

    def get_monitor_workflow_count(self, fixture_id: int, event_id: str) -> int:
        """
        Return the number of MonitorWorkflows that have processed this event.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            
        Returns:
            Length of _monitor_workflows array (0 if not found or empty)
        """
        try:
            fixture = self.fixtures_active.find_one(
                {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                {f"events.$": 1}
            )
            if fixture and fixture.get("events"):
                return len(fixture["events"][0].get(EventFields.MONITOR_WORKFLOWS, []))
            return 0
        except Exception as e:
            print(f"❌ Error getting monitor workflow count for {event_id}: {e}")
            return 0

    def get_download_workflow_count(self, fixture_id: int, event_id: str) -> int:
        """
        Return the number of DownloadWorkflows that have run for this event.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            
        Returns:
            Length of _download_workflows array (0 if not found or empty)
        """
        try:
            fixture = self.fixtures_active.find_one(
                {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                {f"events.$": 1}
            )
            if fixture and fixture.get("events"):
                return len(fixture["events"][0].get(EventFields.DOWNLOAD_WORKFLOWS, []))
            return 0
        except Exception as e:
            print(f"❌ Error getting download workflow count for {event_id}: {e}")
            return 0

    def get_monitor_complete(self, fixture_id: int, event_id: str) -> bool:
        """
        Return the current value of _monitor_complete for an event.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            
        Returns:
            Value of _monitor_complete (False if not found or not set)
        """
        try:
            fixture = self.fixtures_active.find_one(
                {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                {f"events.$": 1}
            )
            if fixture and fixture.get("events"):
                return fixture["events"][0].get(EventFields.MONITOR_COMPLETE, False)
            return False
        except Exception as e:
            print(f"❌ Error getting monitor complete status for {event_id}: {e}")
            return False

    def mark_monitor_complete(self, fixture_id: int, event_id: str) -> bool:
        """
        Set _monitor_complete = true for an event.
        Called by TwitterWorkflow at the VERY START to confirm it actually started.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            
        Returns:
            True if document was modified, False otherwise
        """
        try:
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                {"$set": {f"events.$.{EventFields.MONITOR_COMPLETE}": True}}
            )
            if result.modified_count > 0:
                print(f"✅ Set _monitor_complete=true for {event_id}")
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error setting monitor complete for {event_id}: {e}")
            return False

    def mark_twitter_complete(self, fixture_id: int, event_id: str) -> bool:
        """
        Set _twitter_complete = true for an event.
        Called by UploadWorkflow when _download_workflows count reaches 10.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            
        Returns:
            True if document was modified, False otherwise
        """
        try:
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                {"$set": {
                    f"events.$.{EventFields.TWITTER_COMPLETE}": True,
                    f"events.$.{EventFields.TWITTER_COMPLETED_AT}": datetime.now(timezone.utc)
                }}
            )
            if result.modified_count > 0:
                print(f"✅ Set _twitter_complete=true for {event_id}")
            return result.modified_count > 0
        except Exception as e:
            print(f"❌ Error setting twitter complete for {event_id}: {e}")
            return False

    def check_and_mark_twitter_complete(self, fixture_id: int, event_id: str, required_count: int = 10) -> dict:
        """
        Check if _download_workflows count >= required_count and mark _twitter_complete if so.
        
        This is the new idempotent replacement for increment_twitter_count_and_check_complete.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            required_count: Number of download workflows required for completion (default 10)
            
        Returns:
            Dict with count, was_already_complete, marked_complete
        """
        try:
            count = self.get_download_workflow_count(fixture_id, event_id)
            
            # Check if already complete
            fixture = self.fixtures_active.find_one(
                {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                {f"events.$": 1}
            )
            was_already_complete = False
            if fixture and fixture.get("events"):
                was_already_complete = fixture["events"][0].get(EventFields.TWITTER_COMPLETE, False)
            
            marked_complete = False
            if count >= required_count and not was_already_complete:
                self.mark_twitter_complete(fixture_id, event_id)
                marked_complete = True
                print(f"✅ {event_id}: {count}/{required_count} download workflows - marking twitter complete")
            elif count >= required_count:
                print(f"ℹ️ {event_id}: {count}/{required_count} download workflows - already complete")
            else:
                print(f"📊 {event_id}: {count}/{required_count} download workflows - not yet complete")
            
            return {
                "count": count,
                "was_already_complete": was_already_complete,
                "marked_complete": marked_complete
            }
        except Exception as e:
            print(f"❌ Error checking/marking twitter complete for {event_id}: {e}")
            return {"count": 0, "was_already_complete": False, "marked_complete": False}
    
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
                s3_videos = event.get(EventFields.S3_VIDEOS, [])
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
                {"$pull": {"events": {EventFields.EVENT_ID: event_id}}}
            )
            
            if result.modified_count > 0:
                print(f"🗑️ Removed VAR'd event from MongoDB: {event_id}")
                return True
            return False
            
        except Exception as e:
            print(f"❌ Error removing event {event_id}: {e}")
            return False
    

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
            
            # Preserve all our enhanced fixture-level fields (underscore prefixed)
            for key in active_fixture:
                if key.startswith("_") and key != "_id":
                    update_doc[key] = active_fixture[key]
            
            # Build lookup of live events by event_id for merging
            # Live events already have _event_id from store_live_fixture()
            live_events = live_fixture.get("events", [])
            live_events_by_id = {}
            for live_event in live_events:
                event_id = live_event.get(EventFields.EVENT_ID)
                if event_id:
                    live_events_by_id[event_id] = live_event
            
            # Merge API updates into enhanced events while preserving our fields
            enhanced_events = active_fixture.get("events", [])
            for event in enhanced_events:
                event_id = event.get(EventFields.EVENT_ID)
                if event_id and event_id in live_events_by_id:
                    live_event = live_events_by_id[event_id]
                    # Update raw API fields (non _ prefixed) from live
                    for key, value in live_event.items():
                        if not key.startswith("_"):
                            event[key] = value
            
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
                    print(f"✅ No new videos to add for {event_id} (in {collection_name})")
                    return True
                
                # Get existing videos to check for duplicates
                fixture = collection.find_one(
                    {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id}
                )
                if not fixture:
                    continue  # Try next collection
                
                print(f"📦 Found fixture in {collection_name} for {event_id}")
                
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
                            print(f"📈 Existing video {url.split('/')[-1]} popularity {existing_pop} → {new_pop}")
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
                            print(f"📈 Updated popularity for {len(popularity_updates)} existing videos in {event_id} ({collection_name})")
                            break
                
                if not new_videos:
                    if popularity_updates:
                        print(f"✅ Updated {len(popularity_updates)} existing videos, no new videos to add for {event_id}")
                    else:
                        print(f"✅ All {len(video_objects)} videos already exist for {event_id}, skipping duplicates")
                    return True
                
                if len(new_videos) < len(video_objects):
                    skipped = len(video_objects) - len(new_videos) - len(popularity_updates)
                    if skipped > 0:
                        print(f"⚠️ Filtered out {skipped} duplicate videos for {event_id}")
                
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
                    print(f"⚠️ 0 documents modified when adding videos to {event_id} ({collection_name})")
                    continue  # Try next collection
                
                print(f"✅ Added {len(new_videos)} videos to {event_id} ({collection_name})")
                return True
                
            except Exception as e:
                print(f"⚠️ Error adding videos in {collection_name} for {event_id}: {e}")
                continue
        
        # Not found in either collection
        msg = f"❌ Event {event_id} not found in fixtures_active or fixtures_completed"
        print(msg)
        raise RuntimeError(msg)
    
    def recalculate_video_ranks(self, fixture_id: int, event_id: str) -> bool:
        """
        Recalculate ranks for all videos in an event.
        Sorts by popularity (desc) then file_size (desc) - larger files = better quality.
        Rank 1 = best video.
        
        Only checks fixtures_active since UploadWorkflow (which calls this) is BLOCKING
        inside DownloadWorkflow, which must complete before _twitter_count is incremented.
        Fixture cannot move to completed until all DownloadWorkflows finish.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            
        Returns:
            True if successful
        """
        try:
            fixture = self.fixtures_active.find_one({"_id": fixture_id})
            if not fixture:
                print(f"⚠️ Fixture {fixture_id} not found in fixtures_active")
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
                print(f"⚠️ Event {event_id} not found")
                return False
            
            # Get videos and sort them
            videos = event.get(EventFields.S3_VIDEOS, [])
            if not videos:
                return True
            
            # Sort by popularity (desc) then file_size (desc) - bigger file = better quality
            videos_sorted = sorted(
                videos, 
                key=lambda v: (v.get("popularity", 1), v.get("file_size", 0)),
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
    
    def has_winner_data(self, fixture_doc: dict) -> bool:
        """
        Check if fixture has winner data populated.
        
        Winner data is in teams.home.winner / teams.away.winner:
        - True = this team won
        - False = this team lost
        - None = draw OR not yet determined
        
        For knockout matches (penalty shootouts), winner will eventually be True/False.
        For league matches, can be a draw (both None) which is still valid.
        """
        teams = fixture_doc.get("teams", {})
        home_winner = teams.get("home", {}).get("winner")
        away_winner = teams.get("away", {}).get("winner")
        
        # Winner data exists if either team has a non-None winner value
        # This handles both wins (True/False) AND draws (None/None after match)
        # The key insight: API returns winner=None during match, then sets it after
        
        # For draws, we rely on the completion counter
        # For wins, at least one will be True
        return home_winner is True or away_winner is True
    
    def increment_completion_count(self, fixture_id: int) -> dict:
        """
        Increment the completion counter for a fixture.
        
        Similar to event debouncing:
        - _completion_count: Number of times we've seen completed status
        - _completion_complete: True when ready to move to fixtures_completed
        - _completion_first_seen: When we first saw the completed status
        
        Returns dict with:
        - completion_count: Current count after increment
        - completion_complete: Whether fixture is ready for completion
        - winner_exists: Whether winner data is populated
        """
        try:
            fixture_doc = self.fixtures_active.find_one({"_id": fixture_id})
            if not fixture_doc:
                return {"completion_count": 0, "completion_complete": False, "winner_exists": False}
            
            current_count = fixture_doc.get(FixtureFields.COMPLETION_COUNT, 0)
            winner_exists = self.has_winner_data(fixture_doc)
            
            # If already at 3 or more, don't increment further
            if current_count >= 3:
                completion_complete = True
                return {
                    "completion_count": current_count,
                    "completion_complete": completion_complete,
                    "winner_exists": winner_exists,
                }
            
            if current_count == 0:
                # First time seeing completed status
                self.fixtures_active.update_one(
                    {"_id": fixture_id},
                    {
                        "$set": {
                            FixtureFields.COMPLETION_COUNT: 1,
                            FixtureFields.COMPLETION_COMPLETE: False,
                            FixtureFields.COMPLETION_FIRST_SEEN: datetime.now(timezone.utc),
                        }
                    }
                )
                new_count = 1
            else:
                # Increment counter
                new_count = current_count + 1
                self.fixtures_active.update_one(
                    {"_id": fixture_id},
                    {"$set": {FixtureFields.COMPLETION_COUNT: new_count}}
                )
            
            # Completion ready when count >= 3 OR winner data exists
            completion_complete = new_count >= 3 or winner_exists
            
            if completion_complete and not fixture_doc.get(FixtureFields.COMPLETION_COMPLETE):
                self.fixtures_active.update_one(
                    {"_id": fixture_id},
                    {"$set": {FixtureFields.COMPLETION_COMPLETE: True}}
                )
            
            return {
                "completion_count": new_count,
                "completion_complete": completion_complete,
                "winner_exists": winner_exists,
            }
        except Exception as e:
            print(f"❌ Error incrementing completion count for {fixture_id}: {e}")
            return {"completion_count": 0, "completion_complete": False, "winner_exists": False}
    
    def is_completion_ready(self, fixture_id: int) -> bool:
        """
        Check if fixture is ready for completion (all criteria met).
        
        Criteria:
        1. _completion_complete = True (counter >= 3 OR winner exists)
        2. All events have _monitor_complete = True
        3. All events have _twitter_complete = True
        """
        try:
            fixture_doc = self.fixtures_active.find_one({"_id": fixture_id})
            if not fixture_doc:
                return False
            
            # Check completion counter
            if not fixture_doc.get(FixtureFields.COMPLETION_COMPLETE, False):
                return False
            
            # Check events
            events = fixture_doc.get("events", [])
            enhanced_events = [e for e in events if e.get(EventFields.EVENT_ID)]
            
            if enhanced_events:
                valid_events = [
                    e for e in enhanced_events 
                    if not e.get(EventFields.REMOVED, False) 
                    and "None" not in e.get(EventFields.EVENT_ID, "")
                ]
                
                if valid_events:
                    all_monitored = all(e.get(EventFields.MONITOR_COMPLETE, False) for e in valid_events)
                    all_twitter_done = all(e.get(EventFields.TWITTER_COMPLETE, False) for e in valid_events)
                    
                    if not (all_monitored and all_twitter_done):
                        return False
            
            return True
        except Exception as e:
            print(f"❌ Error checking completion ready for {fixture_id}: {e}")
            return False

    def complete_fixture(self, fixture_id: int) -> bool:
        """
        Move fixture from active to completed.
        
        Prerequisites (checked by is_completion_ready):
        - _completion_complete: true (counter >= 3 OR winner data exists)
        - All valid events have _monitor_complete: true
        - All valid events have _twitter_complete: true
        
        This method just does the move - use is_completion_ready to check first.
        """
        try:
            fixture_doc = self.fixtures_active.find_one({"_id": fixture_id})
            if not fixture_doc:
                print(f"⚠️ Fixture {fixture_id} not found in active")
                return False
            
            # Add completion timestamp
            fixture_doc[FixtureFields.COMPLETED_AT] = datetime.now(timezone.utc)
            
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

    # === Team Aliases Methods ===
    
    def get_team_alias(self, team_id: int) -> dict | None:
        """
        Get cached team alias by team_id.
        
        Args:
            team_id: API-Football team ID
            
        Returns:
            TeamAlias document or None if not cached
        """
        try:
            return self.team_aliases.find_one({"_id": team_id})
        except Exception as e:
            print(f"❌ Error getting team alias for {team_id}: {e}")
            return None
    
    def upsert_team_alias(
        self,
        team_id: int,
        team_name: str,
        national: bool,
        twitter_aliases: List[str],
        model: str,
        country: str | None = None,
        city: str | None = None,
        wikidata_qid: str | None = None,
        wikidata_aliases: List[str] | None = None,
    ) -> bool:
        """
        Create or update team alias in cache.
        
        Args:
            team_id: API-Football team ID (used as _id)
            team_name: Full team name
            national: True if national team, False if club
            twitter_aliases: Final aliases for Twitter search
            model: LLM model used (or "fallback")
            country: Country from API-Football (e.g., "England")
            city: City from venue data (e.g., "Newcastle upon Tyne")
            wikidata_qid: Wikidata QID if found
            wikidata_aliases: Raw aliases from Wikidata
            
        Returns:
            True if successful
        """
        try:
            team_type = "national" if national else "club"  # Derive team_type from national
            
            update_data = {
                TeamAliasFields.TEAM_NAME: team_name,
                TeamAliasFields.TEAM_TYPE: team_type,
                TeamAliasFields.NATIONAL: national,
                TeamAliasFields.TWITTER_ALIASES: twitter_aliases,
                TeamAliasFields.MODEL: model,
                TeamAliasFields.UPDATED_AT: datetime.now(timezone.utc),
            }
            
            # Add optional fields from API
            if country:
                update_data[TeamAliasFields.COUNTRY] = country
            if city:
                update_data[TeamAliasFields.CITY] = city
            if wikidata_qid:
                update_data[TeamAliasFields.WIKIDATA_QID] = wikidata_qid
            if wikidata_aliases:
                update_data[TeamAliasFields.WIKIDATA_ALIASES] = wikidata_aliases
            
            self.team_aliases.update_one(
                {"_id": team_id},
                {
                    "$set": update_data,
                    "$setOnInsert": {TeamAliasFields.CREATED_AT: datetime.now(timezone.utc)},
                },
                upsert=True,
            )
            return True
        except Exception as e:
            print(f"❌ Error upserting team alias for {team_id}: {e}")
            return False
    
    def get_all_team_aliases(self) -> List[dict]:
        """Get all cached team aliases."""
        try:
            return list(self.team_aliases.find({}))
        except Exception as e:
            print(f"❌ Error getting all team aliases: {e}")
            return []
    
    def delete_team_alias(self, team_id: int) -> bool:
        """Delete a team alias from cache (for testing/refresh)."""
        try:
            result = self.team_aliases.delete_one({"_id": team_id})
            return result.deleted_count > 0
        except Exception as e:
            print(f"❌ Error deleting team alias for {team_id}: {e}")
            return False
    
    def clear_team_aliases(self) -> int:
        """Clear all team aliases (for testing/refresh). Returns count deleted."""
        try:
            result = self.team_aliases.delete_many({})
            return result.deleted_count
        except Exception as e:
            print(f"❌ Error clearing team aliases: {e}")
            return 0

    # === Top-Flight Teams Cache ===
    
    def get_top_flight_cache(self) -> dict | None:
        """
        Get cached top-flight team IDs.
        
        Returns:
            Dict with 'team_ids', 'season', 'cached_at' or None if not cached
        """
        try:
            doc = self.db.top_flight_cache.find_one({"_id": "top_5_leagues"})
            return doc
        except Exception as e:
            print(f"❌ Error getting top-flight cache: {e}")
            return None
    
    def save_top_flight_cache(self, team_ids: list[int], season: int) -> bool:
        """
        Save top-flight team IDs to cache.
        
        Args:
            team_ids: List of team IDs from all top 5 leagues
            season: Season year (e.g., 2025)
        """
        try:
            self.db.top_flight_cache.update_one(
                {"_id": "top_5_leagues"},
                {
                    "$set": {
                        "team_ids": team_ids,
                        "season": season,
                        "cached_at": datetime.now(timezone.utc),
                        "count": len(team_ids),
                    }
                },
                upsert=True,
            )
            return True
        except Exception as e:
            print(f"❌ Error saving top-flight cache: {e}")
            return False
    
    def clear_top_flight_cache(self) -> bool:
        """Clear the top-flight teams cache (forces refresh on next call)."""
        try:
            self.db.top_flight_cache.delete_one({"_id": "top_5_leagues"})
            return True
        except Exception as e:
            print(f"❌ Error clearing top-flight cache: {e}")
            return False
