# ✅ CREATE: found_footy/api/__init__.py
"""API modules for Found Footy"""

from .mongo_api import (
    fixtures,
    fixtures_events,
    fixtures_batch,
    filter_fixtures_by_teams,
    parse_team_ids_parameter
)

from .twitter_api import (  # ✅ ADD: Twitter API functions
    twitter_videos_search,
    twitter_video_metadata
)

__all__ = [
    # MongoDB API
    "fixtures",
    "fixtures_events", 
    "fixtures_batch",
    "filter_fixtures_by_teams",
    "parse_team_ids_parameter",
    # Twitter API
    "twitter_videos_search",
    "twitter_video_metadata"
]