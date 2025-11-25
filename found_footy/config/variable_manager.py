#!/usr/bin/env python3
"""
Centralized Prefect Variable Management System
FRESH REBUILD APPROACH - Delete all variables and recreate every startup
"""

import asyncio
import json
from pathlib import Path
from typing import Dict, Any, List
from prefect import get_client
from prefect.client.schemas.objects import Variable

class VariableManager:
    """Variable management with FRESH REBUILD approach"""
    
    def __init__(self):
        self.registered_modules = []
        self.sync_stats = {"deleted": 0, "created": 0, "errors": 0}
    
    def register_module(self, module_name: str, create_func, update_func, description: str):
        """Register a variable module for automatic management"""
        self.registered_modules.append({
            "name": module_name,
            "create_func": create_func,
            "update_func": update_func,
            "description": description
        })
    
    async def fresh_rebuild_all_variables(self):
        """🔥 FRESH APPROACH: Delete everything and rebuild from scratch"""
        print("🔥 FRESH VARIABLE REBUILD - Delete All + Recreate")
        print("=" * 60)
        
        self.sync_stats = {"deleted": 0, "created": 0, "errors": 0}
        
        # Step 1: Delete ALL existing variables
        await self._delete_all_variables()
        
        # Step 2: Create fresh variables from each module
        for module in self.registered_modules:
            await self._create_fresh_module(module)
        
        print("=" * 60)
        print(f"🔥 FRESH REBUILD COMPLETE: {self.sync_stats['deleted']} deleted, {self.sync_stats['created']} created, {self.sync_stats['errors']} errors")
        
        return self.sync_stats
    
    async def _delete_all_variables(self):
        """Delete ALL variables - clean slate"""
        print("\n🗑️ DELETING ALL EXISTING VARIABLES")
        
        async with get_client() as client:
            try:
                # Get all variables
                all_variables = await client.read_variables()
                
                if not all_variables:
                    print("   ℹ️ No existing variables to delete")
                    return
                
                print(f"   🎯 Found {len(all_variables)} variables to delete")
                
                # Delete each variable
                deleted_count = 0
                for var in all_variables:
                    try:
                        await client.delete_variable_by_name(var.name)
                        print(f"   ✅ Deleted: {var.name}")
                        deleted_count += 1
                    except Exception as e:
                        print(f"   ⚠️ Could not delete {var.name}: {e}")
                
                self.sync_stats["deleted"] = deleted_count
                print(f"   🔥 Deleted {deleted_count} variables")
                
            except Exception as e:
                print(f"   ❌ Error deleting variables: {e}")
                self.sync_stats["errors"] += 1
    
    async def _create_fresh_module(self, module: Dict[str, Any]):
        """Create fresh variables from a module"""
        module_name = module["name"]
        print(f"\n📋 CREATING FRESH: {module_name.upper()}")
        print(f"   Description: {module['description']}")
        
        try:
            print(f"   🆕 Creating fresh {module_name}...")
            await module["create_func"]()
            print(f"   ✅ Fresh creation successful: {module_name}")
            self.sync_stats["created"] += 1
            
        except Exception as e:
            print(f"   ❌ Error creating fresh {module_name}: {e}")
            self.sync_stats["errors"] += 1

    async def list_all_variables(self):
        """List all managed variables with their details"""
        print("📊 FRESH VARIABLES OVERVIEW")
        print("=" * 60)
        
        async with get_client() as client:
            try:
                variables = await client.read_variables()
                
                print(f"Total variables: {len(variables)}")
                print()
                
                for var in variables:
                    print(f"🔗 {var.name}")
                    print(f"   Tags: {', '.join(var.tags)}")
                    
                    # Show data summary
                    try:
                        if "ids" in var.name:
                            ids_count = len(var.value.split(",")) if var.value else 0
                            print(f"   Data: {ids_count} IDs")
                        else:
                            data = json.loads(var.value)
                            if isinstance(data, dict):
                                print(f"   Data: {len(data)} items")
                            elif isinstance(data, list):
                                print(f"   Data: {len(data)} entries")
                            else:
                                print(f"   Data: {type(data).__name__}")
                    except:
                        print(f"   Data: {len(var.value)} chars")
                    print()
                        
            except Exception as e:
                print(f"❌ Error listing variables: {e}")

# Create singleton instance
variable_manager = VariableManager()