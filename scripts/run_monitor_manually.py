#!/usr/bin/env python3
"""Manually run monitor cycle to test the pipeline"""
import sys
import os

# Add src to path
sys.path.insert(0, os.path.join(os.path.dirname(__file__), '..'))

from src.jobs.monitor.ops.activate_fixtures_op import activate_fixtures_op
from src.jobs.monitor.ops.batch_fetch_active_op import batch_fetch_active_op
from src.jobs.monitor.ops.process_events_op import process_and_debounce_events_op
from dagster import build_op_context


def run_monitor_cycle():
    """Run one complete monitor cycle"""
    print("=" * 60)
    print("MANUAL MONITOR CYCLE")
    print("=" * 60)
    
    # Create context for ops
    context = build_op_context()
    
    # Step 1: Activate fixtures
    print("\n📌 Step 1: Activating fixtures...")
    activate_result = activate_fixtures_op(context)
    print(f"   Activated: {activate_result.get('activated_count', 0)}")
    
    # Step 2: Fetch fresh data
    print("\n📌 Step 2: Fetching fresh fixture data...")
    fresh_fixtures = batch_fetch_active_op(context, activate_result)
    print(f"   Fetched: {len(fresh_fixtures)} fixtures")
    
    # Step 3: Process events and debounce
    print("\n📌 Step 3: Processing events and debouncing...")
    process_result = process_and_debounce_events_op(context, fresh_fixtures)
    print(f"   Debounced: {process_result.get('debounced_count', 0)}")
    print(f"   Completed: {process_result.get('completed_count', 0)}")
    
    print("\n" + "=" * 60)
    print("✅ MONITOR CYCLE COMPLETE")
    print("=" * 60)
    
    # Show collection counts
    from src.data.mongo_store import FootyMongoStore
    store = FootyMongoStore()
    
    print("\n📊 Collection counts:")
    print(f"   fixtures_staging: {store.fixtures_staging.count_documents({})}")
    print(f"   fixtures_active: {store.fixtures_active.count_documents({})}")
    print(f"   fixtures_live: {store.fixtures_live.count_documents({})}")
    print(f"   fixtures_completed: {store.fixtures_completed.count_documents({})}")


if __name__ == "__main__":
    run_monitor_cycle()
