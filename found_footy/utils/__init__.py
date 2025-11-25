"""Utilities module for Found Footy"""

# Remove the circular import - don't import from api here
# Instead, let the main __init__.py handle all exports

from .fixture_status import (
    get_fixture_statuses,
    is_fixture_completed,
    is_fixture_active,
    is_fixture_staging
)

from .team_data import (
    get_team_data,
    get_team_ids,
    get_all_teams,
    get_team_by_id,
    is_team_tracked
)

__all__ = [
    # Fixture status utilities
    "get_fixture_statuses",
    "is_fixture_completed", 
    "is_fixture_active",
    "is_fixture_staging",
    # Team data utilities
    "get_team_data",
    "get_team_ids",
    "get_all_teams", 
    "get_team_by_id",
    "is_team_tracked"
]