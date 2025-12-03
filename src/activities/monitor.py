"""Monitor activities"""
from temporalio import activity
from typing import Dict, List, Any


@activity.defn
async def activate_fixtures() -> Dict[str, int]:
    """Move fixtures from staging to active if start time reached"""
    # TODO: Implement activation logic
    return {"activated_count": 0}


@activity.defn
async def fetch_active_fixtures() -> List[Dict[str, Any]]:
    """Batch fetch all active fixtures from API-Football"""
    # TODO: Implement API batch fetch
    return []


@activity.defn
async def store_and_compare(fixture_id: int, fixture_data: Dict) -> Dict[str, Any]:
    """
    Store fixture in fixtures_live and compare with fixtures_active.
    Returns comparison result indicating if debounce is needed.
    """
    # TODO: Implement store + compare logic
    return {
        "needs_debounce": False,
        "new_events": 0,
        "incomplete_events": 0,
        "removed_events": 0,
    }


@activity.defn
async def complete_fixture_if_ready(fixture_id: int) -> bool:
    """
    Check if fixture is ready to complete (FT/AET/PEN + all events processed).
    Move to fixtures_completed if ready.
    """
    # TODO: Implement completion check
    return False
