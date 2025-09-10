# ✅ UPDATE: found_footy/__init__.py
"""Found Footy - Enterprise Football Data Pipeline"""

__version__ = "0.1.0"
__author__ = "Vedanta Dhobley"
__description__ = "Real-time football data processing with Prefect 3"

# Export main flows for easy importing
from found_footy.flows.ingest_flow import ingest_flow
from found_footy.flows.monitor_flow import monitor_flow
from found_footy.flows.advance_flow import advance_flow
from found_footy.flows.goal_flow import goal_flow
from found_footy.flows.twitter_flow import twitter_flow

# Export shared utilities
from found_footy.storage.mongo_store import FootyMongoStore
from found_footy.flows.flow_naming import FlowNamingService

__all__ = [
    "ingest_flow",
    "monitor_flow", 
    "advance_flow",
    "goal_flow",
    "twitter_flow",
    "FootyMongoStore",
    "FlowNamingService"
]