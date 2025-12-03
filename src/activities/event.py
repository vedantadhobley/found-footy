"""Event (debounce) activities"""
from temporalio import activity
from typing import Dict, List, Any


@activity.defn
async def debounce_fixture_events(fixture_id: int) -> Dict[str, Any]:
    """
    Debounce all events for a fixture.
    
    Process:
    1. Get live and active events
    2. Build dict of live events by _event_id
    3. Iterate active events:
       - If in live: compare hash, increment/reset stable_count
       - If not in live: mark REMOVED (VAR)
    4. Whatever's left in live dict are NEW events
    5. Return list of events ready for Twitter (stable_count >= 3)
    """
    # TODO: Implement debounce logic from src-dagster/jobs/debounce/debounce_job.py
    return {
        "new_count": 0,
        "updated_count": 0,
        "completed_count": 0,
        "removed_count": 0,
        "twitter_ready": [],  # List of event_ids ready for Twitter
    }
