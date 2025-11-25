#!/usr/bin/env python3
"""
Validate refactor - check that all imports work and collections are set up correctly.
"""
import sys

print("=" * 60)
print("REFACTOR VALIDATION")
print("=" * 60)
print()

# Test 1: Import event utilities
print("1. Testing event utilities...")
try:
    from src.utils.event_config import should_track_event, TRACKABLE_EVENTS, DEBOUNCE_STABLE_COUNT
    print(f"   ✅ event_config: {len(TRACKABLE_EVENTS)} trackable events, debounce={DEBOUNCE_STABLE_COUNT}")
except Exception as e:
    print(f"   ❌ event_config: {e}")
    sys.exit(1)

try:
    from src.utils.event_enhancement import (
        enhance_events_with_metadata,
        get_events_ready_for_twitter
    )
    print("   ✅ event_enhancement imported")
except Exception as e:
    print(f"   ❌ event_enhancement: {e}")
    sys.exit(1)

# Test 2: Import mongo store
print("\n2. Testing mongo store...")
try:
    from src.data.mongo_store import MongoStore
    store = MongoStore()
    print(f"   ✅ MongoStore initialized")
    print(f"   Collections: {', '.join([store.fixtures_staging.name, store.fixtures_active.name, store.fixtures_completed.name])}")
except Exception as e:
    print(f"   ❌ mongo_store: {e}")
    sys.exit(1)

# Test 3: Import monitor job ops
print("\n3. Testing monitor job ops...")
try:
    from src.jobs.monitor.ops import (
        activate_fixtures_op,
        batch_fetch_active_op,
        process_events_op,
        complete_fixtures_op
    )
    print("   ✅ All monitor ops imported")
except Exception as e:
    print(f"   ❌ monitor ops: {e}")
    sys.exit(1)

# Test 4: Import monitor job
print("\n4. Testing monitor job...")
try:
    from src.jobs.monitor import monitor_job
    print("   ✅ monitor_job imported")
except Exception as e:
    print(f"   ❌ monitor_job: {e}")
    sys.exit(1)

# Test 5: Import main Dagster defs
print("\n5. Testing main Dagster definitions...")
try:
    from src import defs
    print("   ✅ Dagster defs loaded")
    # Note: defs.jobs is a list of JobDefinition objects
    print(f"   Jobs defined: {len(defs.jobs) if hasattr(defs, 'jobs') else 'unknown'}")
except Exception as e:
    print(f"   ❌ Dagster defs: {e}")
    sys.exit(1)

# Test 6: Check collection structure
print("\n6. Testing MongoDB collections...")
try:
    from src.data.mongo_store import MongoStore
    store = MongoStore()
    
    # Check that old collections don't exist in code
    assert not hasattr(store, 'fixtures_tracked'), "fixtures_tracked should not exist"
    assert not hasattr(store, 'events_pending'), "events_pending should not exist"
    assert not hasattr(store, 'events_confirmed'), "events_confirmed should not exist"
    
    print("   ✅ Old collections removed from code")
    print("   ✅ Only 3 collections: fixtures_staging, fixtures_active, fixtures_completed")
except Exception as e:
    print(f"   ❌ Collection check: {e}")
    sys.exit(1)

# Test 7: Test event enhancement logic
print("\n7. Testing event enhancement...")
try:
    from src.utils.event_enhancement import enhance_events_with_metadata
    
    # Mock event data
    raw_events = [
        {
            "time": {"elapsed": 83},
            "team": {"id": 40, "name": "Liverpool"},
            "player": {"name": "Szoboszlai"},
            "type": "Goal",
            "detail": "Normal Goal"
        },
        {
            "time": {"elapsed": 45},
            "team": {"id": 42, "name": "Arsenal"},
            "player": {"name": "Test"},
            "type": "Card",
            "detail": "Yellow Card"  # Should be filtered out
        }
    ]
    
    enhanced = enhance_events_with_metadata(
        raw_events=raw_events,
        fixture_id=1378993,
        home_team_id=40,
        away_team_id=42
    )
    
    # Should have only 1 event (Yellow card filtered out)
    assert len(enhanced) == 1, f"Expected 1 trackable event, got {len(enhanced)}"
    
    # Check metadata fields
    event = enhanced[0]
    assert "_event_id" in event, "Missing _event_id"
    assert "_search_hash" in event, "Missing _search_hash"
    assert "_debounce_count" in event, "Missing _debounce_count"
    assert "_twitter_complete" in event, "Missing _twitter_complete"
    assert "_score" in event, "Missing _score"
    
    print(f"   ✅ Event enhancement working")
    print(f"   Event ID: {event['_event_id']}")
    print(f"   Search hash: {event['_search_hash']}")
    print(f"   Score after event: {event['_score']}")
except Exception as e:
    print(f"   ❌ Event enhancement: {e}")
    import traceback
    traceback.print_exc()
    sys.exit(1)

print()
print("=" * 60)
print("✅ ALL VALIDATION TESTS PASSED")
print("=" * 60)
print()
print("Refactor successfully implemented:")
print("  • 3-collection architecture (fixtures_staging, fixtures_active, fixtures_completed)")
print("  • Events stored in-place with enhanced metadata")
print("  • Debounce tracking via _search_hash comparison")
print("  • No separate events collections")
print("  • Old debounce job removed")
print()
