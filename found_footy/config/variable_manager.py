#!/usr/bin/env python3
"""
Centralized Prefect Variable Management System
FRESH REBUILD APPROACH - Delete all variables and recreate every startup
"""

import asyncio  # ✅ ADD: Missing import
import json    # ✅ ADD: Missing import
from pathlib import Path
from typing import Dict, Any, List
from prefect import get_client
from prefect.client.schemas.objects import Variable

# ✅ FIXED: Use proper logging
from found_footy.utils.logging import get_logger, log_error_with_trace

class VariableManager:
    """Variable management with FRESH REBUILD approach"""
    
    def __init__(self):
        self.logger = get_logger(self.__class__.__name__)
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
        self.logger.info("🔥 FRESH VARIABLE REBUILD - Delete All + Recreate")
        self.logger.info("=" * 60)
        
        self.sync_stats = {"deleted": 0, "created": 0, "errors": 0}
        
        # Step 1: Delete ALL existing variables
        await self._delete_all_variables()
        
        # Step 2: Create fresh variables from each module
        for module in self.registered_modules:
            await self._create_fresh_module(module)
        
        self.logger.info("=" * 60)
        self.logger.info(f"🔥 FRESH REBUILD COMPLETE: {self.sync_stats['deleted']} deleted, {self.sync_stats['created']} created, {self.sync_stats['errors']} errors")
        
        return self.sync_stats
    
    async def _delete_all_variables(self):
        """Delete ALL variables - clean slate"""
        self.logger.info("🗑️ DELETING ALL EXISTING VARIABLES")
        
        async with get_client() as client:
            try:
                # Get all variables
                all_variables = await client.read_variables()
                
                if not all_variables:
                    self.logger.info("ℹ️ No existing variables to delete")
                    return
                
                self.logger.info(f"🎯 Found {len(all_variables)} variables to delete")
                
                # Delete each variable
                deleted_count = 0
                for var in all_variables:
                    try:
                        await client.delete_variable_by_name(var.name)
                        self.logger.debug(f"✅ Deleted: {var.name}")
                        deleted_count += 1
                    except Exception as e:
                        self.logger.warning(f"⚠️ Could not delete {var.name}: {e}")
                
                self.sync_stats["deleted"] = deleted_count
                self.logger.info(f"🔥 Deleted {deleted_count} variables")
                
            except Exception as e:
                log_error_with_trace(self.logger, "❌ Error deleting variables", e)
                self.sync_stats["errors"] += 1
    
    async def _create_fresh_module(self, module: Dict[str, Any]):
        """Create fresh variables from a module"""
        module_name = module["name"]
        self.logger.info(f"📋 CREATING FRESH: {module_name.upper()}")
        self.logger.info(f"Description: {module['description']}")
        
        try:
            self.logger.info(f"🆕 Creating fresh {module_name}...")
            await module["create_func"]()
            self.logger.info(f"✅ Fresh creation successful: {module_name}")
            self.sync_stats["created"] += 1
            
        except Exception as e:
            log_error_with_trace(self.logger, f"❌ Error creating fresh {module_name}", e)
            self.sync_stats["errors"] += 1

    async def list_all_variables(self):
        """List all managed variables with their details"""
        self.logger.info("📊 FRESH VARIABLES OVERVIEW")
        self.logger.info("=" * 60)
        
        async with get_client() as client:
            try:
                variables = await client.read_variables()
                
                self.logger.info(f"Total variables: {len(variables)}")
                
                for var in variables:
                    self.logger.info(f"🔗 {var.name}")
                    self.logger.info(f"   Tags: {', '.join(var.tags)}")
                    
                    # Show data summary
                    try:
                        if "ids" in var.name:
                            ids_count = len(var.value.split(",")) if var.value else 0
                            self.logger.info(f"   Data: {ids_count} IDs")
                        else:
                            data = json.loads(var.value)
                            if isinstance(data, dict):
                                self.logger.info(f"   Data: {len(data)} items")
                            elif isinstance(data, list):
                                self.logger.info(f"   Data: {len(data)} entries")
                            else:
                                self.logger.info(f"   Data: {type(data).__name__}")
                    except:
                        self.logger.info(f"   Data: {len(var.value)} chars")
                        
            except Exception as e:
                log_error_with_trace(self.logger, "❌ Error listing variables", e)

# Create singleton instance
variable_manager = VariableManager()