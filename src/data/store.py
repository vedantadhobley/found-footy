"""
Base store class + composed FootyMongoStore + singleton accessor.

Phase 3 (P3c, 2026-05-26) split mongo_store.py into a `data/` package
with one file per logical collection-domain. This module hosts:

  - MongoStoreBase: connection setup, index creation, generic
    insert + static extractors. The mixin files (data/fixtures.py,
    data/events.py, data/videos.py, data/aliases.py, data/cache.py)
    add methods that operate on the collection attributes set by
    MongoStoreBase.__init__.

  - FootyMongoStore: the public class — composition of all mixins
    plus the base. Inheritance order matters; MRO is
    {Fixtures, Events, Videos, Aliases, Cache, Base} so the base's
    __init__ is what runs.

  - get_store(): process-wide singleton (P0 Sprint 4 work; one
    pool per worker process instead of per activity call).

Back-compat: src/data/mongo_store.py is a deprecation shim that
re-exports FootyMongoStore + get_store from here. The 29 caller sites
using `from src.data.mongo_store import get_store` continue to work
unchanged.
"""

import os
from datetime import datetime, timezone
from typing import Any, Dict, List, Optional

from pymongo import ASCENDING, MongoClient, ReturnDocument
from pymongo.errors import DuplicateKeyError

from src.data.models import (
    FixtureFields,
    EventFields,
    FixtureStatus,
    TeamAliasFields,
    create_activation_fields,
)
from src.data._helpers import _log_info, _log_warning, _log_error
from src.utils.footy_logging import log, get_fallback_logger
from src.utils.orchestration_config import MONITOR_DROP_THRESHOLD, TWITTER_REQUIRED_DOWNLOADS

from src.utils.config import MONGODB_URI

# Mixin imports — must come AFTER COMMON_IMPORTS so mixin modules can
# import _helpers without triggering this file's import first.
from src.data.fixtures import FixturesMixin
from src.data.events import EventsMixin
from src.data.videos import VideosMixin
from src.data.aliases import AliasesMixin
from src.data.cache import CacheMixin


class MongoStoreBase:
    """
    MongoDB storage for the 5-collection architecture:
      - fixtures_staging: Waiting to activate (TBD, NS)
      - fixtures_live: Raw API data (temporary, for comparison)
      - fixtures_active: Enhanced with debounce data (never overwritten)
      - fixtures_completed: Archive (FT, AET, PEN)
      - team_aliases: Cached team name aliases for Twitter search (RAG)
      - top_flight_cache: Dynamic top-flight team ID cache (the 6th)

    Mixin classes in this package add domain-specific methods that
    operate on the collection attributes set in __init__.
    """

    def __init__(self, connection_url=None):
        # Use centralized config (can be overridden via parameter or env)
        if connection_url is None:
            connection_url = os.getenv('MONGODB_URI') or os.getenv('MONGODB_URL') or MONGODB_URI
        
        self.client = MongoClient(connection_url)
        self.db = self.client.found_footy
        
        # 5 Collections
        self.fixtures_staging = self.db.fixtures_staging
        self.fixtures_live = self.db.fixtures_live
        self.fixtures_active = self.db.fixtures_active
        self.fixtures_completed = self.db.fixtures_completed
        self.team_aliases = self.db.team_aliases
        
        self._create_indexes()

    def _create_indexes(self):
        """Create indexes for all collections (only runs once per session)"""
        # Skip if already created this session
        if getattr(FootyMongoStore, '_indexes_created', False):
            return
        
        try:
            # Status indexes
            self.fixtures_staging.create_index([("fixture.status.short", ASCENDING)])
            self.fixtures_live.create_index([("fixture.status.short", ASCENDING)])
            self.fixtures_active.create_index([("fixture.status.short", ASCENDING)])
            self.fixtures_completed.create_index([("fixture.status.short", ASCENDING)])
            
            # Date indexes
            self.fixtures_staging.create_index([("fixture.date", ASCENDING)])
            self.fixtures_live.create_index([("fixture.date", ASCENDING)])
            self.fixtures_active.create_index([("fixture.date", ASCENDING)])
            self.fixtures_completed.create_index([("fixture.date", ASCENDING)])

            # Team indexes
            for collection in [self.fixtures_staging, self.fixtures_live, self.fixtures_active, self.fixtures_completed]:
                collection.create_index([("teams.home.id", ASCENDING)])
                collection.create_index([("teams.away.id", ASCENDING)])
            
            # Enhanced event field indexes (fixtures_active only)
            self.fixtures_active.create_index([("events._event_id", ASCENDING)])
            self.fixtures_active.create_index([("events._monitor_complete", ASCENDING)])
            
            # Team aliases indexes (for lookup by team name)
            self.team_aliases.create_index([(TeamAliasFields.TEAM_NAME, ASCENDING)])
            
            FootyMongoStore._indexes_created = True
        except Exception as e:
            _log_warning("index_error", "MongoDB index error", error=str(e))

    @staticmethod
    def _extract_fixture_id(raw_item: dict) -> int:
        """Extract fixture.id from raw API schema"""
        try:
            return int(raw_item["fixture"]["id"])
        except Exception:
            return 0

    @staticmethod
    def _extract_status(raw_item: dict) -> str:
        """Extract status from raw API schema"""
        try:
            return raw_item["fixture"]["status"]["short"]
        except Exception:
            return "NS"

    @staticmethod
    def _extract_current_goals(raw_item: dict) -> dict:
        """Extract goals from raw API schema with null handling"""
        try:
            goals = raw_item.get("goals")
            if goals is None:
                return {"home": 0, "away": 0}
            
            home = goals.get("home")
            away = goals.get("away")
            
            return {
                "home": home if home is not None else 0,
                "away": away if away is not None else 0
            }
        except Exception:
            return {"home": 0, "away": 0}

    def bulk_insert_fixtures(self, raw_fixtures: List[dict], collection_name: str) -> int:
        """
        Insert fixtures into specified collection (staging, active, or completed).
        Convenience method for ingestion workflow.
        """
        if not raw_fixtures:
            return 0
        
        # Map collection names to actual collections
        collection_map = {
            "fixtures_staging": self.fixtures_staging,
            "fixtures_active": self.fixtures_active,
            "fixtures_completed": self.fixtures_completed,
        }
        
        collection = collection_map.get(collection_name)
        if collection is None:
            raise ValueError(f"Unknown collection: {collection_name}")
        
        try:
            from datetime import datetime, timezone
            now = datetime.now(timezone.utc)
            
            docs = []
            for fixture in raw_fixtures:
                fixture_id = self._extract_fixture_id(fixture)
                if fixture_id == 0:
                    continue
                
                doc = dict(fixture)
                doc["_id"] = fixture_id
                
                # For staging fixtures, set _last_monitor to ingestion time
                # This prevents immediate re-polling in the same interval
                if collection_name == "fixtures_staging":
                    doc["_last_monitor"] = now
                
                docs.append(doc)
            
            if docs:
                result = collection.insert_many(docs, ordered=False)
                return len(result.inserted_ids)
            return 0
        except Exception as e:
            _log_error("insert_error", "Error inserting to collection", collection=collection_name, error=str(e), exc=e)
            return 0


class FootyMongoStore(
    FixturesMixin,
    EventsMixin,
    VideosMixin,
    AliasesMixin,
    CacheMixin,
    MongoStoreBase,
):
    """The composed store. All public methods come from the mixins."""
    pass


# ─── Process-wide singleton (Sprint 4 / P0) ─────────────────────────────────

_store_singleton: Optional[FootyMongoStore] = None


def get_store() -> FootyMongoStore:
    """Return the process-wide FootyMongoStore singleton (lazy-init).

    Replaces the pattern of instantiating a new store per activity call —
    PyMongo client connections + index creation are expensive and meant
    to be reused.
    """
    global _store_singleton
    if _store_singleton is None:
        _store_singleton = FootyMongoStore()
    return _store_singleton
