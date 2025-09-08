#!/usr/bin/env python3
"""
Startup Configuration Manager
FRESH REBUILD APPROACH - Delete and recreate everything on startup
"""

import asyncio
from found_footy.config.variable_manager import variable_manager

async def register_all_modules():
    """Register all variable modules for automatic management"""
    print("📋 Registering variable modules...")
    
    # 1. Team Variables Module
    from team_variables_manager import create_team_variables, update_team_variables
    variable_manager.register_module(
        module_name="team_variables",
        create_func=create_team_variables,
        update_func=update_team_variables,
        description="UEFA clubs and FIFA national teams with rankings"
    )
    
    # 2. Fixture Status Variables Module
    from found_footy.utils.fixture_status import create_fixture_status_variables, update_fixture_status_variables
    variable_manager.register_module(
        module_name="fixture_statuses",
        create_func=create_fixture_status_variables,
        update_func=update_fixture_status_variables,
        description="FIFA fixture status codes and lifecycle management"
    )
    
    print(f"✅ Registered {len(variable_manager.registered_modules)} variable modules")

async def sync_mongodb_metadata():
    """Sync team metadata to MongoDB after variables are updated"""
    print("\n🗄️ MONGODB SYNC")
    print("   Description: Populate team metadata from fresh Prefect Variables")
    
    try:
        from found_footy.api.mongo_api import populate_team_metadata_async
        await populate_team_metadata_async(reset_first=True, include_national_teams=True)
        print("   ✅ MongoDB team metadata synchronized")
    except Exception as e:
        print(f"   ❌ MongoDB sync failed: {e}")
        # Fallback
        try:
            from found_footy.api.mongo_api import populate_team_metadata
            populate_team_metadata(reset_first=True, include_national_teams=True)
            print("   ✅ MongoDB sync completed (fallback)")
        except Exception as e2:
            print(f"   ❌ MongoDB fallback also failed: {e2}")

async def full_startup_sync():
    """🔥 FRESH REBUILD: Complete startup with delete + recreate approach"""
    print("🔥 FOUND FOOTY - FRESH VARIABLE REBUILD")
    print("=" * 70)
    
    # Step 1: Register all modules
    await register_all_modules()
    
    # Step 2: 🔥 FRESH REBUILD - Delete all and recreate
    await variable_manager.fresh_rebuild_all_variables()
    
    # Step 3: Sync MongoDB with fresh data
    await sync_mongodb_metadata()
    
    # Step 4: Verification
    print("\n🔍 VERIFICATION")
    await variable_manager.list_all_variables()
    
    # Step 5: Verify Italy specifically
    print("\n🇮🇹 ITALY CHECK")
    try:
        from prefect import get_client
        async with get_client() as client:
            var = await client.read_variable_by_name('all_teams_2025_ids')
            team_ids = [int(x.strip()) for x in var.value.split(',')]
            if 768 in team_ids:
                print("   ✅ Italy (768) is NOW in fresh variables!")
            else:
                print("   ❌ Italy (768) STILL missing - check team_variables_manager.py")
    except Exception as e:
        print(f"   ❌ Italy check failed: {e}")
    
    print("=" * 70)
    print("✅ FRESH REBUILD COMPLETE")
    
    return True

# Convenience functions for CLI usage
def sync_startup():
    """Synchronous wrapper for fresh rebuild startup sync"""
    return asyncio.run(full_startup_sync())

def list_variables():
    """List all managed variables"""
    return asyncio.run(variable_manager.list_all_variables())

if __name__ == "__main__":
    import argparse
    
    parser = argparse.ArgumentParser(description="Fresh rebuild configuration management")
    parser.add_argument("--sync", action="store_true", help="Run fresh rebuild sync")
    parser.add_argument("--list", action="store_true", help="List all variables")
    
    args = parser.parse_args()
    
    if args.sync:
        sync_startup()
    elif args.list:
        list_variables()
    else:
        print("Usage: python startup_config.py [--sync|--list]")
        print("\nExample:")
        print("  python startup_config.py --sync")