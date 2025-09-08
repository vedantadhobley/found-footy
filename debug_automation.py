#!/usr/bin/env python3
"""Debug automation naming specifically"""

import asyncio
import json
from prefect import get_client
from prefect.events import emit_event
from datetime import datetime, timezone

async def check_automation_template():
    """Check the actual automation template that's deployed"""
    print("🔍 Checking automation template...")
    
    try:
        async with get_client() as client:
            automations = await client.read_automations()
            
            for automation in automations:
                if "twitter" in automation.name.lower() or "goal" in automation.name.lower():
                    print(f"🤖 Found automation: {automation.name}")
                    print(f"   Enabled: {automation.enabled}")
                    
                    for action in automation.actions:
                        print(f"   Action type: {type(action).__name__}")
                        if hasattr(action, 'flow_run_name'):
                            print(f"   Template: {action.flow_run_name}")
                        else:
                            print(f"   No flow_run_name found on action")
                        print(f"   Parameters: {action.parameters}")
                    print()
            
            return len(automations) > 0
            
    except Exception as e:
        print(f"❌ Error checking automation: {e}")
        return False

async def test_event_with_rich_payload():
    """Test event with realistic goal data"""
    print("🧪 Testing event with realistic goal payload...")
    
    test_payload = {
        "goal_id": "12345_67_890",
        "fixture_id": "12345",
        "team_name": "Inter Miami",
        "player_name": "Lionel Messi",
        "minute": "67",
        "goal_type": "Goal",
        "opponent_name": "LAFC",
        "home_team": "Inter Miami",
        "away_team": "LAFC",
        "match_context": "Inter Miami vs LAFC",
        "detected_at": datetime.now(timezone.utc).isoformat()
    }
    
    print("📋 Event payload:")
    print(json.dumps(test_payload, indent=2))
    print()
    
    try:
        emit_event(
            event="goal.detected",
            resource={"prefect.resource.id": "goal.12345_67_890"},
            payload=test_payload
        )
        
        print("✅ Event emitted successfully!")
        print("🎯 Expected flow run name:")
        print("   ⚽ GOAL: Lionel Messi (67') for Inter Miami vs LAFC [#12345]")
        print()
        print("📊 Check Prefect UI in ~10 seconds for twitter-search-flow run")
        
        return True
        
    except Exception as e:
        print(f"❌ Failed to emit event: {e}")
        return False

async def check_recent_twitter_runs():
    """Check recent twitter flow runs and their names"""
    print("📊 Recent twitter-search-flow runs:")
    
    try:
        async with get_client() as client:
            flow_runs = await client.read_flow_runs(
                flow_filter={"name": {"any_": ["twitter-search-flow"]}},
                limit=5,
                sort="EXPECTED_START_TIME_DESC"
            )
            
            if not flow_runs:
                print("❌ No twitter-search-flow runs found")
                return
            
            for run in flow_runs:
                timestamp = run.created.strftime("%H:%M:%S")
                name = run.name
                goal_id = run.parameters.get('goal_id', 'No goal_id') if run.parameters else 'No params'
                
                print(f"  {timestamp} | {name}")
                print(f"           | goal_id: {goal_id}")
                print()
                
    except Exception as e:
        print(f"❌ Error checking twitter runs: {e}")

async def recreate_automation_with_debug():
    """Recreate automation with enhanced debugging"""
    print("🔧 Recreating automation with debug info...")
    
    try:
        # First clean existing automations
        async with get_client() as client:
            automations = await client.read_automations()
            
            for automation in automations:
                if "twitter" in automation.name.lower() or "goal" in automation.name.lower():
                    await client.delete_automation(automation.id)
                    print(f"🗑️ Deleted: {automation.name}")
        
        # Get deployment
        async with get_client() as client:
            deployment = await client.read_deployment_by_name("twitter-search-flow/twitter-search-flow")
            print(f"📋 Found deployment: {deployment.name} (ID: {deployment.id})")
            
            from prefect.automations import Automation
            from prefect.events.schemas.automations import EventTrigger
            from prefect.events.actions import RunDeployment
            
            # Create automation with simpler template
            automation = Automation(
                name="goal-twitter-automation-debug",
                description="Debug version with enhanced logging",
                enabled=True,
                trigger=EventTrigger(
                    expect=["goal.detected"],
                    match={"prefect.resource.id": "goal.*"},
                    posture="Reactive",
                    threshold=1,
                    within=0,
                ),
                actions=[
                    RunDeployment(
                        deployment_id=deployment.id,
                        parameters={"goal_id": "{{ event.payload.goal_id }}"},
                        flow_run_name="⚽ {{ event.payload.player_name }} ({{ event.payload.minute }}') - {{ event.payload.team_name }} vs {{ event.payload.opponent_name }}"
                    )
                ],
            )
            
            created_automation = await automation.acreate()
            print(f"✅ Created debug automation: {created_automation.name}")
            print(f"🎯 Template: ⚽ {{{{ event.payload.player_name }}}} ({{{{ event.payload.minute }}}}') - {{{{ event.payload.team_name }}}} vs {{{{ event.payload.opponent_name }}}}")
            
            return True
            
    except Exception as e:
        print(f"❌ Failed to recreate automation: {e}")
        return False

async def main():
    print("🔍 TWITTER AUTOMATION NAMING DEBUG")
    print("=" * 50)
    
    # Check current automation
    automation_exists = await check_automation_template()
    
    if not automation_exists:
        print("❌ No automation found!")
        recreate = input("Create new automation? (y/n): ")
        if recreate.lower() == 'y':
            await recreate_automation_with_debug()
    
    # Check recent runs
    await check_recent_twitter_runs()
    
    # Test event emission
    test = input("\nEmit test event? (y/n): ")
    if test.lower() == 'y':
        await test_event_with_rich_payload()
        
        print("\n⏳ Waiting 15 seconds for automation to trigger...")
        await asyncio.sleep(15)
        
        print("\n📊 Checking for new runs...")
        await check_recent_twitter_runs()

if __name__ == "__main__":
    asyncio.run(main())