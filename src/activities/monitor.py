"""
Monitor Activities
==================

Temporal activities for the MonitorWorkflow.

This module handles:
- Fixture activation (staging → active)
- Polling active fixtures from API
- Event debouncing (3-poll confirmation)
- Twitter workflow triggering
- Fixture completion

See src/data/models.py for data model documentation.
"""
from temporalio import activity
from typing import Dict, List, Any
from datetime import datetime, timezone
import os

from src.data.models import EventFields, create_new_enhanced_event
from src.utils.footy_logging import log

MODULE = "monitor"


def is_player_known(event: dict) -> bool:
    """
    Check if the player name is known (not Unknown or missing).
    
    When a goal is first detected, the API may not yet have the scorer identified.
    In this case, player.name may be None, empty, or "Unknown".
    
    We only debounce and trigger workflows when we have an actual player name,
    since Twitter searches require a player name to be useful.
    
    When the player becomes identified, the player_id changes, creating a new
    event_id. The old "Unknown" event will be removed via VAR logic.
    """
    player_name = event.get("player", {}).get("name")
    if not player_name:
        return False
    # Check for common "unknown" variations
    if player_name.lower() in ("unknown", "tbd", "n/a", ""):
        return False
    return True


# =============================================================================
# ACTIVE: Staging fixture fetching (still used)
# =============================================================================

@activity.defn
async def fetch_staging_fixtures() -> List[Dict[str, Any]]:
    """
    Batch fetch all staging fixtures from API-Football.
    Returns raw API data for fixtures still in staging.
    
    This allows us to update staging fixtures with real-time data
    (status changes, time updates, cancellations, etc.)
    """
    from src.api.api_client import fixtures_batch
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    # Get all staging fixture IDs
    fixture_ids = store.get_staging_fixture_ids()
    
    if not fixture_ids:
        log.info(activity.logger, MODULE, "staging_empty", "No staging fixtures to fetch")
        return []
    
    log.info(activity.logger, MODULE, "staging_fetch_started", "Fetching staging fixtures",
             count=len(fixture_ids))
    
    try:
        fresh_data = fixtures_batch(fixture_ids)
        log.info(activity.logger, MODULE, "staging_fetch_success", "Retrieved staging data",
                 count=len(fresh_data))
        return fresh_data
    
    except Exception as e:
        log.error(activity.logger, MODULE, "staging_fetch_failed", "Staging fetch failed",
                  error=str(e))
        raise


@activity.defn
async def pre_activate_upcoming_fixtures(lookahead_minutes: int = 30) -> Dict[str, Any]:
    """
    Check staging fixtures and poll those not in the current 15-minute interval.
    Pre-activate fixtures with kickoff time within the lookahead window.
    
    Called every monitor cycle (~30s), but only polls API for fixtures whose
    _last_monitor timestamp is in a different 15-minute interval than now.
    
    Interval calculation: (hour * 4) + (minute // 15) → 0-95 per day
    - 08:00-08:14 = interval 32
    - 08:15-08:29 = interval 33
    - etc.
    
    Fixtures are moved to active with NO _last_activity - they'll appear
    at the bottom of the frontend sorted by kickoff time until they actually start.
    
    When the match actually kicks off (status NS→1H), sync_fixture_data() will
    set _last_activity and the fixture will jump to the top.
    
    Args:
        lookahead_minutes: Pre-activate if kickoff <= now + this many minutes
    
    Returns:
        - polled: Number of fixtures fetched from API
        - updated: Number of fixtures updated in staging  
        - activated: Number of fixtures pre-activated
        - skipped: True if all fixtures already in current interval
    """
    from src.data.mongo_store import FootyMongoStore
    from src.api.api_client import fixtures_batch
    from datetime import datetime, timezone, timedelta
    
    store = FootyMongoStore()
    now = datetime.now(timezone.utc)
    current_interval = (now.hour * 4) + (now.minute // 15)
    
    # Get all staging fixtures from MongoDB
    all_staging = store.get_staging_fixtures()
    
    if not all_staging:
        log.debug(activity.logger, MODULE, "staging_empty", "No staging fixtures")
        return {"polled": 0, "updated": 0, "activated": 0, "skipped": True}
    
    # Filter to fixtures NOT in current interval
    fixtures_to_poll = []
    for fixture in all_staging:
        last_monitor = fixture.get("_last_monitor")
        if last_monitor is None:
            # Never monitored - shouldn't happen if set at ingestion, but handle it
            fixtures_to_poll.append(fixture)
        else:
            fixture_interval = (last_monitor.hour * 4) + (last_monitor.minute // 15)
            if fixture_interval != current_interval:
                fixtures_to_poll.append(fixture)
    
    if not fixtures_to_poll:
        # All fixtures already polled this interval
        log.info(activity.logger, MODULE, "staging_skip", "All fixtures in current interval",
                 interval=current_interval, time=now.strftime('%H:%M'),
                 fixture_count=len(all_staging))
        return {"polled": 0, "updated": 0, "activated": 0, "skipped": True}
    
    # Fetch only the stale fixtures from API
    fixture_ids = [f["_id"] for f in fixtures_to_poll]
    
    log.info(activity.logger, MODULE, "staging_poll", "Polling staging fixtures",
             interval=current_interval, time=now.strftime('%H:%M'),
             polling=len(fixture_ids), total=len(all_staging))
    
    live_data = fixtures_batch(fixture_ids)
    
    if not live_data:
        log.warning(activity.logger, MODULE, "staging_no_data", "No data from API for staging fixtures")
        return {"polled": 0, "updated": 0, "activated": 0, "emergency_activated": 0, "skipped": False}
    
    # Get active statuses for failsafe check
    from src.utils.fixture_status import get_active_statuses
    active_statuses = set(get_active_statuses())
    
    # Process each fixture
    lookahead_cutoff = now + timedelta(minutes=lookahead_minutes)
    updated_count = 0
    activated_count = 0
    emergency_activated_count = 0
    
    for fixture_data in live_data:
        fixture_id = fixture_data.get("fixture", {}).get("id")
        if not fixture_id:
            continue
        
        home_team = fixture_data.get("teams", {}).get("home", {}).get("name", "Unknown")
        away_team = fixture_data.get("teams", {}).get("away", {}).get("name", "Unknown")
        status = fixture_data.get("fixture", {}).get("status", {}).get("short", "")
        
        # FAILSAFE: If status is active (1H, 2H, HT, etc.), immediately move to active!
        # This catches games that started early or API anomalies
        if status in active_statuses:
            if store.activate_fixture_with_data(fixture_id, fixture_data):
                store.fixtures_staging.delete_one({"_id": fixture_id})
                log.warning(activity.logger, MODULE, "emergency_activation", "Game started while still in staging",
                            fixture_id=fixture_id, match=f"{home_team} vs {away_team}", status=status)
                emergency_activated_count += 1
            continue
        
        # Parse fixture date
        fixture_date_str = fixture_data.get("fixture", {}).get("date")
        if not fixture_date_str:
            continue
            
        try:
            fixture_date = datetime.fromisoformat(fixture_date_str.replace('Z', '+00:00'))
        except (ValueError, TypeError):
            log.warning(activity.logger, MODULE, "invalid_date", "Invalid fixture date",
                        fixture_id=fixture_id, date=fixture_date_str)
            continue
        
        # Check if fixture should be pre-activated (kickoff within lookahead window)
        if fixture_date <= lookahead_cutoff:
            # Pre-activate: move to active collection
            if store.activate_fixture_with_data(fixture_id, fixture_data):
                # Delete from staging
                store.fixtures_staging.delete_one({"_id": fixture_id})
                minutes_until = int((fixture_date - now).total_seconds() / 60)
                log.info(activity.logger, MODULE, "pre_activated", "Fixture pre-activated",
                         fixture_id=fixture_id, match=f"{home_team} vs {away_team}",
                         kickoff_in_minutes=minutes_until)
                activated_count += 1
        else:
            # Update staging with fresh data + new timestamp
            fixture_data["_last_monitor"] = now
            store.update_staging_fixture(fixture_id, fixture_data)
            updated_count += 1
    
    log.info(activity.logger, MODULE, "staging_complete", "Staging cycle complete",
             polled=len(live_data), updated=updated_count, activated=activated_count,
             emergency_activated=emergency_activated_count if emergency_activated_count > 0 else None)
    
    return {
        "polled": len(live_data),
        "updated": updated_count,
        "activated": activated_count,
        "emergency_activated": emergency_activated_count,
        "skipped": False,
    }


@activity.defn
async def fetch_active_fixtures() -> List[Dict[str, Any]]:
    """
    Batch fetch all active fixtures from API-Football.
    Returns raw API data with events.
    """
    from src.api.api_client import fixtures_batch
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    # Get all active fixture IDs
    fixture_ids = store.get_active_fixture_ids()
    
    if not fixture_ids:
        log.info(activity.logger, MODULE, "active_empty", "No active fixtures to fetch")
        return []
    
    log.info(activity.logger, MODULE, "active_fetch_started", "Fetching active fixtures",
             count=len(fixture_ids))
    
    try:
        fresh_data = fixtures_batch(fixture_ids)
        log.info(activity.logger, MODULE, "active_fetch_success", "Retrieved active data",
                 count=len(fresh_data))
        return fresh_data
    
    except Exception as e:
        log.error(activity.logger, MODULE, "active_fetch_failed", "Active fetch failed",
                  error=str(e))
        raise


@activity.defn
async def store_and_compare(fixture_id: int, fixture_data: Dict) -> Dict[str, Any]:
    """
    Store fixture in fixtures_live and compare with fixtures_active.
    
    Returns comparison result with:
    - needs_debounce: True if there are NEW/INCOMPLETE/REMOVED events
    - new_events: Count of new events
    - incomplete_events: Count of events with data changes
    - removed_events: Count of events removed from live
    """
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    try:
        # Store in fixtures_live (raw API data with all events)
        store.store_live_fixture(fixture_id, fixture_data)
        log.info(activity.logger, MODULE, "live_stored", "Stored live data",
                 fixture_id=fixture_id)
        
        # Compare live vs active
        comparison = store.compare_live_vs_active(fixture_id)
        
        if comparison["needs_debounce"]:
            log.info(activity.logger, MODULE, "debounce_needed", "Debounce required",
                     fixture_id=fixture_id, new_events=comparison['new_events'],
                     incomplete_events=comparison['incomplete_events'],
                     removed_events=comparison['removed_events'])
        
        return comparison
    
    except Exception as e:
        log.error(activity.logger, MODULE, "store_compare_failed", "Store/compare failed",
                  fixture_id=fixture_id, error=str(e))
        raise


@activity.defn
async def complete_fixture_if_ready(fixture_id: int) -> bool:
    """
    Check if fixture is ready to complete and move it if so.
    
    Completion flow (in order):
    1. Check all events have _monitor_complete = True
    2. Check all events have _download_complete = True  
    3. ONLY THEN start/increment the completion counter
    4. When counter >= 3 (or winner data exists), move to completed
    
    This ensures the completion counter doesn't start ticking until
    all event processing (debounce + Twitter) is actually done.
    """
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    try:
        # =====================================================================
        # STEP 1: Check if ALL events are fully processed
        # =====================================================================
        fixture = store.get_fixture_from_active(fixture_id)
        if not fixture:
            log.warning(activity.logger, MODULE, "fixture_not_found", "Fixture not found in active",
                        fixture_id=fixture_id)
            return False
        
        events = fixture.get("events", [])
        enhanced_events = [e for e in events if e.get(EventFields.EVENT_ID)]
        valid_events = [
            e for e in enhanced_events 
            if not e.get(EventFields.REMOVED, False) 
            and "None" not in e.get(EventFields.EVENT_ID, "")
        ]
        
        if valid_events:
            monitored = sum(1 for e in valid_events if e.get(EventFields.MONITOR_COMPLETE))
            download_done = sum(1 for e in valid_events if e.get(EventFields.DOWNLOAD_COMPLETE))
            
            if monitored < len(valid_events) or download_done < len(valid_events):
                log.info(activity.logger, MODULE, "events_not_ready", "Events not ready for completion",
                         fixture_id=fixture_id, monitored=monitored, download=download_done,
                         total=len(valid_events))
                return False  # Don't start completion counter yet!
        
        # =====================================================================
        # STEP 2: All events ready - NOW we can increment completion counter
        # =====================================================================
        completion_status = store.increment_completion_count(fixture_id)
        count = completion_status["completion_count"]
        winner_exists = completion_status["winner_exists"]
        completion_complete = completion_status["completion_complete"]
        
        # Log progress
        if count == 1:
            log.info(activity.logger, MODULE, "completion_started", "Completion counter started",
                     fixture_id=fixture_id, winner="yes" if winner_exists else "pending")
        else:
            log.info(activity.logger, MODULE, "completion_check", "Completion check",
                     fixture_id=fixture_id, count=count, max_count=3,
                     winner="yes" if winner_exists else "pending")
        
        # =====================================================================
        # STEP 3: Check if completion counter is satisfied
        # =====================================================================
        if not completion_complete:
            log.debug(activity.logger, MODULE, "completion_waiting", "Waiting for completion",
                      fixture_id=fixture_id, count=count, max_count=3, winner=winner_exists)
            return False
        
        # =====================================================================
        # STEP 4: All ready - complete the fixture
        # =====================================================================
        if store.complete_fixture(fixture_id):
            log.info(activity.logger, MODULE, "fixture_completed", "Fixture completed",
                     fixture_id=fixture_id)
            # Note: Temp directory cleanup is done in MonitorWorkflow after this returns True
            return True
        
        return False
    
    except Exception as e:
        log.error(activity.logger, MODULE, "completion_error", "Completion error",
                  fixture_id=fixture_id, error=str(e))
        return False


@activity.defn
async def process_fixture_events(fixture_id: int, workflow_id: str = None) -> Dict[str, Any]:
    """
    Process fixture events using pure set comparison with workflow-ID-based tracking.
    
    Algorithm:
    1. Get event_ids from live and active (sets)
    2. NEW = live_ids - active_ids → add event (player must be known)
    3. REMOVED = active_ids - live_ids → decrement monitor count, delete if 0 (VAR)
    4. MATCHING = live_ids & active_ids → register workflow ID, check trigger condition
    5. Trigger Twitter when: len(_monitor_workflows) >= 3 AND _monitor_complete = false
    
    Args:
        fixture_id: The fixture ID to process
        workflow_id: The current MonitorWorkflow ID (for tracking which workflows processed events)
    
    See src/data/models.py for event field documentation.
    """
    from src.data.mongo_store import FootyMongoStore
    from src.utils.event_enhancement import build_twitter_search, calculate_score_context
    
    store = FootyMongoStore()
    
    live_fixture = store.get_live_fixture(fixture_id)
    active_fixture = store.get_fixture_from_active(fixture_id)
    
    if not live_fixture:
        log.warning(activity.logger, MODULE, "not_found_live", "Fixture not in live",
                    fixture_id=fixture_id)
        return {"status": "not_found_live"}
    
    if not active_fixture:
        log.warning(activity.logger, MODULE, "not_found_active", "Fixture not in active",
                    fixture_id=fixture_id)
        return {"status": "not_found_active"}
    
    live_events = live_fixture.get("events", [])
    active_events = active_fixture.get("events", [])
    
    # Build sets for comparison
    live_ids = {e[EventFields.EVENT_ID] for e in live_events if e.get(EventFields.EVENT_ID)}
    active_map = {e[EventFields.EVENT_ID]: e for e in active_events if e.get(EventFields.EVENT_ID)}
    active_ids = set(active_map.keys())
    
    # NEW events - add to active with initial state
    new_ids = live_ids - active_ids
    new_count = 0
    for event_id in new_ids:
        live_event = next(e for e in live_events if e.get(EventFields.EVENT_ID) == event_id)
        
        # Build enhancement fields
        twitter_search = build_twitter_search(live_event, live_fixture)
        score_context = calculate_score_context(live_fixture, live_event)
        
        # Determine initial monitor count based on player status
        # Unknown players (player_id=0) start at 0 to signal frontend they're not yet identified
        # Known players start at 1 and will debounce up to 3
        player_known = is_player_known(live_event)
        initial_count = 1 if player_known else 0
        
        # Create enhanced event using models helper
        # Only include workflow_id in _monitor_workflows if player is known
        # Unknown players stay at 0 workflows (like old counter staying at 0)
        enhanced = create_new_enhanced_event(
            live_event=live_event,
            event_id=event_id,
            twitter_search=twitter_search,
            score_after=score_context.get(EventFields.SCORE_AFTER, ""),
            scoring_team=score_context.get(EventFields.SCORING_TEAM, ""),
            initial_monitor_count=initial_count,
            initial_monitor_workflows=[workflow_id] if workflow_id and player_known else [],
        )
        
        first_seen = enhanced[EventFields.FIRST_SEEN]
        if store.add_event_to_active(fixture_id, enhanced, first_seen):
            player_status = "known" if initial_count == 1 else "UNKNOWN"
            log.info(activity.logger, MODULE, "new_event", "New event added",
                     fixture_id=fixture_id, event_id=event_id, player_status=player_status)
            new_count += 1
    
    # =========================================================================
    # REMOVED events (disappeared from API) - Track with _drop_workflows
    # =========================================================================
    # Instead of immediately deleting, we track which monitor workflows saw
    # the event missing using _drop_workflows array.
    # 
    # NEW APPROACH (Workflow-ID-based):
    # - Event MISSING: Add workflow_id to _drop_workflows via $addToSet
    # - If len(_drop_workflows) >= 3: DELETE the event
    # - Event REAPPEARS: FULL RESET - clear _drop_workflows entirely
    #
    # This means 3 unique monitor runs must see the event missing to delete.
    # API glitches that return empty events for 1-2 polls won't cause data loss.
    # If event flickers back, we start the drop count from scratch.
    #
    # EXCEPTION: Unknown scorer events (player_id=0) are deleted immediately
    # when they disappear - no need to wait for 3 monitors. These are clearly
    # placeholder events that got removed/replaced by the actual scorer.
    #
    # OLD APPROACH (counter-based - kept for backwards compatibility):
    # We still decrement _monitor_count but don't use it for deletion decisions.
    removed_ids = active_ids - live_ids
    removed_count = 0
    for event_id in removed_ids:
        active_event = active_map[event_id]
        current_count = active_event.get(EventFields.MONITOR_COUNT, 0)
        monitor_complete = active_event.get(EventFields.MONITOR_COMPLETE, False)
        
        # Check if this is an unknown scorer event (player_id=0 or no player name)
        player_id = active_event.get("player", {}).get("id", 0)
        player_name = active_event.get("player", {}).get("name", "")
        is_unknown_scorer = player_id == 0 or not player_name or player_name.lower() in ("unknown", "tbd", "n/a")
        
        if is_unknown_scorer:
            # Unknown scorer events - delete immediately, no need to wait
            result = store.fixtures_active.update_one(
                {"_id": fixture_id},
                {"$pull": {"events": {EventFields.EVENT_ID: event_id}}}
            )
            if result.modified_count > 0:
                log.info(activity.logger, MODULE, "unknown_removed", "Unknown scorer event removed",
                         event_id=event_id, reason="unknown_scorer_disappeared", player_id=player_id)
                removed_count += 1
            continue
        
        # OLD: Decrement the counter (kept for backwards compatibility, not used for decisions)
        new_count_val = max(0, current_count - 1)
        store.update_event_stable_count(fixture_id, event_id, new_count_val, None)
        
        # NEW: Add this workflow to drop_workflows and check if threshold reached
        drop_count, should_delete = store.add_drop_workflow_and_check(
            fixture_id, event_id, workflow_id
        )
        
        if should_delete:
            # 3+ unique monitors saw event missing - actually delete the event
            # Only delete S3 if monitor was complete (videos were uploaded)
            if monitor_complete:
                if store.mark_event_removed(fixture_id, event_id):
                    log.warning(activity.logger, MODULE, "var_removed", "VAR removed event",
                                event_id=event_id, action="deleted_db_s3", drop_workflows=drop_count)
                    removed_count += 1
            else:
                # Monitor wasn't complete, just remove from MongoDB (no S3 data)
                result = store.fixtures_active.update_one(
                    {"_id": fixture_id},
                    {"$pull": {"events": {EventFields.EVENT_ID: event_id}}}
                )
                if result.modified_count > 0:
                    log.warning(activity.logger, MODULE, "removed_before_complete", "Event removed before completion",
                                event_id=event_id, reason="dropped_before_complete", drop_workflows=drop_count)
                    removed_count += 1
        else:
            # Not enough monitors have seen it missing yet
            log.warning(activity.logger, MODULE, "event_missing", "Event missing from API",
                        event_id=event_id, drop_workflows=drop_count, need_to_delete=3)
    
    # =========================================================================
    # MATCHING events - Register workflow and check trigger condition
    # =========================================================================
    matching_ids = live_ids & active_ids
    updated_count = 0
    twitter_triggered = []
    
    for event_id in matching_ids:
        active_event = active_map[event_id]
        current_count = active_event.get(EventFields.MONITOR_COUNT, 0)
        monitor_complete = active_event.get(EventFields.MONITOR_COMPLETE, False)
        download_complete = active_event.get(EventFields.DOWNLOAD_COMPLETE, False)
        
        # Get live event for API data sync
        live_event = next((e for e in live_events if e.get(EventFields.EVENT_ID) == event_id), None)
        
        # =====================================================================
        # FULL RESET: Event is PRESENT - clear any accumulated drop workflows
        # =====================================================================
        # If event was previously missing but reappeared, we clear _drop_workflows
        # entirely. This means if it disappears again, the drop count starts from 0.
        # This handles API flickering gracefully - one reappearance = full reset.
        store.clear_drop_workflows(fixture_id, event_id)
        
        # =====================================================================
        # CASE 1: Both monitor and download complete - nothing to do
        # =====================================================================
        if monitor_complete and download_complete:
            # Just recover count if needed (from previous decrements)
            if current_count < 3:
                new_count_val = current_count + 1
                store.update_event_stable_count(fixture_id, event_id, new_count_val, live_event)
                log.info(activity.logger, MODULE, "recovery", "Event count recovered",
                         event_id=event_id, old_count=current_count, new_count=new_count_val,
                         status="fully_complete")
            continue
        
        # =====================================================================
        # CASE 2: Monitor complete but download NOT complete
        # =====================================================================
        # Download workflow already started but hasn't finished yet.
        # The while loop in DownloadWorkflow will handle retries if needed.
        # We don't re-trigger here - just let download continue its work.
        if monitor_complete and not download_complete:
            # Get download workflow count to show progress
            download_count = store.get_download_workflow_count(fixture_id, event_id)
            log.info(activity.logger, MODULE, "twitter_in_progress", "Twitter/download in progress",
                     event_id=event_id, download_workflows=download_count, max_workflows=10)
            continue
        
        # =====================================================================
        # CASE 3: Monitor NOT complete - register workflow and check trigger
        # =====================================================================
        
        # Skip if player is not known (Unknown/missing)
        # Event stays visible in frontend, but won't progress to Twitter
        if live_event and not is_player_known(live_event):
            store.update_event_stable_count(fixture_id, event_id, current_count, live_event)
            log.info(activity.logger, MODULE, "waiting_for_player", "Waiting for player identification",
                     event_id=event_id, count=current_count)
            continue
        
        # Increment monitor count (for VAR decrement logic and frontend display)
        new_count_val = min(current_count + 1, 3)
        if store.update_event_stable_count(fixture_id, event_id, new_count_val, live_event):
            updated_count += 1
        
        # Register this workflow ID in the _monitor_workflows array
        # This is the NEW workflow-ID-based tracking
        # Only register if player is known - unknown players stay at 0 workflows
        # (like old counter staying at 0) until they get a name
        if workflow_id and is_player_known(live_event):
            store.add_monitor_workflow(fixture_id, event_id, workflow_id)
        
        # Get current workflow count and monitor_complete status
        monitor_workflow_count = store.get_monitor_workflow_count(fixture_id, event_id)
        monitor_complete_check = store.get_monitor_complete(fixture_id, event_id)
        
        # NEW TRIGGER CONDITION: len(_monitor_workflows) >= 3 AND _monitor_complete = false
        should_trigger = monitor_workflow_count >= 3 and not monitor_complete_check
        
        if should_trigger:
            player_name = live_event.get("player", {}).get("name") if live_event else None
            team_name = live_event.get("team", {}).get("name", "Unknown") if live_event else "Unknown"
            team_id = live_event.get("team", {}).get("id") if live_event else None
            
            if player_name:
                twitter_triggered.append({
                    "event_id": event_id,
                    "player_name": player_name,
                    "team_id": team_id,
                    "team_name": team_name,
                    "minute": live_event.get("time", {}).get("elapsed") if live_event else None,
                    "extra": live_event.get("time", {}).get("extra") if live_event else None,
                    "first_seen": active_event.get(EventFields.FIRST_SEEN),
                })
                log.info(activity.logger, MODULE, "ready_for_twitter", "Event ready for Twitter",
                         event_id=event_id, monitor_workflows=monitor_workflow_count)
            else:
                # No player name - mark complete without Twitter
                # (This shouldn't happen since we check is_player_known above, but just in case)
                first_seen = active_event.get(EventFields.FIRST_SEEN)
                store.mark_event_monitor_complete(fixture_id, event_id)
                log.warning(activity.logger, MODULE, "monitor_complete_no_player", "Monitor complete without player",
                            event_id=event_id, skip_reason="no_player_name")
        else:
            log.info(activity.logger, MODULE, "monitoring", "Monitoring event",
                     event_id=event_id, count=new_count_val, max_count=3,
                     workflows=monitor_workflow_count, complete=monitor_complete_check)
    
    # Sync fixture metadata
    store.sync_fixture_data(fixture_id)
    
    return {
        "status": "success",
        "new_events": new_count,
        "removed_events": removed_count,
        "updated_events": updated_count,
        "twitter_triggered": twitter_triggered,
    }


@activity.defn
async def sync_fixture_metadata(fixture_id: int) -> bool:
    """
    Sync fixture top-level data from fixtures_live to fixtures_active.
    Called when no debounce is needed but we want to keep fixture metadata fresh.
    
    Updates: score, status, time, teams data, etc.
    Preserves: Enhanced events array
    """
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    try:
        if store.sync_fixture_data(fixture_id):
            log.debug(activity.logger, MODULE, "metadata_synced", "Synced metadata",
                      fixture_id=fixture_id)
            return True
        return False
    except Exception as e:
        log.error(activity.logger, MODULE, "sync_error", "Sync error",
                  fixture_id=fixture_id, error=str(e))
        return False


@activity.defn
async def check_twitter_workflow_running(workflow_id: str) -> Dict[str, Any]:
    """
    Check if a Twitter workflow with the given ID is currently running or completed.
    
    This is used before attempting to restart a Twitter workflow to avoid:
    1. Starting duplicates when one is already running
    2. Restarting completed workflows unnecessarily
    
    Args:
        workflow_id: The Twitter workflow ID (e.g., "twitter-Liverpool-Gakpo-90min-...")
    
    
    Returns:
        Dict with:
        - exists: bool - whether workflow exists
        - running: bool - whether workflow is currently running
        - status: str - workflow status (RUNNING, COMPLETED, FAILED, etc.)
    """
    from temporalio.client import Client, WorkflowExecutionStatus
    from temporalio.service import RPCError
    
    temporal_host = os.getenv("TEMPORAL_HOST", "localhost:7233")
    
    try:
        client = await Client.connect(temporal_host)
        handle = client.get_workflow_handle(workflow_id)
        desc = await handle.describe()
        
        status_name = desc.status.name if desc.status else "UNKNOWN"
        is_running = desc.status == WorkflowExecutionStatus.RUNNING
        
        log.info(activity.logger, MODULE, "workflow_check", "Workflow status checked",
                 workflow_id=workflow_id, status=status_name, running=is_running)
        
        return {
            "exists": True,
            "running": is_running,
            "status": status_name,
        }
    except RPCError as e:
        if "not found" in str(e).lower():
            log.info(activity.logger, MODULE, "workflow_not_found", "Workflow not found",
                     workflow_id=workflow_id)
            return {
                "exists": False,
                "running": False,
                "status": "NOT_FOUND",
            }
        log.warning(activity.logger, MODULE, "workflow_check_error", "Workflow check RPC error",
                    workflow_id=workflow_id, error=str(e))
        return {
            "exists": False,
            "running": False,
            "status": "ERROR",
        }
    except Exception as e:
        log.warning(activity.logger, MODULE, "workflow_check_error", "Workflow check error",
                    workflow_id=workflow_id, error=str(e))
        return {
            "exists": False,
            "running": False,
            "status": "ERROR",
        }


@activity.defn
async def notify_frontend_refresh() -> bool:
    """
    Notify the frontend API to broadcast a refresh to all connected SSE clients.
    Called at the end of monitor cycles and after download completions.
    """
    import os
    import requests
    
    api_url = os.getenv("FRONTEND_API_URL", "http://vedanta-systems-dev-found-footy-api:3001")
    
    try:
        response = requests.post(f"{api_url}/api/found-footy/refresh", timeout=5)
        if response.ok:
            result = response.json()
            log.info(activity.logger, MODULE, "frontend_notified", "Frontend notified",
                     clients=result.get('clientsNotified', 0))
            return True
        else:
            log.warning(activity.logger, MODULE, "frontend_notify_failed", "Frontend notify failed",
                        status_code=response.status_code)
            return False
    except requests.exceptions.ConnectionError:
        # Frontend not running is not a fatal error
        log.debug(activity.logger, MODULE, "frontend_unavailable", "Frontend API not available",
                  reason="connection_refused")
        return False
    except Exception as e:
        log.warning(activity.logger, MODULE, "frontend_notify_error", "Frontend notify error",
                    error=str(e))
        return False


# =============================================================================
# WORKFLOW TRACKING (Workflow-ID-based)
# =============================================================================

@activity.defn
async def register_monitor_workflow(
    fixture_id: int,
    event_id: str,
    workflow_id: str
) -> Dict[str, Any]:
    """
    Register a MonitorWorkflow as having processed this event.
    Uses $addToSet for idempotency - adding the same ID twice is a no-op.
    
    Returns count AND current _monitor_complete status for spawn decision.
    The caller uses: should_trigger = (count >= 3 AND not monitor_complete)
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID
        workflow_id: The MonitorWorkflow ID (e.g., "monitor-27_01_2026-15:30")
    
    Returns:
        Dict with success, count, monitor_complete, should_trigger_twitter
    """
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    try:
        # Add workflow ID to array (idempotent via $addToSet)
        success = store.add_monitor_workflow(fixture_id, event_id, workflow_id)
        
        # Get current count and completion status
        count = store.get_monitor_workflow_count(fixture_id, event_id)
        monitor_complete = store.get_monitor_complete(fixture_id, event_id)
        
        # Determine if we should trigger Twitter
        # Only trigger if count >= 3 AND monitor_complete is still False
        # (If monitor_complete is True, Twitter already started)
        should_trigger = count >= 3 and not monitor_complete
        
        log.info(activity.logger, MODULE, "register_monitor_workflow", "Monitor workflow registered",
                 event_id=event_id, workflow_id=workflow_id, count=count,
                 complete=monitor_complete, should_trigger=should_trigger)
        
        return {
            "success": success,
            "count": count,
            "monitor_complete": monitor_complete,
            "should_trigger_twitter": should_trigger
        }
    except Exception as e:
        log.error(activity.logger, MODULE, "register_workflow_failed", "Register monitor workflow failed",
                  event_id=event_id, error=str(e))
        return {
            "success": False,
            "count": 0,
            "monitor_complete": False,
            "should_trigger_twitter": False
        }