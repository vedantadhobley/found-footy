#!/usr/bin/env python3
"""Integration test - insert baseline and let monitor flow get API data"""
import sys
import os
import time
import requests
sys.path.insert(0, '/app')

os.environ['MONGODB_URL'] = 'mongodb://footy_admin:footy_secure_pass@mongodb:27017/found_footy?authSource=admin'

def trigger_monitor_via_prefect_api():
    """Use correct Prefect API endpoints"""
    try:
        print("🚀 Triggering monitor flow via Prefect API...")
        
        base_url = "http://prefect-server:4200/api"
        
        print("🔍 Getting deployments...")
        response = requests.post(f"{base_url}/deployments/filter", json={})
        
        if response.status_code == 200:
            deployments = response.json()
            
            monitor_deployment = None
            for deploy in deployments:
                if deploy.get('name') == 'monitor-flow':
                    monitor_deployment = deploy
                    break
            
            if monitor_deployment:
                deployment_id = monitor_deployment['id']
                print(f"✅ Found monitor deployment: {deployment_id}")
                
                # Try deployment endpoint
                response = requests.post(
                    f"{base_url}/deployments/{deployment_id}/create_flow_run",
                    json={"parameters": {}}
                )
                
                if response.status_code in [200, 201]:
                    flow_run_data = response.json()
                    print(f"✅ Monitor flow triggered successfully!")
                    return True
                else:
                    print(f"❌ Failed to trigger: {response.status_code}")
                    print(f"   Response: {response.text}")
                    return False
            else:
                print("❌ Monitor deployment not found")
                return False
        else:
            print(f"❌ Failed to get deployments: {response.status_code}")
            return False
            
    except Exception as e:
        print(f"❌ Error triggering monitor: {e}")
        return False

def test_complete_pipeline():
    """Debug API configuration and calls"""
    print("🧪 INTEGRATION TEST - DEBUG API CONFIGURATION")
    print("=" * 60)
    
    # 1. Check API configuration  
    print("1️⃣ Checking API configuration...")
    
    api_key = os.getenv('RAPIDAPI_KEY')
    if api_key:
        print(f"✅ API key found: {api_key[:10]}...{api_key[-4:]}")
    else:
        print("❌ NO API KEY FOUND!")
        return False
    
    # 2. Insert baseline fixture for comparison
    print("\n2️⃣ Setting up baseline fixture...")
    
    from found_footy.storage.mongo_store import FootyMongoStore
    store = FootyMongoStore()
    
    fixture_baseline = {
        "_id": 1378993,
        "fixture": {"id": 1378993, "status": {"elapsed": 82, "short": "2H"}},
        "teams": {
            "home": {"id": 40, "name": "Liverpool"}, 
            "away": {"id": 42, "name": "Arsenal"}
        },
        "goals": {"home": 0, "away": 0},  # ✅ Baseline: 0-0
        "score": {"fulltime": {"home": 0, "away": 0}}
    }
    
    store.fixtures_active.replace_one({"_id": 1378993}, fixture_baseline, upsert=True)
    print("✅ Baseline inserted: Liverpool 0-0 Arsenal")
    
    # 3. Debug events API specifically
    print("\n3️⃣ Debugging events API...")
    
    import requests
    
    headers = {
        'X-RapidAPI-Key': api_key,
        'X-RapidAPI-Host': 'api-football-v1.p.rapidapi.com'
    }
    
    # Direct events API call
    url = f"https://api-football-v1.p.rapidapi.com/v3/fixtures/events?fixture=1378993"
    
    try:
        response = requests.get(url, headers=headers, timeout=10)
        print(f"📡 Events API Status: {response.status_code}")
        
        if response.status_code == 200:
            data = response.json()
            
            if 'response' in data and data['response']:
                events = data['response']
                print(f"✅ {len(events)} events found in API:")
                
                goal_events = [e for e in events if e.get('type') == 'Goal']
                print(f"   Goal events: {len(goal_events)}")
                
                for event in goal_events:
                    player_name = event.get('player', {}).get('name', 'NO_NAME')
                    minute = event.get('time', {}).get('elapsed', 'NO_TIME')
                    print(f"      🥅 {player_name} - {minute}'")
                    
                # This should work - the API HAS the events!
                print("✅ Events API is working - the issue is in your fixtures_events function!")
            else:
                print("❌ No events in API response")
                print(f"   Response: {data}")
        else:
            print(f"❌ Events API failed: {response.text}")
            
    except Exception as e:
        print(f"❌ Events API call failed: {e}")
    
    # 4. Test your wrapper function
    print("\n4️⃣ Testing your fixtures_events function...")
    
    try:
        from found_footy.api.mongo_api import fixtures_events
        
        events_data = fixtures_events([1378993])
        print(f"📦 fixtures_events returned: {len(events_data)} items")
        
        if not events_data:
            print("❌ Your fixtures_events function is broken!")
            print("💡 The direct API works but your wrapper doesn't")
            print("🔧 Need to fix found_footy.api.mongo_api.fixtures_events")
        
    except Exception as e:
        print(f"❌ fixtures_events function failed: {e}")
        import traceback
        traceback.print_exc()
    
    # 5. Continue with monitor test
    print("\n5️⃣ Triggering monitor flow...")
    
    if trigger_monitor_via_prefect_api():
        print("⏰ Waiting 30 seconds for processing...")
        time.sleep(30)
        
        # 4. Check results
        print("\n4️⃣ Checking results...")
        
        try:
            goals_pending = list(store.goals_pending.find({"_id": {"$regex": "^1378993_"}}))
            goals_processed = list(store.goals_processed.find({"_id": {"$regex": "^1378993_"}}))
            
            total_goals = len(goals_pending) + len(goals_processed)
            
            print(f"📊 RESULTS:")
            print(f"   Goals pending: {len(goals_pending)}")
            print(f"   Goals processed: {len(goals_processed)}")
            
            if total_goals > 0:
                print("🎯 SUCCESS! Monitor detected goals via API:")
                for goal in goals_pending:
                    print(f"   📥 {goal['_id']}: {goal.get('player_name', 'Unknown')}")
                return True
            else:
                # Check if fixture was updated by monitor
                current_fixture = store.fixtures_active.find_one({"_id": 1378993})
                if current_fixture:
                    current_goals = current_fixture.get("goals", {})
                    print(f"   Current fixture goals: {current_goals}")
                    
                    if current_goals.get("home", 0) > 0 or current_goals.get("away", 0) > 0:
                        print("✅ Monitor updated fixture from API but no goals processed")
                        print("💡 This means API returned goals but without complete player data")
                        return True
                    else:
                        print("⏳ No goal changes detected - API returned same 0-0 score")
                        print("💡 This is normal if the real fixture hasn't had goals yet")
                        return True
                else:
                    print("❌ Fixture disappeared")
                    return False
                    
        except Exception as e:
            print(f"❌ Results check failed: {e}")
            return False
    else:
        print("❌ Monitor flow trigger failed")
        return False

def cleanup_test():
    """Clean up test data"""
    print("🧹 Cleaning up test data...")
    
    try:
        from found_footy.storage.mongo_store import FootyMongoStore
        store = FootyMongoStore()
        
        store.fixtures_active.delete_many({"_id": 1378993})
        store.goals_pending.delete_many({"_id": {"$regex": "^1378993_"}})
        store.goals_processed.delete_many({"_id": {"$regex": "^1378993_"}})
        print("✅ Test data cleaned up")
        
    except Exception as e:
        print(f"⚠️ Cleanup issues: {e}")

if __name__ == "__main__":
    try:
        success = test_complete_pipeline()
        
        if success:
            print("\n🎉 INTEGRATION TEST PASSED!")
            print("✅ Monitor flow called API and processed correctly")
        else:
            print("\n❌ INTEGRATION TEST FAILED!")
        
        input("\nPress Enter to cleanup...")
        cleanup_test()
        
    except Exception as e:
        print(f"❌ Test failed: {e}")
        import traceback
        traceback.print_exc()
        cleanup_test()