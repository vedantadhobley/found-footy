#!/usr/bin/env python3
"""Debug event emission and automation triggering"""

import asyncio
from prefect import get_client
from datetime import datetime, timezone

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
            
            if not automations:
                print("❌ No automations found!")
                return False
            
            for automation in automations:
                print(f"🤖 Automation: {automation.name}")
                print(f"   ID: {automation.id}")
                print(f"   Enabled: {automation.enabled}")
                print(f"   Trigger: {automation.trigger}")
                print(f"   Actions: {len(automation.actions)}")
                
            return True
                
    except Exception as e:
        print(f"❌ Error checking automations: {e}")
        return False

async def check_recent_events():
    """Check recent events to see if they're being recorded"""
    print("📡 Checking recent events...")
    
    try:
        async with get_client() as client:
            # Get recent events (last 10)
            events = await client.read_events(limit=10)
            
            if not events:
                print("❌ No recent events found")
                return
            
            print(f"📊 Found {len(events)} recent events:")
            for event in events[-5:]:  # Show last 5
                print(f"   {event.event} - {event.resource}")
                
    except Exception as e:
        print(f"❌ Error checking events: {e}")

async def timeline_viewer():
    """Enhanced timeline viewer for goal processing"""
    print("🕐 ENHANCED TIMELINE VIEWER - Watching for goal processing...")
    print("="*80)
    
    last_check = datetime.now(timezone.utc)
    
    while True:
        try:
            async with get_client() as client:
                # Get recent flow runs for both flows
                flow_runs = await client.read_flow_runs(
                    flow_filter={"name": {"any_": ["twitter-flow", "fixtures-flow"]}},
                    limit=50,
                    sort="EXPECTED_START_TIME_DESC"
                )
                
                # Filter to new runs since last check
                new_runs = [
                    run for run in flow_runs 
                    if run.created >= last_check
                ]
                
                if new_runs:
                    print(f"\n🚨 {len(new_runs)} NEW FLOWS DETECTED:")
                    print(f"{'Time':<10} {'Type':<12} {'Status':<10} {'Name':<50}")
                    print("-" * 85)
                    
                    for run in reversed(new_runs):  # Show in chronological order
                        timestamp = run.created.strftime("%H:%M:%S")
                        flow_type = "🐦 Twitter" if "twitter" in run.flow_name else "⚽ Fixtures"
                        status_icon = "🟢" if run.state.is_completed() else "🟡" if run.state.is_running() else "🔴" if run.state.is_failed() else "⚪"
                        status = run.state.name[:8]
                        name = run.name[:45] + "..." if len(run.name) > 45 else run.name
                        
                        print(f"{timestamp:<10} {flow_type:<12} {status_icon} {status:<8} {name}")
                
                last_check = datetime.now(timezone.utc)
                
        except Exception as e:
            print(f"❌ Timeline viewer error: {e}")
        
        # Check every 3 seconds for more responsive timeline
        await asyncio.sleep(3)

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
        await test_event_emission()
    else:
        print("❌ Skipping event test - no automations found")
    
    print("="*50)

if __name__ == "__main__":
    asyncio.run(main())