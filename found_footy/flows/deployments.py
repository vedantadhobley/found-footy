import argparse
import sys
import subprocess
import asyncio
import time
import re
from pathlib import Path
from prefect import get_client
from datetime import datetime

async def ensure_work_pools():
    """Ensure work pools exist before creating deployments using CLI"""
    pools = [
        "ingest-pool", 
        "monitor-pool", 
        "advance-pool", 
        "goal-pool", 
        "twitter-pool"
    ]
    
    for pool_name in pools:
        try:
            result = subprocess.run(
                ["prefect", "work-pool", "inspect", pool_name], 
                capture_output=True, 
                text=True
            )
            
            if result.returncode == 0:
                print(f"✅ {pool_name} already exists")
            else:
                print(f"🔧 Creating {pool_name}...")
                create_result = subprocess.run(
                    ["prefect", "work-pool", "create", pool_name, "--type", "process"],
                    capture_output=True,
                    text=True
                )
                
                if create_result.returncode == 0:
                    print(f"✅ Created {pool_name}")
                else:
                    print(f"❌ Failed to create {pool_name}: {create_result.stderr}")
                    
        except Exception as e:
            print(f"❌ Error managing work pool {pool_name}: {e}")
            raise

async def clean_all_deployments_api():
    """Clean up ALL existing deployments using Prefect client API"""
    print("🧹 CLEANING ALL EXISTING DEPLOYMENTS (using API)...")
    
    try:
        async with get_client() as client:
            deployments = await client.read_deployments()
            
            if not deployments:
                print("ℹ️ No deployments found to delete")
                return
            
            print(f"🎯 Found {len(deployments)} deployments to delete:")
            for deployment in deployments:
                print(f"  - {deployment.name}")
            
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
            automations = await client.read_automations()
            
            if not automations:
                print("ℹ️ No automations found to delete")
                return
            
            print(f"🎯 Found {len(automations)} automations to delete:")
            for automation in automations:
                print(f"  - {automation.name}")
            
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

# Update deploy_from_yaml() to skip automation
def deploy_from_yaml():
    """Deploy using prefect.yaml - no automation needed"""
    print("🚀 Creating deployments using prefect.yaml...")
    
    # Remove automation creation entirely from sequence
    asyncio.run(ensure_work_pools())
    asyncio.run(clean_all_deployments_api())
    # ✅ REMOVED: No more automation creation
    
    print("⏳ Waiting 5 seconds for cleanup to complete...")
    time.sleep(5)
    
    # Deploy from YAML
    print("🏗️ Deploying from prefect.yaml...")
    
    result = subprocess.run([
        "prefect", "deploy", "--all"
    ], capture_output=True, text=True, cwd="/app")
    
    if result.returncode == 0:
        print("✅ All deployments created from prefect.yaml!")
        print("✅ Using direct Twitter flow triggering - no automation needed")
        return True
    else:
        print(f"❌ Failed to deploy from prefect.yaml:")
        print(f"   stdout: {result.stdout}")
        print(f"   stderr: {result.stderr}")
        return False

def run_immediate():
    """Run the fixtures flow immediately for today's date"""
    print("🏃 Running fixtures flow immediately for today...")
    try:
        from found_footy.flows.fixtures_flows import fixtures_ingest_flow
        result = fixtures_ingest_flow()
        print(f"✅ Immediate run completed successfully: {result}")
    except Exception as e:
        print(f"❌ Immediate run failed: {e}")
        import traceback
        traceback.print_exc()

if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--apply", action="store_true", help="Apply all deployments")
    parser.add_argument("--run-now", action="store_true", help="Also run immediately")
    parser.add_argument("--clean-only", action="store_true", help="Only clean deployments, don't recreate")
    args = parser.parse_args()
    
    if args.clean_only:
        print("🧹 CLEAN-ONLY MODE: Deleting all deployments...")
        asyncio.run(clean_all_deployments_api())
        asyncio.run(clean_all_automations())
        print("✅ Clean-only completed!")
    elif args.apply:
        print("📋 Creating deployments with FRESH REBUILD...")
        success = deploy_from_yaml()
        
        if success and args.run_now:
            run_immediate()
        elif not success:
            print("❌ Deployment failed, skipping immediate run")
        
        print("✅ Setup complete!")
        print("🌐 Access Prefect UI at http://localhost:4200")
        print("📝 All variables freshly rebuilt from source code")
    else:
        print("Use --apply to create deployments with fresh rebuild")
        print("Use --apply --run-now to also run immediately")
        print("Use --clean-only to just delete all deployments")