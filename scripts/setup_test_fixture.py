#!/usr/bin/env python3
"""
Setup test fixture for debounce cycle testing.

Fetches fixture 1378993 (Liverpool vs Arsenal, 1-0) and inserts it into
fixtures_active with NO events, so the monitor can detect events and trigger
the full debounce cycle from scratch.

Usage:
    python scripts/setup_test_fixture.py
    OR
    docker exec found-footy-dagster-daemon python scripts/setup_test_fixture.py
"""
import sys
import requests

from src.api.mongo_api import get_api_headers, BASE_URL
from src.data.mongo_store import FootyMongoStore

# Test fixture configuration
TEST_FIXTURE_ID = 1378993  # Liverpool vs Arsenal, 1-0 (single goal)


def setup_test_fixture(fixture_id: int = TEST_FIXTURE_ID):
    """
    Fetch fixture data and insert into fixtures_active for testing.
    
    Args:
        fixture_id: Fixture ID to use for testing (default: 1378993)
    """
    print(f"🔧 Setting up test fixture {fixture_id}...")
    print()
    
    # Fetch fixture data from API
    print("📡 Fetching fixture data from API...")
    url = f'{BASE_URL}/fixtures'
    headers = get_api_headers()
    response = requests.get(url, headers=headers, params={'id': fixture_id})
    
    if response.status_code != 200:
        print(f'❌ API request failed: {response.status_code}')
        print(response.text)
        sys.exit(1)
    
    data = response.json()
    fixtures = data.get('response', [])
    
    if not fixtures:
        print(f'❌ No fixture found with ID {fixture_id}')
        sys.exit(1)
    
    fixture_data = fixtures[0]
    
    # Display fixture info
    home_team = fixture_data["teams"]["home"]["name"]
    away_team = fixture_data["teams"]["away"]["name"]
    home_score = fixture_data["goals"]["home"]
    away_score = fixture_data["goals"]["away"]
    status = fixture_data["fixture"]["status"]["long"]
    event_count = len(fixture_data.get("events", []))
    
    print(f"✅ Fetched fixture {fixture_id}")
    print(f"   {home_team} vs {away_team}")
    print(f"   Score: {home_score}-{away_score}")
    print(f"   Status: {status}")
    print(f"   Events: {event_count}")
    print()
    
    # Remove events and lineups (we want monitor to detect them fresh)
    if 'events' in fixture_data:
        print(f"🗑️  Removing {event_count} events from fixture data")
        del fixture_data['events']
    
    if 'lineups' in fixture_data:
        del fixture_data['lineups']
    
    # Connect to MongoDB
    print("🔌 Connecting to MongoDB...")
    store = FootyMongoStore()
    
    # Clean up existing data for this fixture
    print("🧹 Cleaning up existing data...")
    
    # Remove from fixtures_active if exists
    result = store.db.fixtures_active.delete_one({'fixture.id': fixture_id})
    if result.deleted_count > 0:
        print(f"   ✓ Removed from fixtures_active")
    
    # Remove from fixtures_completed if exists
    result = store.db.fixtures_completed.delete_one({'fixture.id': fixture_id})
    if result.deleted_count > 0:
        print(f"   ✓ Removed from fixtures_completed")
    
    # Insert the fixture into fixtures_active (with empty events array)
    print()
    print("💾 Inserting fixture into fixtures_active...")
    fixture_data["events"] = []  # Empty events - monitor will fetch and enhance
    store.db.fixtures_active.insert_one(fixture_data)
    
    print()
    print("=" * 60)
    print("✅ TEST FIXTURE READY")
    print("=" * 60)
    print(f"Fixture {fixture_id} inserted into fixtures_active with empty events")
    print()
    print("Next steps:")
    print("1. Monitor job will fetch fresh data WITH events")
    print("2. Events will be enhanced with metadata in-place")
    print("3. Debounce counts will be updated each cycle")
    print("4. When stable, Twitter search will be triggered")
    print("5. When all complete, fixture moves to fixtures_completed")
    print()
    print("Watch the logs with:")
    print("  docker logs -f found-footy-dagster-daemon")
    print()


if __name__ == "__main__":
    # Allow custom fixture ID from command line
    fixture_id = TEST_FIXTURE_ID
    if len(sys.argv) > 1:
        try:
            fixture_id = int(sys.argv[1])
        except ValueError:
            print(f"❌ Invalid fixture ID: {sys.argv[1]}")
            sys.exit(1)
    
    setup_test_fixture(fixture_id)
