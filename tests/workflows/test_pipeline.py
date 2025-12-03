#!/usr/bin/env python3
"""
Full Pipeline Test Script

Fetches a real fixture from API-Football, inserts it into staging with NS status
and empty events, then runs MonitorWorkflow to test the complete end-to-end pipeline:
  
  staging (NS) → active (activated) → fetch events → debounce → twitter → download → S3

This triggers the full cascade:
  - MonitorWorkflow activates fixture and detects events
  - MonitorWorkflow triggers EventWorkflow (child) per fixture
  - EventWorkflow debounces events (3 stable cycles)
  - EventWorkflow triggers TwitterWorkflow (child) per stable event
  - TwitterWorkflow searches for videos and triggers DownloadWorkflow (child)
  - DownloadWorkflow downloads, dedupes, and uploads to S3

Usage:
    # Test with default fixture (1378993 - Liverpool vs Arsenal)
    docker exec found-footy-worker python /workspace/tests/workflows/test_pipeline.py
    
    # Test with specific fixture ID
    docker exec found-footy-worker python /workspace/tests/workflows/test_pipeline.py --fixture-id 1234567
"""
import asyncio
import argparse
import os
import sys
import requests
from temporalio.client import Client

from src.workflows.monitor_workflow import MonitorWorkflow
from src.data.mongo_store import FootyMongoStore
from src.api.api_client import get_api_headers, BASE_URL

# Default fixture for testing
DEFAULT_FIXTURE_ID = 1378993  # Liverpool vs Arsenal, 1-0


def setup_fixture_in_staging(fixture_id: int, store: FootyMongoStore):
    """Fetch real fixture from API and insert into staging with NS status and empty events"""
    print(f"📡 Fetching fixture {fixture_id} from API...")
    
    url = f'{BASE_URL}/fixtures'
    headers = get_api_headers()
    response = requests.get(url, headers=headers, params={'id': fixture_id})
    
    if response.status_code != 200:
        print(f'❌ API request failed: {response.status_code}')
        print(response.text)
        return None
    
    data = response.json()
    fixtures = data.get('response', [])
    
    if not fixtures:
        print(f'❌ No fixture found with ID {fixture_id}')
        return None
    
    fixture_data = fixtures[0]
    
    # Display info
    home = fixture_data["teams"]["home"]["name"]
    away = fixture_data["teams"]["away"]["name"]
    score = f"{fixture_data['goals']['home']}-{fixture_data['goals']['away']}"
    original_status = fixture_data["fixture"]["status"]["long"]
    event_count = len(fixture_data.get("events", []))
    
    print(f"✅ Fetched: {home} vs {away} ({score})")
    print(f"   Original status: {original_status}")
    print(f"   Original events: {event_count}")
    
    # Force NS status and empty events (monitor will activate and fetch fresh)
    fixture_data["fixture"]["status"]["long"] = "Not Started"
    fixture_data["fixture"]["status"]["short"] = "NS"
    fixture_data["fixture"]["status"]["elapsed"] = 0
    fixture_data["events"] = []
    
    if "lineups" in fixture_data:
        del fixture_data["lineups"]
    
    print(f"   Modified to: NS status, 0 events (will be fetched by monitor)")
    
    # Clean existing data
    print(f"🧹 Cleaning existing data for fixture {fixture_id}...")
    for collection_name in ["fixtures_staging", "fixtures_live", "fixtures_active", "fixtures_completed"]:
        result = store.db[collection_name].delete_one({"fixture.id": fixture_id})
        if result.deleted_count > 0:
            print(f"   ✓ Removed from {collection_name}")
    
    # Insert into staging
    print(f"💾 Inserting into fixtures_staging...")
    store.fixtures_staging.insert_one(fixture_data)
    
    return fixture_data


async def test_full_pipeline(fixture_id: int):
    """Setup fixture in staging and run full pipeline via MonitorWorkflow"""
    print("=" * 70)
    print("FULL PIPELINE TEST")
    print("=" * 70)
    print()
    
    store = FootyMongoStore()
    
    print("🔧 SETUP: Fetching and inserting into staging...")
    print("-" * 70)
    fixture_data = setup_fixture_in_staging(fixture_id, store)
    
    if not fixture_data:
        return
    
    print()
    print("✅ Fixture ready in staging with NS status and empty events")
    print("   Monitor will:")
    print("   1. Activate fixture (staging → active)")
    print("   2. Fetch full data from API (with events)")
    print("   3. Trigger EventWorkflow for debounce")
    print()
    
    print("🎯 RUNNING: MonitorWorkflow")
    print("-" * 70)
    
    temporal_host = os.getenv("TEMPORAL_HOST", "localhost:7233")
    client = await Client.connect(temporal_host)
    print(f"🔌 Connected to Temporal at {temporal_host}")
    print()
    
    result = await client.execute_workflow(
        MonitorWorkflow.run,
        id=f"test-monitor-{fixture_id}",
        task_queue="found-footy",
    )
    
    print()
    print("=" * 70)
    print("✅ WORKFLOW COMPLETED")
    print("=" * 70)
    print(f"📊 Results: {result}")
    print()
    
    # Check fixture status
    print("🔍 Checking fixture status in MongoDB...")
    store = FootyMongoStore()
    active_fixture = store.get_fixture_from_active(fixture_id)
    
    if active_fixture:
        events = active_fixture.get("events", [])
        enhanced_events = [e for e in events if e.get("_event_id")]
        
        print(f"✅ Fixture {fixture_id} in fixtures_active")
        print(f"   Status: {active_fixture.get('fixture', {}).get('status', {}).get('short', 'Unknown')}")
        print(f"   Total events: {len(events)}")
        print(f"   Enhanced events: {len(enhanced_events)}")
        
        if enhanced_events:
            print(f"\n   Enhanced events:")
            for event in enhanced_events[:3]:
                print(f"     - {event.get('_event_id')}: {event.get('type')} by {event.get('player', {}).get('name', 'Unknown')}")
                print(f"       stable_count={event.get('_stable_count', 0)}, debounce_complete={event.get('_debounce_complete', False)}")
    else:
        completed = store.db.fixtures_completed.find_one({"fixture.id": fixture_id})
        if completed:
            print(f"✅ Fixture {fixture_id} moved to fixtures_completed")
        else:
            print(f"❌ Fixture {fixture_id} not found in active or completed")
    
    print()
    print("Verification:")
    print(f"  1. Temporal UI: http://localhost:4100")
    print(f"     - Check for 'event-{fixture_id}-*' child workflows")
    print(f"  2. MongoDB Express: http://localhost:4101")
    print(f"     - Check fixtures_active collection")
    print(f"  3. Worker logs: docker logs found-footy-worker --tail 50")
    print()


def main():
    parser = argparse.ArgumentParser(
        description="Full Pipeline Test - Tests complete end-to-end workflow cascade",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__
    )
    
    parser.add_argument("--fixture-id", type=int, default=DEFAULT_FIXTURE_ID,
                       help=f"Fixture ID to test with (default: {DEFAULT_FIXTURE_ID})")
    
    args = parser.parse_args()
    
    try:
        asyncio.run(test_full_pipeline(args.fixture_id))
    
    except KeyboardInterrupt:
        print("\n\n⚠️  Test interrupted")
        sys.exit(130)
    except Exception as e:
        print(f"\n\n❌ Test failed: {e}")
        import traceback
        traceback.print_exc()
        sys.exit(1)


if __name__ == "__main__":
    main()
