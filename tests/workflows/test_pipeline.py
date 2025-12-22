#!/usr/bin/env python3
"""
Pipeline Test Script - Insert fixture into staging for end-to-end testing

Fetches a real fixture from API-Football, resets it to staging state (NS, no events,
null scores), and inserts into fixtures_staging. The scheduled MonitorWorkflow 
(runs every minute) will automatically:

  1. Activate fixture (staging → active)
  2. Fetch fresh data from API (with real events)
  3. Debounce events (3 cycles = 3 minutes)
  4. Trigger TwitterWorkflow for stable events
  5. Download videos and upload to S3

Usage:
    # Insert default fixture (Liverpool vs Arsenal)
    docker exec found-footy-worker python /workspace/tests/workflows/test_pipeline.py
    
    # Insert specific fixture
    docker exec found-footy-worker python /workspace/tests/workflows/test_pipeline.py --fixture-id 1234567
    
    # Skip cleanup (don't delete existing S3/MongoDB data)
    docker exec found-footy-worker python /workspace/tests/workflows/test_pipeline.py --no-clean
    
Then watch:
    - Temporal UI: http://localhost:4100
    - Twitter Firefox: http://localhost:4104
    - MongoDB Express: http://localhost:4101
"""
import argparse
import requests

from src.data.mongo_store import FootyMongoStore
from src.data.s3_store import FootyS3Store
from src.api.api_client import get_api_headers, BASE_URL

# Default fixture for testing
DEFAULT_FIXTURE_ID = 1378993  # Liverpool vs Arsenal, 1-0


def clean_fixture_data(fixture_id: int):
    """Delete all existing data for a fixture from MongoDB and S3"""
    print(f"🧹 Cleaning existing data for fixture {fixture_id}...")
    
    # Clean MongoDB
    store = FootyMongoStore()
    mongo_deleted = 0
    for coll_name in ["fixtures_staging", "fixtures_live", "fixtures_active", "fixtures_completed"]:
        result = store.db[coll_name].delete_one({"_id": fixture_id})
        if result.deleted_count > 0:
            print(f"   ✓ Removed from {coll_name}")
            mongo_deleted += 1
    
    if mongo_deleted == 0:
        print(f"   ℹ️  No MongoDB documents found")
    
    # Clean S3
    s3_store = FootyS3Store()
    videos = s3_store.list_videos_by_fixture(fixture_id)
    
    if videos:
        print(f"   🗑️  Deleting {len(videos)} videos from S3...")
        for video in videos:
            s3_key = video['key']
            s3_store.delete_video(s3_key)
        print(f"   ✓ Deleted {len(videos)} S3 videos")
    else:
        print(f"   ℹ️  No S3 videos found")
    
    print(f"✅ Cleanup complete")
    print()


def insert_fixture_to_staging(fixture_id: int, clean_first: bool = True):
    """Fetch fixture from API and insert into staging with reset state"""
    
    # Clean existing data first (S3 + MongoDB)
    if clean_first:
        clean_fixture_data(fixture_id)
    
    store = FootyMongoStore()
    
    print(f"📡 Fetching fixture {fixture_id} from API...")
    
    url = f'{BASE_URL}/fixtures'
    headers = get_api_headers()
    response = requests.get(url, headers=headers, params={'id': fixture_id})
    
    if response.status_code != 200:
        print(f'❌ API request failed: {response.status_code}')
        print(response.text)
        return False
    
    data = response.json()
    fixtures = data.get('response', [])
    
    if not fixtures:
        print(f'❌ No fixture found with ID {fixture_id}')
        return False
    
    fixture_data = fixtures[0]
    
    # Display original info
    home = fixture_data["teams"]["home"]["name"]
    away = fixture_data["teams"]["away"]["name"]
    home_score = fixture_data['goals']['home']
    away_score = fixture_data['goals']['away']
    original_status = fixture_data["fixture"]["status"]["short"]
    event_count = len(fixture_data.get("events", []))
    
    print(f"✅ Fetched: {home} vs {away}")
    print(f"   Original: {original_status}, {home_score}-{away_score}, {event_count} events")
    
    # Reset to staging state (as if game hasn't started)
    fixture_data["_id"] = fixture_id  # Use fixture ID as document _id
    fixture_data["fixture"]["status"]["long"] = "Not Started"
    fixture_data["fixture"]["status"]["short"] = "NS"
    fixture_data["fixture"]["status"]["elapsed"] = None
    fixture_data["goals"]["home"] = None
    fixture_data["goals"]["away"] = None
    fixture_data["score"]["halftime"]["home"] = None
    fixture_data["score"]["halftime"]["away"] = None
    fixture_data["score"]["fulltime"]["home"] = None
    fixture_data["score"]["fulltime"]["away"] = None
    fixture_data["events"] = []
    
    # Remove optional fields
    for field in ["lineups", "statistics", "players"]:
        if field in fixture_data:
            del fixture_data[field]
    
    print(f"   Reset to: NS, null scores, 0 events")
    
    # Insert into staging
    store.fixtures_staging.insert_one(fixture_data)
    print(f"💾 Inserted into fixtures_staging")
    
    print()
    print("=" * 60)
    print("✅ FIXTURE READY IN STAGING")
    print("=" * 60)
    print()
    print("The MonitorWorkflow runs every minute and will:")
    print("  1. Activate this fixture (staging → active)")
    print("  2. Fetch fresh data from API (with events)")
    print("  3. Debounce events (3 min = 3 cycles)")
    print("  4. Search Twitter for videos")
    print("  5. Download and upload to S3")
    print()
    print("Watch progress:")
    print(f"  📊 Temporal UI:     http://localhost:4100")
    print(f"  🖥️  Twitter Firefox: http://localhost:4103")
    print(f"  🗄️  MongoDB Express: http://localhost:4101")
    print(f"  📝 Worker logs:     docker logs -f found-footy-worker")
    print()
    
    return True


def main():
    parser = argparse.ArgumentParser(
        description="Insert fixture into staging for pipeline testing",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__
    )
    
    parser.add_argument(
        "--fixture-id", "-f",
        type=int,
        nargs="+",
        default=[DEFAULT_FIXTURE_ID],
        help=f"Fixture ID(s) to test (default: {DEFAULT_FIXTURE_ID}). Can specify multiple."
    )
    
    parser.add_argument(
        "--no-clean",
        action="store_true",
        help="Skip cleanup (don't delete existing S3/MongoDB data)"
    )
    
    args = parser.parse_args()
    
    # Handle multiple fixtures
    fixture_ids = args.fixture_id if isinstance(args.fixture_id, list) else [args.fixture_id]
    
    print(f"🚀 Processing {len(fixture_ids)} fixture(s)...")
    print()
    
    success_count = 0
    for fixture_id in fixture_ids:
        print(f"{'='*60}")
        print(f"📋 Fixture {fixture_id}")
        print(f"{'='*60}")
        if insert_fixture_to_staging(fixture_id, clean_first=not args.no_clean):
            success_count += 1
        print()
    
    print(f"{'='*60}")
    print(f"✅ Complete: {success_count}/{len(fixture_ids)} fixtures added to staging")
    print(f"{'='*60}")
    
    exit(0 if success_count == len(fixture_ids) else 1)


if __name__ == "__main__":
    main()
