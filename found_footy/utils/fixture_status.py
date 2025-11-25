"""Fixture status management using Prefect Variables"""
import asyncio
import json
from prefect import get_client
from prefect.client.schemas.objects import Variable

# ✅ FIFA STATUS DEFINITIONS - Centralized and documented
FIXTURE_STATUSES = {
    "completed": {
        "FT": "Match Finished (regular time)",
        "AET": "Match Finished (after extra time)",
        "PEN": "Match Finished (after penalty shootout)",
        "PST": "Match Postponed (rescheduled to different day)",
        "CANC": "Match Cancelled (will not be played)",
        "ABD": "Match Abandoned (may not be rescheduled)",
        "AWD": "Technical Loss (awarded result)",
        "WO": "WalkOver (forfeit)"
    },
    "active": {
        "1H": "First Half in progress",
        "HT": "Halftime (will resume)",
        "2H": "Second Half in progress", 
        "ET": "Extra Time in progress",
        "BT": "Break Time (between periods)",
        "P": "Penalty Shootout (wait for completion)",
        "SUSP": "Match Suspended (may resume)",
        "INT": "Match Interrupted (should resume)",
        "LIVE": "Generic live status"
    },
    "staging": {
        "TBD": "Time To Be Defined (pre-match)",
        "NS": "Not Started (pre-match)"
    }
}

async def create_fixture_status_variables():
    """Create Prefect Variables for fixture status management"""
    print("🚀 Creating fixture status variables...")
    
    async with get_client() as client:
        try:
            # Main status configuration
            status_variable = Variable(
                name="fixture_statuses",
                value=json.dumps(FIXTURE_STATUSES, indent=2),
                tags=["fixtures", "statuses", "configuration"]
            )
            
            await client.create_variable(status_variable)
            print("✅ Created variable: fixture_statuses")
            
            # Create individual lists for easy access
            for category, statuses in FIXTURE_STATUSES.items():
                list_variable = Variable(
                    name=f"fixture_statuses_{category}",
                    value=json.dumps(list(statuses.keys())),
                    tags=["fixtures", "statuses", category]
                )
                
                await client.create_variable(list_variable)
                print(f"✅ Created variable: fixture_statuses_{category}")
            
            print(f"🎯 Status Summary:")
            print(f"📊 Completed: {len(FIXTURE_STATUSES['completed'])} statuses")
            print(f"🔄 Active: {len(FIXTURE_STATUSES['active'])} statuses")
            print(f"📅 Staging: {len(FIXTURE_STATUSES['staging'])} statuses")
            
        except Exception as e:
            if "already exists" in str(e):
                print("⚠️ Status variables already exist - updating...")
                await update_fixture_status_variables()
            else:
                raise e

async def update_fixture_status_variables():
    """Update existing fixture status variables"""
    print("🔄 Updating fixture status variables...")
    
    async with get_client() as client:
        try:
            # Update main configuration
            await client.set_variable(
                name="fixture_statuses", 
                value=json.dumps(FIXTURE_STATUSES, indent=2)
            )
            
            # Update individual lists
            for category, statuses in FIXTURE_STATUSES.items():
                await client.set_variable(
                    name=f"fixture_statuses_{category}",
                    value=json.dumps(list(statuses.keys()))
                )
            
            print("✅ All fixture status variables updated")
            
        except Exception as e:
            print(f"❌ Error updating status variables: {e}")

def get_fixture_statuses():
    """Synchronous helper to get fixture statuses"""
    try:
        return asyncio.run(get_fixture_statuses_async())
    except RuntimeError:
        import nest_asyncio
        nest_asyncio.apply()
        return asyncio.run(get_fixture_statuses_async())

async def get_fixture_statuses_async():
    """Get fixture status configuration from Prefect Variables"""
    try:
        async with get_client() as client:
            var = await client.read_variable_by_name("fixture_statuses")
            config = json.loads(var.value)
            
            # Return lists of status codes for easy use
            return {
                "completed": list(config["completed"].keys()),
                "active": list(config["active"].keys()),
                "staging": list(config["staging"].keys()),
                "all_descriptions": config
            }
            
    except Exception as e:
        print(f"⚠️ Could not load fixture statuses from variables: {e}")
        # Fallback to hardcoded values
        return {
            "completed": list(FIXTURE_STATUSES["completed"].keys()),
            "active": list(FIXTURE_STATUSES["active"].keys()),
            "staging": list(FIXTURE_STATUSES["staging"].keys()),
            "all_descriptions": FIXTURE_STATUSES
        }

# ✅ CONVENIENCE FUNCTIONS
def get_completed_statuses():
    """Get list of completed status codes"""
    return get_fixture_statuses()["completed"]

def get_active_statuses():
    """Get list of active status codes"""
    return get_fixture_statuses()["active"]

def get_staging_statuses():
    """Get list of staging status codes"""
    return get_fixture_statuses()["staging"]

def is_fixture_completed(status_code):
    """Check if a status code indicates completion"""
    return status_code in get_completed_statuses()

def is_fixture_active(status_code):
    """Check if a status code indicates active monitoring"""
    return status_code in get_active_statuses()

def is_fixture_staging(status_code):
    """Check if a status code indicates staging"""
    return status_code in get_staging_statuses()

if __name__ == "__main__":
    import argparse
    
    parser = argparse.ArgumentParser(description="Manage fixture status variables")
    parser.add_argument("--create", action="store_true", help="Create status variables")
    parser.add_argument("--update", action="store_true", help="Update existing variables")
    args = parser.parse_args()
    
    if args.create:
        asyncio.run(create_fixture_status_variables())
    elif args.update:
        asyncio.run(update_fixture_status_variables())
    else:
        print("Usage: python fixture_status.py [--create|--update]")