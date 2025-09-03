"""Professional flow triggering service with rich naming"""
import asyncio
from datetime import datetime, timedelta
from prefect.deployments import run_deployment
from found_footy.flows.flow_naming import FlowNamingService

class FlowTriggerService:
    """Professional flow triggering with centralized naming"""
    
    @staticmethod
    def trigger_fixtures_ingest(date_str=None, team_ids=None, run_now=True):
        """Trigger fixtures ingest with rich naming"""
        flow_name = FlowNamingService.get_fixtures_ingest_name(date_str)
        
        return run_deployment(
            name="fixtures-ingest-manual/fixtures-ingest-manual",
            parameters={
                "date_str": date_str,
                "team_ids": team_ids
            },
            flow_run_name=flow_name,
            timeout=0 if run_now else None
        )
    
    @staticmethod
    def trigger_fixtures_monitor(run_now=True):
        """Trigger monitor with timestamp"""
        flow_name = FlowNamingService.get_fixtures_monitor_name()
        
        return run_deployment(
            name="fixtures-monitor-flow/fixtures-monitor-flow",
            parameters={},
            flow_run_name=flow_name,
            timeout=0 if run_now else None
        )
    
    @staticmethod
    def schedule_fixtures_advance(
        source_collection,
        destination_collection, 
        fixture_id,
        scheduled_time=None,
        run_now=False
    ):
        """Schedule or trigger fixture advancement with rich naming"""
        flow_name = FlowNamingService.get_fixtures_advance_name(
            source_collection, destination_collection, fixture_id
        )
        
        return run_deployment(
            name="fixtures-advance-flow/fixtures-advance-flow",
            parameters={
                "source_collection": source_collection,
                "destination_collection": destination_collection,
                "fixture_id": fixture_id
            },
            flow_run_name=flow_name,
            scheduled_time=scheduled_time,
            timeout=0 if run_now else None
        )
    
    @staticmethod
    def trigger_twitter_search(goal_id):
        """Trigger Twitter search with goal context"""
        flow_name = FlowNamingService.get_twitter_search_name(goal_id)
        
        return run_deployment(
            name="twitter-search-flow/twitter-search-flow",
            parameters={"goal_id": goal_id},
            flow_run_name=flow_name,
            timeout=0
        )

# ✅ CONVENIENCE FUNCTIONS
def trigger_ingest(date_str=None, team_ids=None):
    return FlowTriggerService.trigger_fixtures_ingest(date_str, team_ids)

def trigger_monitor():
    return FlowTriggerService.trigger_fixtures_monitor()

def schedule_advance(source, destination, fixture_id, scheduled_time=None):
    return FlowTriggerService.schedule_fixtures_advance(source, destination, fixture_id, scheduled_time)

def trigger_twitter(goal_id):
    return FlowTriggerService.trigger_twitter_search(goal_id)