import argparse
import subprocess
import asyncio
import time
from prefect import get_client

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

def deploy_from_yaml():
    """Deploy using prefect.yaml - no automation needed"""
    print("🚀 Creating deployments using prefect.yaml...")
    
    asyncio.run(ensure_work_pools())
    asyncio.run(clean_all_deployments_api())
    
    print("⏳ Waiting 5 seconds for cleanup to complete...")
    time.sleep(5)
    
    # Deploy from YAML
    print("🏗️ Deploying from prefect.yaml...")
    
    result = subprocess.run([
        "prefect", "deploy", "--all"
    ], capture_output=True, text=True, cwd="/app")
    
    if result.returncode == 0:
        print("✅ All deployments created from prefect.yaml!")
        print("✅ Using direct flow triggering - no automation needed")
        return True
    else:
        print(f"❌ Failed to deploy from prefect.yaml:")
        print(f"   stdout: {result.stdout}")
        print(f"   stderr: {result.stderr}")
        return False

if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--apply", action="store_true", help="Apply all deployments")
    parser.add_argument("--clean-only", action="store_true", help="Only clean deployments, don't recreate")
    args = parser.parse_args()
    
    if args.clean_only:
        print("🧹 CLEAN-ONLY MODE: Deleting all deployments...")
        asyncio.run(clean_all_deployments_api())
        print("✅ Clean-only completed!")
    elif args.apply:
        print("📋 Creating deployments...")
        success = deploy_from_yaml()
        
        if success:
            print("✅ Setup complete!")
            print("🌐 Access Prefect UI at http://localhost:4200")
        else:
            print("❌ Deployment failed")
    else:
        print("Use --apply to create deployments")
        print("Use --clean-only to just delete all deployments")