"""Advance fixtures job - moves fixtures between collections

Migrated from found_footy/flows/advance_flow.py
"""

import logging
from typing import Optional

from dagster import job, op, OpExecutionContext, Config
from src.data.mongo_store import FootyMongoStore

logger = logging.getLogger(__name__)


class AdvanceFixturesConfig(Config):
    """Configuration for advancing fixtures"""
    source_collection: str
    destination_collection: str
    fixture_id: Optional[int] = None


@op(
    name="advance_fixtures_op",
    description="Move fixtures between collections (staging→active or active→completed)"
)
def advance_fixtures_op(
    context: OpExecutionContext,
    config: AdvanceFixturesConfig
) -> dict:
    """
    Advance fixtures between collections:
    - staging → active (before kickoff)
    - active → completed (after match ends)
    """
    store = FootyMongoStore()
    
    if config.fixture_id:
        context.log.info(f"🎯 Processing specific fixture: {config.fixture_id}")
    else:
        context.log.info(f"📦 Advancing fixtures: {config.source_collection} → {config.destination_collection}")
    
    result = store.fixtures_advance(
        source_collection_name=config.source_collection,
        destination_collection_name=config.destination_collection,
        fixture_id=config.fixture_id
    )
    
    moved_count = result.get("moved_count", 0)
    
    if moved_count > 0:
        if config.destination_collection == "fixtures_active":
            context.log.info(f"🚀 KICKOFF: {moved_count} match(es) now live")
        elif config.destination_collection == "fixtures_completed":
            context.log.info(f"🏁 COMPLETED: {moved_count} match(es) archived")
        else:
            context.log.info(f"✅ Advanced {moved_count} fixture(s)")
    else:
        context.log.info("ℹ️ No fixtures to advance")
    
    return result


@job(
    name="advance_fixtures",
    description="Advance fixtures between collections"
)
def advance_fixtures_job():
    """Fixture advancement workflow"""
    advance_fixtures_op()
