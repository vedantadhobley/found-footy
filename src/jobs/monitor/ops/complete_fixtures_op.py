"""Complete fixtures op - move fixtures to completed when all events are processed

A fixture is complete when:
1. Fixture status is FT (full time)
2. All events have _debounce_complete = True
3. All events have _twitter_complete = True
"""

from dagster import op, OpExecutionContext
from typing import List, Dict
from src.data.mongo_store import MongoStore


@op(
    name="complete_fixtures",
    description="Move finished fixtures from active to completed",
    tags={"kind": "mongodb", "stage": "complete"}
)
def complete_fixtures_op(
    context: OpExecutionContext,
    fixtures_updated: List[Dict]
) -> int:
    """
    Check if any fixtures are complete and move them.
    
    Args:
        fixtures_updated: Fixtures that were just processed
        
    Returns:
        Number of fixtures completed
    """
    store = MongoStore()
    completed_count = 0
    
    for fixture in fixtures_updated:
        fixture_id = fixture.get("fixture", {}).get("id")
        status = fixture.get("fixture", {}).get("status", {}).get("short")
        
        if not fixture_id:
            continue
        
        # Must be full time
        if status != "FT":
            context.log.debug(f"Fixture {fixture_id} status={status}, not complete")
            continue
        
        # Check if all events are complete
        events = fixture.get("events", [])
        enhanced_events = [e for e in events if e.get("_event_id")]
        
        if not enhanced_events:
            # No trackable events - fixture can complete immediately
            context.log.info(f"Fixture {fixture_id} has no trackable events, completing")
            store.move_fixture_to_completed(fixture_id)
            completed_count += 1
            continue
        
        # CRITICAL: For fixtures with events, we MUST wait for BOTH:
        # 1. Debounce completion (event data stable)
        # 2. Twitter completion (videos discovered)
        # Only then can we move the fixture to completed
        all_debounced = all(e.get("_debounce_complete") for e in enhanced_events)
        all_twitter_done = all(e.get("_twitter_complete") for e in enhanced_events)
        
        if all_debounced and all_twitter_done:
            context.log.info(
                f"Fixture {fixture_id} complete: {len(enhanced_events)} events all processed "
                f"(debounce + Twitter both complete)"
            )
            store.move_fixture_to_completed(fixture_id)
            completed_count += 1
        else:
            # Log progress - fixture stays in active until ALL processing done
            debounced = sum(1 for e in enhanced_events if e.get("_debounce_complete"))
            twitter_done = sum(1 for e in enhanced_events if e.get("_twitter_complete"))
            context.log.debug(
                f"Fixture {fixture_id} waiting: "
                f"{debounced}/{len(enhanced_events)} debounced, "
                f"{twitter_done}/{len(enhanced_events)} Twitter complete "
                f"(need both to move to completed)"
            )
    
    if completed_count > 0:
        context.log.info(f"Completed {completed_count} fixtures")
    
    return completed_count
