#!/usr/bin/env python3
"""Debug event emission and automation triggering"""
import asyncio
from prefect import get_client

async def test_event_emission():
    """Test emitting a goal.detected event manually"""
    print("🧪 Testing manual event emission...")
    
    try:
        from prefect.events import emit_event
        
        test_goal_id = "test_12345_67_890"
        
        emit_event(
            event="goal.detected",
            resource={"prefect.resource.id": f"goal.{test_goal_id}"},
            payload={
                "goal_id": test_goal_id,
                "fixture_id": 12345,
                "team_name": "Test Team",
                "player_name": "Test Player",
                "minute": 67,
                "goal_type": "Goal"
            }
        )
        
        print(f"✅ Test event emitted for goal {test_goal_id}")
        print(f"🎯 Check Prefect UI for twitter-flow run with goal_id: {test_goal_id}")
        return True
        
    except Exception as e:
        print(f"❌ Failed to emit test event: {e}")
        return False

async def check_automation_details():
    """Check automation configuration in detail"""
    print("🔍 Checking automation details...")
    
    try:
        async with get_client() as client:
            automations = await client.read_automations()
            
            twitter_automation = None
            for automation in automations:
                if "twitter" in automation.name.lower() or "goal" in automation.name.lower():
                    twitter_automation = automation
                    break
            
            if twitter_automation:
                print(f"✅ Found automation: {twitter_automation.name}")
                print(f"   ID: {twitter_automation.id}")
                print(f"   Enabled: {twitter_automation.enabled}")
                print(f"   Trigger type: {twitter_automation.trigger.get('type')}")
                print(f"   Expected events: {twitter_automation.trigger.get('expect')}")
                print(f"   Match criteria: {twitter_automation.trigger.get('match')}")
                print(f"   Actions: {len(twitter_automation.actions)} actions")
                
                for i, action in enumerate(twitter_automation.actions):
                    print(f"   Action {i+1}:")
                    print(f"     Type: {action.get('type')}")
                    print(f"     Deployment ID: {action.get('deployment_id')}")
                    print(f"     Parameters: {action.get('parameters')}")
                
                return True
            else:
                print("❌ No automation found!")
                return False
                
    except Exception as e:
        print(f"❌ Error checking automation: {e}")
        return False

async def check_recent_events():
    """Check recent events to see if they're being recorded"""
    print("📡 Checking recent events...")
    
    try:
        async with get_client() as client:
            # Try to get recent events
            try:
                response = await client._client.post("/events/filter", json={
                    "event": {"any_": ["goal.detected"]},
                    "limit": 5,
                    "order": "DESC"
                })
                
                if response.status_code == 200:
                    events = response.json()
                    print(f"🎯 Found {len(events)} recent goal.detected events:")
                    
                    for event in events:
                        print(f"   • Event ID: {event.get('id')}")
                        print(f"     Occurred: {event.get('occurred')}")
                        print(f"     Payload: {event.get('payload')}")
                        print()
                else:
                    print(f"⚠️ Failed to fetch events: {response.status_code}")
                    
            except Exception as e:
                print(f"⚠️ Could not fetch events (might not be supported): {e}")
                
    except Exception as e:
        print(f"❌ Error checking events: {e}")

async def main():
    """Run all diagnostic checks"""
    print("🚀 Starting event automation diagnostics...")
    print("="*50)
    
    # Check automation configuration
    automation_ok = await check_automation_details()
    print()
    
    # Check recent events
    await check_recent_events()
    print()
    
    # Test manual event emission
    if automation_ok:
        test_ok = await test_event_emission()
        print()
        
        if test_ok:
            print("⏳ Wait 30 seconds and check Prefect UI for twitter-flow run...")
            print("🌐 Go to: http://localhost:4200/runs")
            print("🔍 Look for flow run with goal_id 'test_12345_67_890'")
        else:
            print("❌ Test event emission failed - automation won't work")
    else:
        print("❌ Automation not found - cannot test")
    
    print("="*50)

if __name__ == "__main__":
    asyncio.run(main())