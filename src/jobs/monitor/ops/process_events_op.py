"""Process events op - store live data and compare

Correct flow:
1. Store fresh API data in fixtures_live (raw, no enhancements)
2. Compare live vs active to find events needing debounce
3. Directly invoke debounce_job for fixtures that need it
4. Move completed fixtures

fixtures_active is NEVER overwritten - only updated by debounce_job!
"""

from dagster import op, OpExecutionContext
from typing import List, Dict
from src.data.mongo_store import FootyMongoStore


@op(
    name="process_and_debounce_events",
    description="Store live data, compare, and invoke debounce per fixture",
    tags={"kind": "mongodb", "stage": "process"}
)
def process_and_debounce_events_op(context: OpExecutionContext, fresh_fixtures: List[Dict]) -> Dict[str, any]:
    """
    Store live data and invoke debounce for fixtures that need it.
    
    Flow:
    1. Store fresh API data in fixtures_live
    2. Compare live vs active to identify events needing debounce
    3. Directly invoke debounce_job for each fixture that needs it
    4. Move FT/AET/PEN fixtures to completed
    
    Args:
        fresh_fixtures: List of fixtures from API with raw events
        
    Returns:
        Dict with processing stats
    """
    store = FootyMongoStore()
    
    from src.jobs.debounce.debounce_job import debounce_fixture_events_op
    
    debounced_count = 0
    completed_count = 0
    
    for fresh_fixture in fresh_fixtures:
        try:
            fixture_id = fresh_fixture.get("fixture", {}).get("id")
            if not fixture_id:
                context.log.warning(f"Fixture missing ID")
                continue
            
            # Store in fixtures_live (raw API data)
            store.store_live_fixture(fixture_id, fresh_fixture)
            context.log.info(f"📥 Stored live data for fixture {fixture_id}")
            
            # Compare live vs active (3 cases: NEW, INCOMPLETE, REMOVED)
            comparison = store.compare_live_vs_active(fixture_id)
            
            if comparison["needs_debounce"]:
                context.log.info(
                    f"🎯 Fixture {fixture_id} needs debounce: "
                    f"NEW={comparison['new_events']}, "
                    f"INCOMPLETE={comparison['incomplete_events']}, "
                    f"REMOVED={comparison['removed_events']}"
                )
                
                # Directly invoke debounce job
                try:
                    result = debounce_fixture_events_op(context, fixture_id)
                    if result.get("status") == "success":
                        debounced_count += 1
                        context.log.info(f"✅ Debounced fixture {fixture_id}")
                    else:
                        context.log.warning(f"⚠️ Debounce returned non-success for {fixture_id}")
                except Exception as e:
                    context.log.error(f"❌ Error invoking debounce for {fixture_id}: {e}")
            
            # AFTER debounce, check if fixture is finished
            status = fresh_fixture.get("fixture", {}).get("status", {}).get("short")
            if status in ["FT", "AET", "PEN"]:
                if store.complete_fixture(fixture_id):
                    context.log.info(f"🏁 Moved fixture {fixture_id} to completed (status: {status})")
                    completed_count += 1
        
        except Exception as e:
            context.log.error(f"❌ Error processing fixture: {e}")
            continue
    
    context.log.info(
        f"📊 Summary: {len(fresh_fixtures)} fixtures processed, "
        f"{debounced_count} debounced, "
        f"{completed_count} completed"
    )
    
    return {
        "status": "success",
        "fixtures_processed": len(fresh_fixtures),
        "debounced_count": debounced_count,
        "completed_count": completed_count
    }
