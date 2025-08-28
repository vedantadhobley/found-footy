import argparse
import sys
import subprocess
from pathlib import Path
from prefect import get_client

async def ensure_work_pools():
    """Ensure work pools exist before creating deployments using CLI"""
    pools = ["fixtures-pool", "youtube-pool"]
    
    for pool_name in pools:
        try:
            # Check if pool exists using CLI
            result = subprocess.run(
                ["prefect", "work-pool", "inspect", pool_name], 
                capture_output=True, 
                text=True
            )
            
            if result.returncode == 0:
                print(f"✅ {pool_name} already exists")
            else:
                print(f"🔧 Creating {pool_name}...")
                # Create pool using CLI
                create_result = subprocess.run(
                    ["prefect", "work-pool", "create", pool_name, "--type", "process"],
                    capture_output=True,
                    text=True
                )
                
                if create_result.returncode == 0:
                    print(f"✅ Created {pool_name}")
                else:
                    print(f"❌ Failed to create {pool_name}: {create_result.stderr}")
                    raise Exception(f"Failed to create work pool {pool_name}")
                    
        except Exception as e:
            print(f"❌ Error managing work pool {pool_name}: {e}")
            raise

async def clean_all_deployments_api():
    """Clean up ALL existing deployments using Prefect client API"""
    print("🧹 CLEANING ALL EXISTING DEPLOYMENTS (using API)...")
    print("💣 This will delete EVERY deployment in the system!")
    
    try:
        async with get_client() as client:
            # Get all deployments
            deployments = await client.read_deployments()
            
            if not deployments:
                print("ℹ️ No deployments found to delete")
                return
            
            print(f"🎯 Found {len(deployments)} deployments to delete:")
            for deployment in deployments:
                print(f"  - {deployment.name}")
            
            # Delete each deployment
            deleted_count = 0
            for deployment in deployments:
                try:
                    await client.delete_deployment(deployment.id)
                    print(f"✅ Deleted: {deployment.name}")
                    deleted_count += 1
                except Exception as e:
                    print(f"⚠️ Failed to delete {deployment.name}: {e}")
            
            print(f"✅ API cleanup completed - deleted {deleted_count} deployments")
            
    except Exception as e:
        print(f"⚠️ Error in API cleanup: {e}")
        print("✅ Continuing with deployment creation...")

async def clean_all_automations():
    """Clean up ALL existing automations"""
    print("🤖 CLEANING ALL EXISTING AUTOMATIONS...")
    
    try:
        async with get_client() as client:
            # Get all automations
            automations = await client.read_automations()
            
            if not automations:
                print("ℹ️ No automations found to delete")
                return
            
            print(f"🎯 Found {len(automations)} automations to delete:")
            for automation in automations:
                print(f"  - {automation.name}")
            
            # Delete each automation
            deleted_count = 0
            for automation in automations:
                try:
                    await client.delete_automation(automation.id)
                    print(f"✅ Deleted automation: {automation.name}")
                    deleted_count += 1
                except Exception as e:
                    print(f"⚠️ Failed to delete automation {automation.name}: {e}")
            
            print(f"✅ Automation cleanup completed - deleted {deleted_count} automations")
            
    except Exception as e:
        print(f"⚠️ Error in automation cleanup: {e}")

async def create_youtube_automation():
    """Create the YouTube automation using the Prefect API - FIXED"""
    print("🤖 Creating YouTube automation using Prefect API...")
    
    try:
        async with get_client() as client:
            # Get the youtube-flow deployment
            deployments = await client.read_deployments()
            youtube_deployment = None
            
            for deployment in deployments:
                if deployment.name == "youtube-flow":
                    youtube_deployment = deployment
                    break
            
            if not youtube_deployment:
                print("❌ youtube-flow deployment not found! Cannot create automation.")
                return False
            
            # ✅ FIXED: Add required 'posture' field for Prefect 3
            automation_data = {
                "name": "trigger-youtube-on-fixture-completion",
                "description": "Automatically trigger YouTube flow when a fixture completes",
                "enabled": True,
                "trigger": {
                    "type": "event",
                    "expect": ["fixture.completed"],
                    "match": {
                        "prefect.resource.id": "fixture.*"
                    },
                    "posture": "Reactive",  # ✅ CRITICAL: This was missing!
                    "threshold": 1,
                    "within": 0
                },
                "actions": [
                    {
                        "type": "run-deployment",
                        "deployment_id": str(youtube_deployment.id),
                        "parameters": {
                            "team1": "{{ event.payload.home_team }}",
                            "team2": "{{ event.payload.away_team }}",
                            "match_date": "{{ event.occurred.strftime('%Y-%m-%d') }}"
                        }
                    }
                ]
            }
            
            # ✅ Use the client's HTTP session directly
            response = await client._client.post("/automations/", json=automation_data)
            
            if response.status_code == 201:
                created_automation = response.json()
                print(f"✅ Created automation: {created_automation['name']} (ID: {created_automation['id']})")
                print("🎬 YouTube worker will now respond to fixture completion events!")
                return True
            else:
                print(f"❌ Failed to create automation via HTTP: {response.status_code} - {response.text}")
                return False
            
    except Exception as e:
        print(f"❌ Failed to create automation: {e}")
        import traceback
        traceback.print_exc()
        
        # ✅ FALLBACK: Try CLI approach with CORRECT flags
        print("🔄 Trying CLI automation creation as fallback...")
        return create_automation_cli()

def create_automation_cli():
    """Fallback: Create automation using CLI with CORRECT syntax"""
    try:
        # ✅ FIXED CLI command - use JSON file approach with posture field
        automation_config = {
            "name": "trigger-youtube-on-fixture-completion",
            "description": "Automatically trigger YouTube flow when a fixture completes",
            "enabled": True,
            "trigger": {
                "type": "event",
                "expect": ["fixture.completed"],
                "match": {
                    "prefect.resource.id": "fixture.*"
                },
                "posture": "Reactive",  # ✅ CRITICAL: Added missing posture field
                "threshold": 1,
                "within": 0
            },
            "actions": [
                {
                    "type": "run-deployment",
                    "source": "inferred",
                    "deployment": "youtube-flow"
                }
            ]
        }
        
        # Write config to temp file
        import json
        import tempfile
        
        with tempfile.NamedTemporaryFile(mode='w', suffix='.json', delete=False) as f:
            json.dump(automation_config, f, indent=2)
            temp_file = f.name
        
        # ✅ Use correct CLI syntax
        result = subprocess.run([
            "prefect", "automation", "create", temp_file
        ], capture_output=True, text=True, cwd="/app")
        
        # Clean up temp file
        import os
        os.unlink(temp_file)
        
        if result.returncode == 0:
            print("✅ CLI automation created successfully!")
            return True
        else:
            print(f"❌ CLI automation failed: {result.stderr}")
            print(f"❌ CLI stdout: {result.stdout}")
            return False
            
    except Exception as e:
        print(f"❌ CLI automation error: {e}")
        return False

def deploy_from_yaml():
    """Deploy using prefect.yaml project config - THE RIGHT WAY"""
    print("🚀 Creating deployments using prefect.yaml project config...")
    
    # Ensure pools exist first
    import asyncio
    asyncio.run(ensure_work_pools())
    
    # Clean existing deployments and automations
    asyncio.run(clean_all_deployments_api())
    asyncio.run(clean_all_automations())
    
    # Wait for cleanup
    print("⏳ Waiting 3 seconds for cleanup to complete...")
    import time
    time.sleep(3)
    
    # ✅ Use the correct command: prefect deploy --all
    print("🏗️ Deploying all deployments from prefect.yaml...")
    
    result = subprocess.run([
        "prefect", "deploy", "--all"
    ], capture_output=True, text=True, cwd="/app")
    
    if result.returncode == 0:
        print("✅ All deployments created successfully from prefect.yaml!")
        print(f"📋 Output: {result.stdout}")
        
        # ✅ CRITICAL: Create automation with FIXED API/CLI calls
        print("🤖 Creating YouTube automation with FIXED implementation...")
        automation_success = asyncio.run(create_youtube_automation())
        
        if automation_success:
            print("✅ Setup complete with automation!")
        else:
            print("⚠️ Deployments created but automation failed")
        
        return True
    else:
        print(f"❌ Failed to deploy from prefect.yaml:")
        print(f"   stdout: {result.stdout}")
        print(f"   stderr: {result.stderr}")
        return False

def run_immediate():
    """Run the fixtures flow immediately for today's date"""
    print("🏃 Running fixtures flow immediately for today...")
    print("🔍 About to call fixtures_flow()...")
    try:
        from found_footy.flows.fixtures_flow import fixtures_flow
        result = fixtures_flow()
        print(f"✅ Immediate run completed successfully: {result}")
    except Exception as e:
        print(f"❌ Immediate run failed: {e}")
        import traceback
        traceback.print_exc()

if __name__ == "__main__":
    print(f"🐛 DEBUG: Command line args: {sys.argv}")
    
    parser = argparse.ArgumentParser()
    parser.add_argument("--apply", action="store_true", help="Apply all deployments")
    parser.add_argument("--run-now", action="store_true", help="Also run immediately")
    parser.add_argument("--clean-only", action="store_true", help="Only clean deployments, don't recreate")
    args = parser.parse_args()
    
    print(f"🐛 DEBUG: Parsed args - apply: {args.apply}, run_now: {args.run_now}, clean_only: {args.clean_only}")
    
    if args.clean_only:
        print("🧹 CLEAN-ONLY MODE: Deleting all deployments...")
        import asyncio
        asyncio.run(clean_all_deployments_api())
        asyncio.run(clean_all_automations())
        print("✅ Clean-only completed!")
    elif args.apply:
        print("📋 Creating deployments from YAML configs...")
        # ✅ USE YAML DEPLOYMENT INSTEAD OF SERVE
        success = deploy_from_yaml()
        
        if success and args.run_now:
            print("🚨 DEBUG: run_now flag is True, calling run_immediate()")
            run_immediate()
        elif not success:
            print("❌ Deployment failed, skipping immediate run")
        else:
            print("⚠️ DEBUG: run_now flag is False, skipping immediate run")
        
        print("✅ Setup complete!")
        print("🌐 Access Prefect UI at http://localhost:4200")
        print("🎛️ Enable 'fixtures-flow-daily' schedule from the UI when ready")
    else:
        print("❌ DEBUG: apply flag is False")
        print("Use --apply to create deployments")
        print("Use --apply --run-now to also run immediately")
        print("Use --clean-only to just delete all deployments")