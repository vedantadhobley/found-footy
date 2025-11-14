"""Advance fixtures asset - moves fixtures between collections"""

import logging
from typing import Optional

from dagster import asset, AssetExecutionContext, Output
from src.data.mongo_store import FootyMongoStore

logger = logging.getLogger(__name__)


@asset(
    name="advance_fixtures",
    description="Advance fixtures between collections (staging→active or active→completed)",
    group_name="fixtures",
    compute_kind="database"
)
def advance_fixtures_asset(
    context: AssetExecutionContext,
    source_collection: str,
    destination_collection: str,
    fixture_id: Optional[int] = None
) -> Output:
    """
    Advance fixtures between collections.
    
    Migrated from Prefect advance_flow.py
    
    - Move fixtures from staging to active (before kickoff)
    - Move fixtures from active to completed (after final whistle)
    """
    store = FootyMongoStore()
    
    if fixture_id:
        context.log.info(f"🎯 Processing specific fixture: {fixture_id}")
    else:
        context.log.info(f"📦 Advancing fixtures: {source_collection} → {destination_collection}")
    
    result = store.fixtures_advance(
        source_collection_name=source_collection,
        destination_collection_name=destination_collection,
        fixture_id=fixture_id
    )
    
    moved_count = result.get("moved_count", 0)
    
    if moved_count > 0:
        context.log.info(f"✅ Advanced {moved_count} fixture(s)")
    else:
        context.log.info("ℹ️ No fixtures to advance")
    
    return Output(
        value=result,
        metadata={
            "source": source_collection,
            "destination": destination_collection,
            "moved_count": moved_count
        }
    )
