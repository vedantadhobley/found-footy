#!/usr/bin/env python3
"""
Test monitor cycle with fixture 1378993 (Liverpool vs Arsenal, 1-0)

Inserts fixture directly into fixtures_active with basic data (no events).
Monitor will fetch full data, enhance events, and start debounce cycle.
"""
import sys
from datetime import datetime, timezone

from src.data.mongo_store import MongoStore

# Test fixture data - Liverpool vs Arsenal, 1-0
TEST_FIXTURE_DATA = {
    "_id": 1378993,
    "fixture": {
        "id": 1378993,
        "referee": "Anthony Taylor",
        "timezone": "UTC",
        "date": "2024-02-04T16:30:00+00:00",
        "timestamp": 1707062400,
        "status": {
            "long": "Match Finished",
            "short": "FT",
            "elapsed": 90
        },
        "venue": {
            "id": 494,
            "name": "Anfield",
            "city": "Liverpool"
        }
    },
    "league": {
        "id": 39,
        "name": "Premier League",
        "country": "England",
        "logo": "https://media.api-sports.io/football/leagues/39.png",
        "flag": "https://media.api-sports.io/flags/gb.svg",
        "season": 2023,
        "round": "Regular Season - 23"
    },
    "teams": {
        "home": {
            "id": 40,
            "name": "Liverpool",
            "logo": "https://media.api-sports.io/football/teams/40.png"
        },
        "away": {
            "id": 42,
            "name": "Arsenal",
            "logo": "https://media.api-sports.io/football/teams/42.png"
        }
    },
    "goals": {
        "home": 1,
        "away": 0
    },
    "score": {
        "halftime": {"home": 0, "away": 0},
        "fulltime": {"home": 1, "away": 0},
        "extratime": {"home": None, "away": None},
        "penalty": {"home": None, "away": None}
    },
    "events": []  # Empty - monitor will fetch and enhance
}


def test_monitor_cycle():
    """
    Insert test fixture and show monitoring instructions.
    """
    print("=" * 60)
    print("MONITOR CYCLE TEST")
    print("=" * 60)
    print()
    
    store = MongoStore()
    fixture_id = TEST_FIXTURE_DATA["_id"]
    
    # Clean up existing data
    print("🧹 Cleaning up existing data...")
    
    for collection_name in ["fixtures_active", "fixtures_completed"]:
        result = store.db[collection_name].delete_one({"fixture.id": fixture_id})
        if result.deleted_count > 0:
            print(f"   ✓ Removed from {collection_name}")
    
    # Insert fixture with empty events
    print()
    print("💾 Inserting test fixture into fixtures_active...")
    print(f"   Fixture: {fixture_id}")
    print(f"   Teams: {TEST_FIXTURE_DATA['teams']['home']['name']} vs {TEST_FIXTURE_DATA['teams']['away']['name']}")
    print(f"   Score: {TEST_FIXTURE_DATA['goals']['home']}-{TEST_FIXTURE_DATA['goals']['away']}")
    print(f"   Status: {TEST_FIXTURE_DATA['fixture']['status']['short']}")
    print(f"   Events: {len(TEST_FIXTURE_DATA['events'])} (empty - will be fetched)")
    
    store.fixtures_active.insert_one(TEST_FIXTURE_DATA)
    
    print()
    print("=" * 60)
    print("✅ TEST FIXTURE READY")
    print("=" * 60)
    print()
    print("Next: Run monitor_job to test the cycle")
    print()
    print("Expected behavior:")
    print("1. ✅ Monitor fetches fixture from API (with events)")
    print("2. ✅ Filters to trackable events (1 Goal)")
    print("3. ✅ Enhances event with metadata:")
    print("      - _event_id: 1378993_40_Goal_1")
    print("      - _search_hash: szoboszlai_liverpool")
    print("      - _debounce_count: 0 (first cycle)")
    print("      - _debounce_complete: false")
    print("      - _twitter_complete: false")
    print("      - _score: {home: 1, away: 0}")
    print("4. ✅ Updates fixture in fixtures_active")
    print("5. ❌ Does NOT move to completed (needs debounce + Twitter)")
    print()
    print("After 3 more monitor cycles (with stable hash):")
    print("   - _debounce_count: 3")
    print("   - _debounce_complete: true")
    print("   - Event ready for Twitter job")
    print()
    print("After Twitter job completes:")
    print("   - _twitter_complete: true")
    print("   - _discovered_videos: [...]")
    print("   - Fixture moves to fixtures_completed")
    print()
    print("To run monitor_job:")
    print("   dagster job execute -m src -j monitor_job")
    print()


if __name__ == "__main__":
    try:
        test_monitor_cycle()
    except Exception as e:
        print(f"❌ Error: {e}")
        import traceback
        traceback.print_exc()
        sys.exit(1)
