"""team_aliases collection methods (RAG cache for Twitter team-name aliases)."""

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


class AliasesMixin:
    """Mixin: team_aliases CRUD (RAG alias cache)."""

    def get_team_alias(self, team_id: int) -> dict | None:
        """
        Get cached team alias by team_id.
        
        Args:
            team_id: API-Football team ID
            
        Returns:
            TeamAlias document or None if not cached
        """
        try:
            return self.team_aliases.find_one({"_id": team_id})
        except Exception as e:
            _log_error("get_team_alias_error", f"Error getting team alias for {team_id}", error=str(e), team_id=team_id, exc=e)
            return None

    def upsert_team_alias(
        self,
        team_id: int,
        team_name: str,
        national: bool,
        twitter_aliases: List[str],
        model: str,
        country: str | None = None,
        city: str | None = None,
        wikidata_qid: str | None = None,
        wikidata_aliases: List[str] | None = None,
    ) -> bool:
        """
        Create or update team alias in cache.
        
        Args:
            team_id: API-Football team ID (used as _id)
            team_name: Full team name
            national: True if national team, False if club
            twitter_aliases: Final aliases for Twitter search
            model: LLM model used (or "fallback")
            country: Country from API-Football (e.g., "England")
            city: City from venue data (e.g., "Newcastle upon Tyne")
            wikidata_qid: Wikidata QID if found
            wikidata_aliases: Raw aliases from Wikidata
            
        Returns:
            True if successful
        """
        try:
            team_type = "national" if national else "club"  # Derive team_type from national
            
            update_data = {
                TeamAliasFields.TEAM_NAME: team_name,
                TeamAliasFields.TEAM_TYPE: team_type,
                TeamAliasFields.NATIONAL: national,
                TeamAliasFields.TWITTER_ALIASES: twitter_aliases,
                TeamAliasFields.MODEL: model,
                TeamAliasFields.UPDATED_AT: datetime.now(timezone.utc),
            }
            
            # Add optional fields from API
            if country:
                update_data[TeamAliasFields.COUNTRY] = country
            if city:
                update_data[TeamAliasFields.CITY] = city
            if wikidata_qid:
                update_data[TeamAliasFields.WIKIDATA_QID] = wikidata_qid
            if wikidata_aliases:
                update_data[TeamAliasFields.WIKIDATA_ALIASES] = wikidata_aliases
            
            self.team_aliases.update_one(
                {"_id": team_id},
                {
                    "$set": update_data,
                    "$setOnInsert": {TeamAliasFields.CREATED_AT: datetime.now(timezone.utc)},
                },
                upsert=True,
            )
            return True
        except Exception as e:
            _log_error("upsert_team_alias_error", f"Error upserting team alias for {team_id}", error=str(e), team_id=team_id, exc=e)
            return False

    def get_all_team_aliases(self) -> List[dict]:
        """Get all cached team aliases."""
        try:
            return list(self.team_aliases.find({}))
        except Exception as e:
            _log_error("get_all_team_aliases_error", "Error getting all team aliases", error=str(e), exc=e)
            return []

    def delete_team_alias(self, team_id: int) -> bool:
        """Delete a team alias from cache (for testing/refresh)."""
        try:
            result = self.team_aliases.delete_one({"_id": team_id})
            return result.deleted_count > 0
        except Exception as e:
            _log_error("delete_team_alias_error", f"Error deleting team alias for {team_id}", error=str(e), team_id=team_id, exc=e)
            return False

    def clear_team_aliases(self) -> int:
        """Clear all team aliases (for testing/refresh). Returns count deleted."""
        try:
            result = self.team_aliases.delete_many({})
            return result.deleted_count
        except Exception as e:
            _log_error("clear_team_aliases_error", "Error clearing team aliases", error=str(e), exc=e)
            return 0
