"""top_flight_cache collection methods (dynamic top-flight team ID cache)."""

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


class CacheMixin:
    """Mixin: top_flight_cache CRUD (the 6th formal collection)."""

    def get_top_flight_cache(self) -> dict | None:
        """
        Get cached top-flight team IDs.
        
        Returns:
            Dict with 'team_ids', 'season', 'cached_at' or None if not cached
        """
        try:
            doc = self.db.top_flight_cache.find_one({"_id": "top_5_leagues"})
            return doc
        except Exception as e:
            _log_error("get_top_flight_cache_error", "Error getting top-flight cache", error=str(e), exc=e)
            return None

    def save_top_flight_cache(self, team_ids: list[int], season: int) -> bool:
        """
        Save top-flight team IDs to cache.
        
        Args:
            team_ids: List of team IDs from all top 5 leagues
            season: Season year (e.g., 2025)
        """
        try:
            self.db.top_flight_cache.update_one(
                {"_id": "top_5_leagues"},
                {
                    "$set": {
                        "team_ids": team_ids,
                        "season": season,
                        "cached_at": datetime.now(timezone.utc),
                        "count": len(team_ids),
                    }
                },
                upsert=True,
            )
            return True
        except Exception as e:
            _log_error("save_top_flight_cache_error", "Error saving top-flight cache", error=str(e), exc=e)
            return False

    def clear_top_flight_cache(self) -> bool:
        """Clear the top-flight teams cache (forces refresh on next call)."""
        try:
            self.db.top_flight_cache.delete_one({"_id": "top_5_leagues"})
            return True
        except Exception as e:
            _log_error("clear_top_flight_cache_error", "Error clearing top-flight cache", error=str(e), exc=e)
            return False
