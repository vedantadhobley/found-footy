# ✅ UPDATE: found_footy/flows/__init__.py
"""Flow modules for Found Footy"""

# Domain-specific flows
from .ingest_flow import ingest_flow
from .monitor_flow import monitor_flow
from .advance_flow import advance_flow
from .goal_flow import goal_flow
from .twitter_flow import twitter_flow
from .filter_flow import filter_flow  # ✅ ADD

# Shared utilities
from .shared_tasks import (
    fixtures_process_parameters_task,
    fixtures_fetch_api_task,
    fixtures_categorize_task,
    fixtures_store_task,
    fixtures_delta_task,
    fixtures_advance_task
)
from .flow_naming import FlowNamingService

__all__ = [
    # Flows
    "ingest_flow",
    "monitor_flow",
    "advance_flow", 
    "goal_flow",
    "twitter_flow",
    "filter_flow",  # ✅ ADD
    # Tasks
    "fixtures_process_parameters_task",
    "fixtures_fetch_api_task",
    "fixtures_categorize_task", 
    "fixtures_store_task",
    "fixtures_delta_task",
    "fixtures_advance_task",
    # Services
    "FlowNamingService"
]