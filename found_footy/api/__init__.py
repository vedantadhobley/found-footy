# ✅ CREATE: found_footy/api/__init__.py
"""API modules for Found Footy"""

from .mongo_api import (
    fixtures,
    fixtures_events,
    fixtures_batch,
    filter_fixtures_by_teams,
    populate_team_metadata,
    get_teams_from_variables,
    get_team_ids_from_variables
)

__all__ = [
    "fixtures",
    "fixtures_events", 
    "fixtures_batch",
    "filter_fixtures_by_teams",
    "populate_team_metadata",
    "get_teams_from_variables",
    "get_team_ids_from_variables"
]