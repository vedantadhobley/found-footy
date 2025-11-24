"""
Test raw API response for batch fixtures endpoint
"""
import os
import json
import requests

def test_raw_batch_api():
    """Hit the API directly and see what comes back"""
    print("\n" + "=" * 60)
    print("  RAW API TEST: Batch Fixtures Endpoint")
    print("=" * 60)
    
    api_key = os.getenv("API_FOOTBALL_KEY")
    if not api_key:
        print("❌ API_FOOTBALL_KEY not set")
        return False
    
    url = "https://v3.football.api-sports.io/fixtures?ids=1378993-1378994"
    headers = {"x-apisports-key": api_key}
    
    print(f"\n🌐 URL: {url}")
    print(f"🔑 API Key: {api_key[:10]}...{api_key[-4:]}")
    
    try:
        response = requests.get(url, headers=headers, timeout=10)
        print(f"\n📡 Status Code: {response.status_code}")
        
        if response.status_code != 200:
            print(f"❌ Error: {response.text}")
            return False
        
        data = response.json()
        
        print(f"\n🔑 Response top-level keys: {list(data.keys())}")
        
        if 'response' in data:
            fixtures = data['response']
            print(f"✅ Found {len(fixtures)} fixture(s)")
            
            for idx, fixture in enumerate(fixtures, 1):
                print(f"\n{'='*50}")
                print(f"FIXTURE {idx}: {fixture['fixture']['id']}")
                print(f"{'='*50}")
                print(f"Home: {fixture['teams']['home']['name']}")
                print(f"Away: {fixture['teams']['away']['name']}")
                print(f"Score: {fixture['goals']['home']}-{fixture['goals']['away']}")
                print(f"Status: {fixture['fixture']['status']['short']}")
                
                print(f"\n🔑 Fixture keys:")
                for key in sorted(fixture.keys()):
                    if key == 'events' and fixture[key]:
                        print(f"   ✨ {key}: {len(fixture[key])} events")
                    elif isinstance(fixture[key], list):
                        print(f"   - {key}: [{len(fixture[key])} items]")
                    elif isinstance(fixture[key], dict):
                        print(f"   - {key}: {{dict}}")
                    else:
                        print(f"   - {key}: {fixture[key]}")
                
                # Check for events
                if 'events' in fixture and fixture['events']:
                    events = fixture['events']
                    print(f"\n🎉 EVENTS DATA INCLUDED! ({len(events)} events)")
                    
                    event_types = {}
                    for event in events:
                        event_type = event.get('type', 'Unknown')
                        event_types[event_type] = event_types.get(event_type, 0) + 1
                    
                    print(f"📊 Event types:")
                    for event_type, count in event_types.items():
                        print(f"   {event_type}: {count}")
                    
                    goal_events = [e for e in events if e.get('type') == 'Goal']
                    if goal_events:
                        print(f"\n⚽ Goals:")
                        for goal in goal_events:
                            player = goal.get('player', {}).get('name', 'Unknown')
                            team = goal.get('team', {}).get('name', 'Unknown')
                            minute = goal.get('time', {}).get('elapsed', '?')
                            detail = goal.get('detail', '')
                            print(f"   {minute}' - {player} ({team}) [{detail}]")
                else:
                    print(f"\n❌ No 'events' key or empty events")
            
            # Summary
            print(f"\n" + "=" * 60)
            if any('events' in f and f['events'] for f in fixtures):
                print("✅ SUCCESS: Batch endpoint INCLUDES events data!")
                print("\n💡 Simplified workflow possible:")
                print("   • 1 batch call = fixtures + events + scores")
                print("   • No separate fixtures_events() needed")
                print("   • Massive API request savings!")
            else:
                print("❌ Events data NOT included in batch response")
            print("=" * 60)
            
            return True
        else:
            print("❌ No 'response' key in API result")
            return False
            
    except Exception as e:
        print(f"\n❌ Error: {e}")
        import traceback
        traceback.print_exc()
        return False

if __name__ == "__main__":
    import sys
    success = test_raw_batch_api()
    sys.exit(0 if success else 1)
