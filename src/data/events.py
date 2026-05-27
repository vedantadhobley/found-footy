"""Event-level methods on fixtures_active (lifecycle, workflow tracking, telemetry)."""

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
from src.utils.orchestration_config import MONITOR_DROP_THRESHOLD, TWITTER_REQUIRED_DOWNLOADS


class EventsMixin:
    """Mixin: per-event $addToSet workflow tracking + telemetry + completion marking."""

    def add_event_to_active(self, fixture_id: int, event_with_enhancements: dict, event_first_seen: datetime) -> bool:
        """
        Add NEW event to fixtures_active events array with enhancement fields.
        Called by debounce job when detecting a new event (CASE 1).
        
        Enhancement fields in event:
        - _event_id: Unique event identifier
        - _monitor_count: Number of consecutive unchanged polls
        - _monitor_complete: True when monitor_count reaches threshold
        - _download_complete: True when videos downloaded
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
            _log_error("add_event_error", "Error adding event to active fixture", fixture_id=fixture_id, error=str(e), exc=e)
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
            _log_error("update_event_error", "Error updating event", event_id=event_id, error=str(e), exc=e)
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
            _log_error("mark_monitor_complete_error", "Error marking event monitor complete", event_id=event_id, error=str(e), exc=e)
            return False

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
            _log_error("add_monitor_workflow_error", "Error adding monitor workflow", workflow_id=workflow_id, event_id=event_id, error=str(e), exc=e)
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
            _log_error("add_download_workflow_error", "Error adding download workflow", workflow_id=workflow_id, event_id=event_id, error=str(e), exc=e)
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
            _log_error("get_monitor_count_error", "Error getting monitor workflow count", event_id=event_id, error=str(e), exc=e)
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
            _log_error("get_download_count_error", "Error getting download workflow count", event_id=event_id, error=str(e), exc=e)
            return 0

    def clear_drop_workflows(self, fixture_id: int, event_id: str) -> bool:
        """
        Clear _drop_workflows when event is present in API.
        
        This performs a FULL RESET - if an event reappears after being missing,
        we clear all accumulated drop workflows so the counter starts from scratch.
        This handles API flickering where an event temporarily disappears.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            
        Returns:
            True if document was modified (had workflows to clear), False otherwise
        """
        try:
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                {"$set": {f"events.$.{EventFields.DROP_WORKFLOWS}": []}}
            )
            return result.modified_count > 0
        except Exception as e:
            _log_error("clear_drop_workflows_error", "Error clearing drop workflows", event_id=event_id, error=str(e), exc=e)
            return False

    def add_drop_workflow_and_check(
        self, fixture_id: int, event_id: str, workflow_id: str
    ) -> tuple[int, bool]:
        """
        Add workflow ID to _drop_workflows and check if deletion threshold reached.
        
        Uses $addToSet for idempotency - the same workflow ID won't be added twice.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            workflow_id: The MonitorWorkflow ID that saw this event MISSING
            
        Returns:
            Tuple of (count, should_delete):
            - count: Number of workflows that have seen event missing
            - should_delete: True if count >= 3 (threshold reached)
        """
        # Pulled from orchestration_config (replaces the previous local constant)
        DROP_THRESHOLD = MONITOR_DROP_THRESHOLD

        try:
            # Atomic $addToSet + read in one round-trip. find_one_and_update with
            # ReturnDocument.AFTER returns the document with our update applied,
            # so the count we observe always includes our own workflow_id.
            #
            # Concurrent callers from different monitor cycles can both observe
            # count >= DROP_THRESHOLD and each return should_delete=True — that's
            # fine because the caller's actions (mark_event_removed, $pull event)
            # are themselves idempotent. We just avoid the previous wasteful
            # 2-round-trip pattern.
            fixture = self.fixtures_active.find_one_and_update(
                {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                {"$addToSet": {f"events.$.{EventFields.DROP_WORKFLOWS}": workflow_id}},
                projection={f"events.$": 1},
                return_document=ReturnDocument.AFTER,
            )

            if not fixture or not fixture.get("events"):
                return (0, False)

            drop_workflows = fixture["events"][0].get(EventFields.DROP_WORKFLOWS, [])
            count = len(drop_workflows)
            should_delete = count >= DROP_THRESHOLD

            return (count, should_delete)
        except Exception as e:
            _log_error("add_drop_workflow_error", "Error adding drop workflow", workflow_id=workflow_id, event_id=event_id, error=str(e), exc=e)
            return (0, False)

    def get_drop_workflow_count(self, fixture_id: int, event_id: str) -> int:
        """
        Return the number of MonitorWorkflows that have seen this event MISSING.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            
        Returns:
            Length of _drop_workflows array (0 if not found or empty)
        """
        try:
            fixture = self.fixtures_active.find_one(
                {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                {f"events.$": 1}
            )
            if fixture and fixture.get("events"):
                return len(fixture["events"][0].get(EventFields.DROP_WORKFLOWS, []))
            return 0
        except Exception as e:
            _log_error("get_drop_count_error", "Error getting drop workflow count", event_id=event_id, error=str(e), exc=e)
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
            _log_error("get_monitor_complete_error", "Error getting monitor complete status", event_id=event_id, error=str(e), exc=e)
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
                _log_info("monitor_complete_set", "Set _monitor_complete=true", event_id=event_id)
            return result.modified_count > 0
        except Exception as e:
            _log_error("set_monitor_complete_error", "Error setting monitor complete", event_id=event_id, error=str(e), exc=e)
            return False

    def increment_event_telemetry(
        self,
        fixture_id: int,
        event_id: str,
        increments: Optional[Dict[str, int]] = None,
        set_fields: Optional[Dict[str, Any]] = None,
        min_fields: Optional[Dict[str, Any]] = None,
    ) -> bool:
        """
        Atomically update the per-event `_telemetry` document.

        All three update kinds compose into a single MongoDB write — atomic
        per call but multiple callers can race safely; counter increments
        always sum and timestamps always settle to the earliest via $min.

        Args:
            fixture_id, event_id: target event
            increments: {field_path: int} — applied via $inc. Field paths
                are relative to the telemetry subdoc, e.g.:
                  {"search_attempts": 1}
                  {"download_failures_by_class.VideoGeoRestrictedError": 1}
            set_fields: {field_path: value} — unconditional $set. Last
                writer wins; only use for values where that's the intent
                (e.g. primary_failure_class derived after the fact).
            min_fields: {field_path: value} — $min semantics, suitable
                for first_seen_at / first_s3_upload_at where the first
                writer's timestamp should win.

        Returns:
            True if a document was modified, False otherwise.
        """
        ops: Dict[str, Dict[str, Any]] = {}
        if increments:
            ops["$inc"] = {
                f"events.$.{EventFields.TELEMETRY}.{k}": v for k, v in increments.items()
            }
        if set_fields:
            ops["$set"] = {
                f"events.$.{EventFields.TELEMETRY}.{k}": v for k, v in set_fields.items()
            }
        if min_fields:
            ops["$min"] = {
                f"events.$.{EventFields.TELEMETRY}.{k}": v for k, v in min_fields.items()
            }
        if not ops:
            return False
        try:
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                ops,
            )
            return result.modified_count > 0
        except Exception as e:
            _log_error("telemetry_update_failed",
                       "Failed to update event telemetry",
                       fixture_id=fixture_id, event_id=event_id,
                       error=str(e), exc=e)
            return False

    def get_event_telemetry(self, fixture_id: int, event_id: str) -> Optional[Dict[str, Any]]:
        """Read the telemetry subdoc for one event (or None if event not found)."""
        try:
            doc = self.fixtures_active.find_one(
                {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                {f"events.$": 1},
            )
            if not doc or not doc.get("events"):
                return None
            return doc["events"][0].get(EventFields.TELEMETRY, {}) or {}
        except Exception as e:
            _log_error("get_telemetry_failed",
                       "Failed to read event telemetry",
                       fixture_id=fixture_id, event_id=event_id,
                       error=str(e), exc=e)
            return None

    def mark_download_complete(self, fixture_id: int, event_id: str) -> bool:
        """
        Set _download_complete = true for an event.
        Called by UploadWorkflow when _download_workflows count reaches 10.
        
        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            
        Returns:
            True if document was modified, False otherwise
        """
        try:
            now = datetime.now(timezone.utc)
            result = self.fixtures_active.update_one(
                {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                {"$set": {
                    f"events.$.{EventFields.DOWNLOAD_COMPLETE}": True,
                    f"events.$.{EventFields.DOWNLOAD_COMPLETED_AT}": now,
                }}
            )
            if result.modified_count > 0:
                _log_info("download_complete_set", "Set _download_complete=true", event_id=event_id)
            return result.modified_count > 0
        except Exception as e:
            _log_error("set_download_complete_error", "Error setting download complete", event_id=event_id, error=str(e), exc=e)
            return False

    def check_and_mark_download_complete(self, fixture_id: int, event_id: str, required_count: int = TWITTER_REQUIRED_DOWNLOADS) -> dict:
        """
        Check if _download_workflows count >= required_count and mark
        _download_complete if so.

        Two-stage (read + conditional update). The original Sprint-2
        attempt used `$expr` inside `$elemMatch` for single-round-trip
        atomicity, but MongoDB doesn't permit `$expr` inside element
        matches (surfaced live during Crystal Palace v Rayo, 2026-05-27,
        first goal: every DLWF's exit-check raised
        "$expr can only be applied to the top-level document").

        The two-stage shape preserves the correctness invariant:
          1. Read the event's current count + complete flag.
          2. If count >= required AND not yet complete, attempt a
             $set guarded by `_download_complete: {$ne: true}` —
             so a concurrent racer's write either: (a) succeeds first
             and ours becomes a no-op (modified_count=0), or (b) loses
             and ours wins. Exactly one caller observes `marked_complete:
             True`, which is what UploadWorkflow and DownloadWorkflow's
             finally both depend on for clean exit accounting.

        Args:
            fixture_id: Fixture ID
            event_id: Event ID
            required_count: Number of download workflows required for completion

        Returns:
            Dict with count, was_already_complete, marked_complete
        """
        try:
            # Stage 1 — read current state for this event.
            doc = self.fixtures_active.find_one(
                {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
                {f"events.$": 1},
            )
            if not doc or not doc.get("events"):
                return {"count": 0, "was_already_complete": False, "marked_complete": False}

            ev = doc["events"][0]
            count = len(ev.get(EventFields.DOWNLOAD_WORKFLOWS, []) or [])
            was_complete = bool(ev.get(EventFields.DOWNLOAD_COMPLETE, False))

            # Threshold not yet reached — log + return without writing.
            if count < required_count:
                _log_info("download_not_complete",
                          "Download workflows not yet complete",
                          event_id=event_id, count=count, required=required_count)
                return {"count": count, "was_already_complete": was_complete, "marked_complete": False}

            # Already complete — log + return without writing.
            if was_complete:
                _log_info("download_already_complete",
                          "Already complete — no-op",
                          event_id=event_id, count=count, required=required_count)
                return {"count": count, "was_already_complete": True, "marked_complete": False}

            # Stage 2 — conditional set. The $ne guard makes this idempotent
            # under concurrent races (only one caller sees modified_count==1).
            result = self.fixtures_active.update_one(
                {
                    "_id": fixture_id,
                    "events": {
                        "$elemMatch": {
                            EventFields.EVENT_ID: event_id,
                            EventFields.DOWNLOAD_COMPLETE: {"$ne": True},
                        }
                    },
                },
                {"$set": {f"events.$.{EventFields.DOWNLOAD_COMPLETE}": True}},
            )

            if result.modified_count > 0:
                _log_info("download_workflows_complete",
                          "Download workflows threshold reached — marking complete",
                          event_id=event_id, count=count, required=required_count)
                return {"count": count, "was_already_complete": False, "marked_complete": True}

            # We lost the race — another caller flipped it between stage 1 and 2.
            return {"count": count, "was_already_complete": True, "marked_complete": False}

        except Exception as e:
            _log_error("check_download_complete_error", "Error checking/marking download complete", event_id=event_id, error=str(e), exc=e)
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
        from src.data.s3_store import FootyS3Store, get_s3_store
        
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
                    s3_store = get_s3_store()
                    deleted_count = 0
                    failed_count = 0
                    for s3_url in s3_urls:
                        # Extract S3 key from relative path
                        # Format: /video/footy-videos/{key}
                        try:
                            if not s3_url.startswith("/video/footy-videos/"):
                                _log_warning("unexpected_s3_url", "Unexpected S3 URL format", s3_url=s3_url)
                                failed_count += 1
                                continue
                            key = s3_url.replace("/video/footy-videos/", "")
                            s3_store.delete_video(key)
                            deleted_count += 1
                            _log_info("var_video_deleted", "Deleted S3 video for VAR'd goal", key=key)
                        except Exception as e:
                            failed_count += 1
                            _log_warning("s3_delete_failed", "Failed to delete S3 video", s3_url=s3_url, error=str(e))
                    
                    # Log summary of S3 deletions
                    if failed_count > 0:
                        _log_warning("var_s3_delete_summary", f"VAR S3 cleanup: {deleted_count} deleted, {failed_count} failed",
                                     event_id=event_id, deleted=deleted_count, failed=failed_count)
            
            # Step 3: Remove event from MongoDB array entirely
            result = self.fixtures_active.update_one(
                {"_id": fixture_id},
                {"$pull": {"events": {EventFields.EVENT_ID: event_id}}}
            )
            
            if result.modified_count > 0:
                _log_info("var_event_removed", "Removed VAR'd event from MongoDB", event_id=event_id)
                return True
            return False
            
        except Exception as e:
            _log_error("remove_event_error", "Error removing event", event_id=event_id, error=str(e), exc=e)
            return False
