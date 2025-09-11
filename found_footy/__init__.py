"""Found Footy - Enterprise Football Data Pipeline"""

from found_footy.api.mongo_api import (
    fixtures,
    fixtures_events,
    fixtures_batch,
    filter_fixtures_by_teams
)

from found_footy.flows.ingest_flow import ingest_flow
from found_footy.flows.monitor_flow import monitor_flow
from found_footy.flows.advance_flow import advance_flow
from found_footy.flows.goal_flow import goal_flow
from found_footy.flows.twitter_flow import twitter_flow

from found_footy.storage.mongo_store import FootyMongoStore

from found_footy.utils.fixture_status import (
    get_fixture_statuses,
    is_fixture_completed,
    is_fixture_active,
    is_fixture_staging
)

from found_footy.utils.team_data import (
    get_team_data,
    get_team_ids,
    get_all_teams,
    get_team_by_id,
    is_team_tracked
)

__all__ = [
    # API functions
    "fixtures",
    "fixtures_events", 
    "fixtures_batch",
    "filter_fixtures_by_teams",
    # Flow functions
    "ingest_flow",
    "monitor_flow",
    "advance_flow", 
    "goal_flow",
    "twitter_flow",
    # Storage
    "FootyMongoStore",
    # Utilities
    "get_fixture_statuses",
    "is_fixture_completed",
    "is_fixture_active",
    "is_fixture_staging",
    "get_team_data",
    "get_team_ids",
    "get_all_teams",
    "get_team_by_id",
    "is_team_tracked"
]