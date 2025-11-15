"""Advance fixtures job - moves fixtures between collections

Migrated from found_footy/flows/advance_flow.py

Purpose: Move fixtures from staging → active when they're about to start
Schedule: Runs after daily ingest (chained via run_status_sensor)
"""

from typing import Dict, Any, Optional
from dagster import job, op, OpExecutionContext, Config
from src.data.mongo_store import FootyMongoStore
from src.utils import fixture_logic


class AdvanceConfig(Config):
    """Configuration for advancing fixtures"""
    source_collection: str = "fixtures_staging"
    destination_collection: str = "fixtures_active"
    fixture_id: Optional[int] = None


@op(
    name="advance_fixtures_op",
    description="Check fixtures_staging and advance fixtures that are ready (kickoff - 3min)"
)
def advance_fixtures_op(context: OpExecutionContext, config: AdvanceConfig) -> Dict[str, Any]:
    """
    Advance fixtures between collections based on timing.
    
    For staging → active: Only advance fixtures where now >= kickoff_time - 3min
    For manual triggers: Advance specific fixture_id immediately
    """
    from datetime import datetime, timezone, timedelta
    
    store = FootyMongoStore()
    
    context.log.info(f"📋 Checking advances: {config.source_collection} → {config.destination_collection}")
    
    # Manual trigger - advance specific fixture immediately
    if config.fixture_id:
        context.log.info(f"🎯 Manual advance for fixture: {config.fixture_id}")
        result = fixture_logic.advance_fixtures(
            source_collection=config.source_collection,
            destination_collection=config.destination_collection,
            fixture_id=config.fixture_id,
            store=store
        )
        if result["status"] == "success" and result.get("advanced_count", 0) > 0:
            context.log.info(f"✅ Manually advanced fixture {config.fixture_id}")
        return result
    
    # Scheduled trigger - check timing for staging → active
    if config.source_collection == "fixtures_staging" and config.destination_collection == "fixtures_active":
        context.log.info("⏰ Checking for fixtures ready to advance (at kickoff)")
        
        now = datetime.now(timezone.utc)
        
        # Find fixtures where now >= kickoff time
        source_collection = getattr(store, config.source_collection)
        ready_fixtures = []
        
        for fixture_doc in source_collection.find({}):
            try:
                # Extract kickoff time from fixture
                kickoff_str = fixture_doc.get("fixture", {}).get("date")
                if not kickoff_str:
                    continue
                
                kickoff_time = datetime.fromisoformat(kickoff_str)
                
                # Advance if now >= kickoff (right at kickoff)
                if now >= kickoff_time:
                    fixture_id = fixture_doc["_id"]
                    ready_fixtures.append(fixture_id)
                    context.log.info(f"✅ Ready to advance: fixture {fixture_id}, kickoff at {kickoff_time}")
            
            except Exception as e:
                context.log.error(f"❌ Error checking fixture timing: {e}")
                continue
        
        if not ready_fixtures:
            context.log.info("⏸️ No fixtures ready to advance yet")
            return {"status": "success", "advanced_count": 0}
        
        # Advance ready fixtures
        advanced_count = 0
        for fixture_id in ready_fixtures:
            result = fixture_logic.advance_fixtures(
                source_collection=config.source_collection,
                destination_collection=config.destination_collection,
                fixture_id=fixture_id,
                store=store
            )
            if result["status"] == "success":
                advanced_count += result.get("advanced_count", 0)
        
        context.log.info(f"🚀 KICKOFF: {advanced_count} matches moved to active collection")
        return {"status": "success", "advanced_count": advanced_count}
    
    # Default: advance all (for other collection pairs)
    result = fixture_logic.advance_fixtures(
        source_collection=config.source_collection,
        destination_collection=config.destination_collection,
        fixture_id=config.fixture_id,
        store=store
    )
    
    if result["status"] == "success":
        advanced_count = result.get("advanced_count", 0)
        if advanced_count > 0:
            context.log.info(f"🔄 ADVANCED: {advanced_count} matches moved")
        else:
            context.log.info("⏸️ No fixtures to advance")
    else:
        context.log.error(f"❌ Advancement failed: {result.get('error', 'Unknown error')}")
    
    return result


@job(
    name="advance_fixtures",
    description="Move fixtures from staging to active (or active to completed)"
)
def advance_fixtures_job():
    """
    Advance fixtures workflow.
    
    Default flow:
    - staging → active: Move imminent fixtures for monitoring
    - active → completed: Archive finished matches
    
    Can be triggered manually with custom parameters.
    """
    advance_fixtures_op()
