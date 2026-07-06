"""Fixtures collection methods (staging / live / active / completed)."""

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


class FixturesMixin:
    """Mixin: staging/live/active/completed CRUD + completion flow."""

    def fixtures_insert_staging(self, raw_fixtures: List[dict]) -> int:
        """Insert fixtures into staging collection"""
        return self.bulk_insert_fixtures(raw_fixtures, "fixtures_staging")

    def get_staging_fixtures(self) -> List[dict]:
        """Get all fixtures in staging"""
        try:
            return list(self.fixtures_staging.find({}))
        except Exception as e:
            _log_error("staging_fixtures_error", "Error getting staging fixtures", error=str(e), exc=e)
            return []

    def get_staging_fixture_ids(self) -> List[int]:
        """Get all staging fixture IDs"""
        try:
            fixtures = list(self.fixtures_staging.find({}, {"_id": 1}))
            return [f["_id"] for f in fixtures]
        except Exception as e:
            _log_error("staging_ids_error", "Error getting staging fixture IDs", error=str(e), exc=e)
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
            _log_error("update_staging_error", "Error updating staging fixture", fixture_id=fixture_id, error=str(e), exc=e)
            return False

    def activate_fixture_with_data(self, fixture_id: int, api_data: dict) -> bool:
        """
        Move fixture from staging to active using fresh API data.
        
        Called when we detect a status change from NS/TBD to any other status.
        Uses create_activation_fields() from models.py to set tracking fields.
        
        See src/data/models.py for complete field documentation.
        """
        try:
            # Build active document from fresh API data
            doc = dict(api_data)
            doc["_id"] = fixture_id
            doc["events"] = []  # Start with empty events array

            # Add fixture-level tracking fields from models.py
            doc.update(create_activation_fields())

            # Insert into active FIRST. If this fails, the fixture stays in
            # staging and we can retry — no data loss. The previous order
            # (delete staging → insert active) could lose the fixture entirely
            # if the active insert failed for any reason.
            self.fixtures_active.replace_one(
                {"_id": fixture_id},
                doc,
                upsert=True
            )

            # Now remove from staging. If THIS fails, the fixture briefly
            # exists in both collections — duplicate but recoverable. Next
            # ingest cycle's "skip existing" logic handles it; if not,
            # cleanup_old_fixtures sweeps eventually.
            staging_result = self.fixtures_staging.delete_one({"_id": fixture_id})
            if staging_result.deleted_count == 0:
                _log_warning("fixture_not_in_staging", "Fixture not found in staging (may have been activated by a concurrent monitor cycle)", fixture_id=fixture_id)

            return True
        except Exception as e:
            _log_error("activate_fixture_error", "Error activating fixture with data", fixture_id=fixture_id, error=str(e), exc=e)
            return False

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
            _log_error("store_live_error", "Error storing live fixture", fixture_id=fixture_id, error=str(e), exc=e)
            return False

    def get_live_fixture(self, fixture_id: int) -> dict | None:
        """Get raw API data from fixtures_live"""
        try:
            return self.fixtures_live.find_one({"_id": fixture_id})
        except Exception as e:
            _log_error("get_live_error", "Error getting live fixture", fixture_id=fixture_id, error=str(e), exc=e)
            return None

    def get_fixture_from_active(self, fixture_id: int) -> dict | None:
        """Get fixture document from fixtures_active"""
        try:
            return self.fixtures_active.find_one({"_id": fixture_id})
        except Exception as e:
            _log_error("get_active_error", "Error getting fixture from active", fixture_id=fixture_id, error=str(e), exc=e)
            return None

    def get_active_fixtures(self) -> List[dict]:
        """Get all active fixtures for monitoring"""
        try:
            return list(self.fixtures_active.find({}))
        except Exception as e:
            _log_error("get_active_fixtures_error", "Error getting active fixtures", error=str(e), exc=e)
            return []

    def get_active_fixture_ids(self) -> List[int]:
        """Get all active fixture IDs"""
        try:
            fixtures = list(self.fixtures_active.find({}, {"_id": 1}))
            return [f["_id"] for f in fixtures]
        except Exception as e:
            _log_error("get_active_ids_error", "Error getting active fixture IDs", error=str(e), exc=e)
            return []

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
            import traceback
            _log_error("compare_error", "Error comparing live vs active", fixture_id=fixture_id, error=str(e), traceback=traceback.format_exc(), exc=e)
            return {"needs_debounce": False, "reason": "error", "error": str(e)}

    def sync_fixture_data(self, fixture_id: int) -> bool:
        """
        Sync fixture top-level data from fixtures_live to fixtures_active.
        Also updates raw event fields from API while preserving our enhanced fields.
        Called after successful debounce to keep fixture data fresh.
        
        Updates: fixture, teams, league, goals, score, status, etc.
        Updates in events: assist, detail, comments, time (raw API fields)
        Preserves in events: all _ prefixed fields (our enhancements)
        
        Also regenerates display titles for all events.
        
        IMPORTANT: Sets _last_activity when status changes from NS/TBD to a live status.
        This is the moment the match actually kicks off and should appear at top of frontend.
        """
        from src.utils.fixture_status import get_staging_statuses, get_active_statuses
        from datetime import datetime, timezone
        
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
            
            # Always update _last_monitor for observability
            update_doc[FixtureFields.LAST_MONITOR] = datetime.now(timezone.utc)
            
            # Check if match just started (NS/TBD → active status)
            old_status = active_fixture.get("fixture", {}).get("status", {}).get("short", "")
            new_status = live_fixture.get("fixture", {}).get("status", {}).get("short", "")
            
            staging_statuses = get_staging_statuses()  # ["NS", "TBD"]
            active_statuses = get_active_statuses()    # ["1H", "HT", "2H", etc.]
            
            if old_status in staging_statuses and new_status in active_statuses:
                # Match just started! Set _last_activity NOW
                # This is when the fixture should jump to the top of the frontend
                _log_info("match_kickoff", "Match KICKED OFF - setting _last_activity",
                         fixture_id=fixture_id, old_status=old_status, new_status=new_status)
                update_doc[FixtureFields.LAST_ACTIVITY] = datetime.now(timezone.utc)
            
            # Preserve all our enhanced fixture-level fields (underscore prefixed)
            for key in active_fixture:
                if key.startswith("_") and key != "_id":
                    # Don't overwrite _last_activity if we just set it above
                    if key == FixtureFields.LAST_ACTIVITY and FixtureFields.LAST_ACTIVITY in update_doc:
                        continue
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
                _log_info("fixture_synced", "Synced fixture", fixture_id=fixture_id, status=live_status)
            return result.modified_count > 0
        except Exception as e:
            _log_error("sync_fixture_error", "Error syncing fixture data", fixture_id=fixture_id, error=str(e), exc=e)
            return False

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
            _log_error("increment_completion_error", f"Error incrementing completion count for {fixture_id}", error=str(e), fixture_id=fixture_id, exc=e)
            return {"completion_count": 0, "completion_complete": False, "winner_exists": False}

    def is_completion_ready(self, fixture_id: int) -> bool:
        """
        Check if fixture is ready for completion (all criteria met).
        
        Criteria:
        1. _completion_complete = True (counter >= 3 OR winner exists)
        2. All events have _monitor_complete = True
        3. All events have _download_complete = True
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
                    all_download_done = all(e.get(EventFields.DOWNLOAD_COMPLETE, False) for e in valid_events)
                    
                    if not (all_monitored and all_download_done):
                        return False
            
            return True
        except Exception as e:
            _log_error("completion_ready_check_error", f"Error checking completion ready for {fixture_id}", error=str(e), fixture_id=fixture_id, exc=e)
            return False

    def complete_fixture(self, fixture_id: int) -> bool:
        """
        Move fixture from active to completed.
        
        Prerequisites (checked by is_completion_ready):
        - _completion_complete: true (counter >= 3 OR winner data exists)
        - All valid events have _monitor_complete: true
        - All valid events have _download_complete: true
        
        This method just does the move - use is_completion_ready to check first.
        """
        try:
            fixture_doc = self.fixtures_active.find_one({"_id": fixture_id})
            if not fixture_doc:
                _log_warning("fixture_not_found_complete", f"Fixture {fixture_id} not found in active for completion", fixture_id=fixture_id)
                return False

            # Add completion timestamp
            fixture_doc[FixtureFields.COMPLETED_AT] = datetime.now(timezone.utc)

            # Insert (or replace) into completed FIRST. The previous code used
            # insert_one which would raise DuplicateKeyError if the fixture
            # was already migrated by a prior call — leaving us in a state
            # where active+completed both have it. replace_one(upsert=True)
            # is idempotent on retry.
            self.fixtures_completed.replace_one(
                {"_id": fixture_id},
                fixture_doc,
                upsert=True,
            )

            # Now remove from active + live. If either delete fails, the
            # fixture sits briefly in both collections — but the data is
            # safely in completed, and the next monitor cycle's
            # complete_fixture call is now an idempotent no-op via the
            # upsert above.
            self.fixtures_active.delete_one({"_id": fixture_id})
            self.fixtures_live.delete_one({"_id": fixture_id})

            return True
        except Exception as e:
            _log_error("complete_fixture_error", f"Error completing fixture {fixture_id}", error=str(e), fixture_id=fixture_id, exc=e)
            return False

    def get_completed_fixtures(self) -> List[dict]:
        """Get all completed fixtures"""
        try:
            return list(self.fixtures_completed.find({}))
        except Exception as e:
            _log_error("get_completed_fixtures_error", "Error getting completed fixtures", error=str(e), exc=e)
            return []
