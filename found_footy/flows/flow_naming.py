"""Centralized flow naming system for Found Footy - FIXED imports"""
from datetime import datetime, timezone
from typing import Optional, Dict, Any

class FlowNamingService:
    """Professional flow naming service with safe MongoDB access"""
    
    @staticmethod
    def get_monitor_flow_name(timestamp: Optional[datetime] = None) -> str:
        """Generate timestamped name for monitor flow"""
        try:
            if timestamp is None:
                timestamp = datetime.now()
            time_str = timestamp.strftime("%H:%M:%S")
            
            # ✅ SAFE: Try to get active fixture count
            try:
                from found_footy.storage.mongo_store import FootyMongoStore
                store = FootyMongoStore()
                active_count = store.fixtures_active.count_documents({})
                if active_count > 0:
                    return f"👁️ MONITOR: {time_str} - {active_count} active"
                else:
                    return f"👁️ MONITOR: {time_str} - No active fixtures"
            except Exception:
                # Safe fallback if MongoDB is unavailable
                return f"👁️ MONITOR: {time_str} - Active Check"
                
        except Exception:
            return "👁️ MONITOR: Active Check"
    
    @staticmethod
    def get_advance_flow_name(
        source_collection: str, 
        destination_collection: str, 
        fixture_id: Optional[int] = None
    ) -> str:
        """Generate contextual name for advance flow"""
        try:
            if fixture_id:
                # ✅ SAFE: Try to get fixture details for rich name
                try:
                    from found_footy.storage.mongo_store import FootyMongoStore
                    store = FootyMongoStore()
                    
                    fixture = None
                    if source_collection == "fixtures_staging":
                        fixture = store.fixtures_staging.find_one({"fixture_id": fixture_id})
                    elif source_collection == "fixtures_active":
                        fixture = store.fixtures_active.find_one({"fixture_id": fixture_id})
                    
                    if fixture:
                        home_team, away_team = store._extract_team_names(fixture)
                        return f"🔄 ADVANCE: {home_team} vs {away_team} → {destination_collection}"
                    else:
                        return f"🔄 ADVANCE: Match #{fixture_id} → {destination_collection}"
                except Exception:
                    return f"🔄 ADVANCE: Match #{fixture_id} → {destination_collection}"
            else:
                return f"🔄 ADVANCE: {source_collection} → {destination_collection}"
        except Exception:
            return f"🔄 ADVANCE: {source_collection} → {destination_collection}"
    
    @staticmethod
    def get_twitter_flow_name(goal_id: Optional[str] = None) -> str:
        """Generate contextual name for twitter flow"""
        try:
            if goal_id:
                # ✅ SAFE: Try to get goal details from database
                try:
                    from found_footy.storage.mongo_store import FootyMongoStore
                    store = FootyMongoStore()
                    
                    goal_doc = store.goals_pending.find_one({"_id": goal_id})
                    if goal_doc:
                        player_name = goal_doc.get("player_name", "Unknown")
                        team_name = goal_doc.get("team_name", "Unknown")
                        minute = goal_doc.get("minute", 0)
                        return f"🔍 VIDEO SEARCH: {player_name} ({team_name}) {minute}'"
                    else:
                        return f"🔍 VIDEO SEARCH: Goal {goal_id}"
                except Exception:
                    return f"🔍 VIDEO SEARCH: Goal {goal_id}"
            else:
                return "🔍 VIDEO SEARCH: Unknown Goal"
        except Exception:
            return f"🔍 VIDEO SEARCH: Goal {goal_id or 'Unknown'}"

    @staticmethod
    def get_goal_flow_name(fixture_id: int, goal_count: int = 0) -> str:
        """Generate contextual name for goal flow"""
        try:
            # ✅ SAFE: Try to get fixture details for rich name
            try:
                from found_footy.storage.mongo_store import FootyMongoStore
                store = FootyMongoStore()
                fixture = store.fixtures_active.find_one({"fixture_id": fixture_id})
                if fixture:
                    home_team, away_team = store._extract_team_names(fixture)
                    return f"⚽ GOALS: {home_team} vs {away_team} - {goal_count} events"
                else:
                    return f"⚽ GOALS: Match #{fixture_id} - {goal_count} events"
            except Exception:
                return f"⚽ GOALS: Match #{fixture_id} - {goal_count} events"
        except Exception:
            return f"⚽ GOALS: Match #{fixture_id} - {goal_count} events"

    @staticmethod
    def get_download_flow_name(goal_id: Optional[str] = None) -> str:
        """Generate contextual name for download flow"""
        try:
            if goal_id:
                # ✅ SAFE: Try to get goal details from database
                try:
                    from found_footy.storage.mongo_store import FootyMongoStore
                    store = FootyMongoStore()
                    
                    goal_doc = store.goals_pending.find_one({"_id": goal_id})
                    if goal_doc:
                        player_name = goal_doc.get("player_name", "Unknown")
                        team_name = goal_doc.get("team_name", "Unknown")
                        return f"📥 S3 DOWNLOAD: {player_name} ({team_name})"
                    else:
                        return f"📥 S3 DOWNLOAD: Goal {goal_id}"
                except Exception:
                    return f"📥 S3 DOWNLOAD: Goal {goal_id}"
            else:
                return "📥 S3 DOWNLOAD: Unknown Goal"
        except Exception:
            return f"📥 S3 DOWNLOAD: Goal {goal_id or 'Unknown'}"

    @staticmethod
    def get_ingest_flow_name(date_str: Optional[str] = None, team_count: Optional[int] = None) -> str:
        """Generate rich name for ingest flow"""
        try:
            if date_str:
                readable_date = datetime.strptime(date_str[:8], "%Y%m%d").strftime("%a %b %d")
            else:
                readable_date = datetime.now().strftime("%a %b %d")
            
            if team_count is None:
                try:
                    from found_footy.utils.team_data import get_team_ids
                    team_count = len(get_team_ids())
                except Exception:
                    team_count = 50  # Safe fallback
            
            team_suffix = f" ({team_count} teams)" if team_count else " - All Teams"
            return f"📥 INGEST: {readable_date}{team_suffix}"
        except Exception:
            return "📥 INGEST: Date TBD - All Teams"

# ✅ CLEAN: All convenience functions with safe fallbacks
def runtime_advance_flow_name():
    """Runtime naming for advance flow"""
    try:
        from prefect.context import get_run_context
        context = get_run_context()
        parameters = context.flow_run.parameters
        return FlowNamingService.get_advance_flow_name(
            parameters.get("source_collection", "unknown"),
            parameters.get("destination_collection", "unknown"),
            parameters.get("fixture_id")
        )
    except Exception:
        return "🔄 ADVANCE: Runtime naming"

def get_ingest_flow_name(date_str=None, team_count=None):
    return FlowNamingService.get_ingest_flow_name(date_str, team_count)

def get_monitor_flow_name(timestamp=None):
    return FlowNamingService.get_monitor_flow_name(timestamp)

def get_advance_flow_name(source_collection, destination_collection, fixture_id=None):
    return FlowNamingService.get_advance_flow_name(source_collection, destination_collection, fixture_id)

def get_twitter_flow_name(goal_id=None):
    return FlowNamingService.get_twitter_flow_name(goal_id)

def get_goal_flow_name(fixture_id, goal_count=0):
    return FlowNamingService.get_goal_flow_name(fixture_id, goal_count)

def get_download_flow_name(goal_id=None):
    return FlowNamingService.get_download_flow_name(goal_id)

def generate_ingest_flow_name():
    return get_ingest_flow_name()

def generate_monitor_flow_name():
    return get_monitor_flow_name()