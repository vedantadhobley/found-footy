#!/usr/bin/env python3
"""
Test monitor job logic directly without Dagster CLI.
"""
from src.jobs.monitor.monitor_job import monitor_job

print("=" * 60)
print("EXECUTING MONITOR JOB")
print("=" * 60)
print()

# Execute the job directly
result = monitor_job.execute_in_process()

print()
print("=" * 60)
print("MONITOR JOB COMPLETE")
print("=" * 60)
print()
print(f"Success: {result.success}")

# Check the fixture was processed
if result.success:
    from src.data.mongo_store import MongoStore
    store = MongoStore()
    fixture = store.fixtures_active.find_one({"fixture.id": 1378993})
    
    if fixture:
        events = fixture.get("events", [])
        enhanced = [e for e in events if e.get("_event_id")]
        print(f"\nFixture 1378993:")
        print(f"  Total events: {len(events)}")
        print(f"  Enhanced events: {len(enhanced)}")
        
        if enhanced:
            for event in enhanced:
                print(f"\n  Event: {event.get('_event_id')}")
                print(f"    Player: {event.get('player', {}).get('name')}")
                print(f"    Team: {event.get('team', {}).get('name')}")
                print(f"    Search hash: {event.get('_search_hash')}")
                print(f"    Debounce: {event.get('_debounce_count')}/{3}")
                print(f"    Debounce complete: {event.get('_debounce_complete')}")
                print(f"    Twitter complete: {event.get('_twitter_complete')}")
                print(f"    Score: {event.get('_score')}")
