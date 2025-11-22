"""
Comprehensive test suite for all mongo_api endpoints with api-football.com
"""
import os
import sys
from datetime import date, datetime, timedelta

from dotenv import load_dotenv

# Load environment variables
load_dotenv()
sys.path.insert(0, '/workspace')

from src.api.mongo_api import (
    filter_fixtures_by_teams,
    fixtures,
    fixtures_batch,
    fixtures_events,
    get_api_headers,
    parse_team_ids_parameter,
)


def print_section(title):
    """Print a formatted section header"""
    print(f"\n{'=' * 60}")
    print(f"  {title}")
    print(f"{'=' * 60}\n")

def test_get_api_headers():
    """Test 1: API Headers Configuration"""
    print_section("TEST 1: API Headers Configuration")
    
    try:
        headers = get_api_headers()
        print(f"✅ Headers retrieved successfully")
        print(f"   Keys: {list(headers.keys())}")
        print(f"   x-apisports-key: {headers['x-apisports-key'][:10]}...{headers['x-apisports-key'][-4:]}")
        return True
    except Exception as e:
        print(f"❌ Failed: {e}")
        return False

def test_fixtures_today():
    """Test 2: Get fixtures for today"""
    print_section("TEST 2: Fixtures for Today")
    
    try:
        today_fixtures = fixtures(date.today())
        print(f"✅ Found {len(today_fixtures)} fixtures for today")
        
        if today_fixtures:
            fixture = today_fixtures[0]
            print(f"\n📋 Sample fixture:")
            print(f"   ID: {fixture['fixture']['id']}")
            print(f"   Home: {fixture['teams']['home']['name']}")
            print(f"   Away: {fixture['teams']['away']['name']}")
            print(f"   League: {fixture['league']['name']}")
            print(f"   Status: {fixture['fixture']['status']['long']}")
            print(f"   Date: {fixture['fixture']['date']}")
        
        return True
    except Exception as e:
        print(f"❌ Failed: {e}")
        import traceback
        traceback.print_exc()
        return False

def test_fixtures_specific_date():
    """Test 3: Get fixtures for a specific date (yesterday)"""
    print_section("TEST 3: Fixtures for Specific Date (Yesterday)")
    
    try:
        yesterday = date.today() - timedelta(days=1)
        yesterday_fixtures = fixtures(yesterday)
        print(f"✅ Found {len(yesterday_fixtures)} fixtures for {yesterday}")
        
        if yesterday_fixtures:
            # Find a finished match
            finished = [f for f in yesterday_fixtures if f['fixture']['status']['short'] == 'FT']
            if finished:
                fixture = finished[0]
                print(f"\n📋 Sample finished match:")
                print(f"   ID: {fixture['fixture']['id']}")
                print(f"   {fixture['teams']['home']['name']} {fixture['goals']['home']} - {fixture['goals']['away']} {fixture['teams']['away']['name']}")
                print(f"   League: {fixture['league']['name']}")
                
                # Save fixture ID for events test
                return fixture['fixture']['id']
        
        return True
    except Exception as e:
        print(f"❌ Failed: {e}")
        import traceback
        traceback.print_exc()
        return False

def test_fixtures_events(fixture_id):
    """Test 4: Get events for a specific fixture"""
    print_section(f"TEST 4: Fixture Events (Fixture ID: {fixture_id})")
    
    try:
        events = fixtures_events(fixture_id)
        print(f"✅ Found {len(events)} events for fixture {fixture_id}")
        
        if events:
            # Count event types
            event_types = {}
            for event in events:
                event_type = event.get('type', 'Unknown')
                event_types[event_type] = event_types.get(event_type, 0) + 1
            
            print(f"\n📊 Event breakdown:")
            for event_type, count in event_types.items():
                print(f"   {event_type}: {count}")
            
            # Show goals
            goal_events = [e for e in events if e.get('type') == 'Goal']
            if goal_events:
                print(f"\n⚽ Goals:")
                for goal in goal_events:
                    player = goal.get('player', {}).get('name', 'Unknown')
                    team = goal.get('team', {}).get('name', 'Unknown')
                    minute = goal.get('time', {}).get('elapsed', '?')
                    detail = goal.get('detail', '')
                    print(f"   {minute}' - {player} ({team}) [{detail}]")
        
        return True
    except Exception as e:
        print(f"❌ Failed: {e}")
        import traceback
        traceback.print_exc()
        return False

def test_fixtures_batch():
    """Test 5: Get multiple fixtures by IDs"""
    print_section("TEST 5: Batch Fixtures by IDs")
    
    try:
        # Get some fixture IDs from today
        today_fixtures = fixtures(date.today())
        if len(today_fixtures) < 3:
            print(f"⚠️  Not enough fixtures today, using yesterday's fixtures")
            yesterday = date.today() - timedelta(days=1)
            today_fixtures = fixtures(yesterday)
        
        # Get first 3 fixture IDs
        fixture_ids = [f['fixture']['id'] for f in today_fixtures[:3]]
        print(f"   Testing with fixture IDs: {fixture_ids}")
        
        batch_fixtures = fixtures_batch(fixture_ids)
        print(f"✅ Retrieved {len(batch_fixtures)} fixtures in batch")
        
        for fixture in batch_fixtures:
            home = fixture['teams']['home']['name']
            away = fixture['teams']['away']['name']
            print(f"   - {fixture['fixture']['id']}: {home} vs {away}")
        
        return True
    except Exception as e:
        print(f"❌ Failed: {e}")
        import traceback
        traceback.print_exc()
        return False

def test_filter_fixtures_by_teams():
    """Test 6: Filter fixtures by team IDs"""
    print_section("TEST 6: Filter Fixtures by Team IDs")
    
    try:
        # Get fixtures
        today_fixtures = fixtures(date.today())
        if not today_fixtures:
            yesterday = date.today() - timedelta(days=1)
            today_fixtures = fixtures(yesterday)
        
        # Get some team IDs from the fixtures
        team_ids = [
            today_fixtures[0]['teams']['home']['id'],
            today_fixtures[1]['teams']['home']['id'] if len(today_fixtures) > 1 else None
        ]
        team_ids = [tid for tid in team_ids if tid]
        
        print(f"   Filtering for team IDs: {team_ids}")
        
        filtered = filter_fixtures_by_teams(today_fixtures, team_ids)
        print(f"✅ Filtered to {len(filtered)} fixtures from {len(today_fixtures)} total")
        
        for fixture in filtered[:3]:
            home = fixture['teams']['home']['name']
            home_id = fixture['teams']['home']['id']
            away = fixture['teams']['away']['name']
            away_id = fixture['teams']['away']['id']
            print(f"   - {home} (ID:{home_id}) vs {away} (ID:{away_id})")
        
        return True
    except Exception as e:
        print(f"❌ Failed: {e}")
        import traceback
        traceback.print_exc()
        return False

def test_parse_team_ids_parameter():
    """Test 7: Parse team IDs from various formats"""
    print_section("TEST 7: Parse Team IDs Parameter")
    
    test_cases = [
        (None, []),
        ("", []),
        ("42", [42]),
        ("42,33,50", [42, 33, 50]),
        ([42, 33, 50], [42, 33, 50]),
        (42, [42]),
        ("  42 ,  33  ", [42, 33]),
    ]
    
    all_passed = True
    for input_val, expected in test_cases:
        try:
            result = parse_team_ids_parameter(input_val)
            if result == expected:
                print(f"✅ {repr(input_val):30s} -> {result}")
            else:
                print(f"❌ {repr(input_val):30s} -> {result} (expected {expected})")
                all_passed = False
        except Exception as e:
            print(f"❌ {repr(input_val):30s} -> Error: {e}")
            all_passed = False
    
    return all_passed

def test_empty_batch():
    """Test 8: Empty batch request"""
    print_section("TEST 8: Empty Batch Request")
    
    try:
        result = fixtures_batch([])
        if result == []:
            print(f"✅ Empty batch returns empty list")
            return True
        else:
            print(f"❌ Expected empty list, got: {result}")
            return False
    except Exception as e:
        print(f"❌ Failed: {e}")
        return False

def test_date_coercion():
    """Test 9: Date parameter formats"""
    print_section("TEST 9: Date Parameter Formats")
    
    try:
        # Test with date object
        result1 = fixtures(date(2024, 11, 20))
        print(f"✅ date(2024, 11, 20): {len(result1)} fixtures")
        
        # Test with string YYYY-MM-DD
        result2 = fixtures("2024-11-20")
        print(f"✅ '2024-11-20': {len(result2)} fixtures")
        
        # Test with string YYYYMMDD
        result3 = fixtures("20241120")
        print(f"✅ '20241120': {len(result3)} fixtures")
        
        # Test with None (should use today)
        result4 = fixtures(None)
        print(f"✅ None (today): {len(result4)} fixtures")
        
        return True
    except Exception as e:
        print(f"❌ Failed: {e}")
        import traceback
        traceback.print_exc()
        return False

def main():
    """Run all tests"""
    print("\n" + "=" * 60)
    print("  API-FOOTBALL.COM ENDPOINT TEST SUITE")
    print("=" * 60)
    
    results = {}
    
    # Test 1: Headers
    results['headers'] = test_get_api_headers()
    
    # Test 2: Today's fixtures
    results['fixtures_today'] = test_fixtures_today()
    
    # Test 3: Yesterday's fixtures (get a finished match ID)
    fixture_id = test_fixtures_specific_date()
    results['fixtures_date'] = fixture_id is not None
    
    # Test 4: Fixture events (if we have a fixture ID)
    if fixture_id and isinstance(fixture_id, int):
        results['fixtures_events'] = test_fixtures_events(fixture_id)
    else:
        print_section("TEST 4: Fixture Events")
        print("⚠️  Skipped - no fixture ID available")
        results['fixtures_events'] = None
    
    # Test 5: Batch fixtures
    results['fixtures_batch'] = test_fixtures_batch()
    
    # Test 6: Filter by teams
    results['filter_by_teams'] = test_filter_fixtures_by_teams()
    
    # Test 7: Parse team IDs
    results['parse_team_ids'] = test_parse_team_ids_parameter()
    
    # Test 8: Empty batch
    results['empty_batch'] = test_empty_batch()
    
    # Test 9: Date coercion
    results['date_coercion'] = test_date_coercion()
    
    # Summary
    print_section("TEST SUMMARY")
    passed = sum(1 for v in results.values() if v is True)
    failed = sum(1 for v in results.values() if v is False)
    skipped = sum(1 for v in results.values() if v is None)
    total = len(results)
    
    for test_name, result in results.items():
        status = "✅ PASS" if result is True else ("❌ FAIL" if result is False else "⚠️  SKIP")
        print(f"{status}  {test_name}")
    
    print(f"\n{'=' * 60}")
    print(f"  Total: {total} | Passed: {passed} | Failed: {failed} | Skipped: {skipped}")
    print(f"{'=' * 60}\n")
    
    if failed == 0:
        print("🎉 All tests passed!")
        return 0
    else:
        print(f"⚠️  {failed} test(s) failed")
        return 1

if __name__ == "__main__":
    exit(main())
