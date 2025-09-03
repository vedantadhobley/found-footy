"""Centralized flow naming system for Found Footy"""
from datetime import datetime, timezone
from typing import Optional, Dict, Any
from found_footy.storage.mongo_store import FootyMongoStore

store = FootyMongoStore()

class FlowNamingService:
    """Professional flow naming service with rich contextual names"""
    
    @staticmethod
    def get_fixtures_ingest_name(date_str: Optional[str] = None, team_count: Optional[int] = None) -> str:
        """Generate rich name for fixtures ingest flow"""
        try:
            if date_str:
                readable_date = datetime.strptime(date_str[:8], "%Y%m%d").strftime("%a %b %d")
            else:
                readable_date = datetime.now().strftime("%a %b %d")
            
            team_suffix = f" ({team_count} teams)" if team_count else " - All Teams"
            return f"📥 INGEST: {readable_date}{team_suffix}"
        except:
            return "📥 INGEST: Date TBD - All Teams"
    
    @staticmethod
    def get_fixtures_monitor_name(timestamp: Optional[datetime] = None) -> str:
        """Generate timestamped name for monitor flow"""
        try:
            if timestamp is None:
                timestamp = datetime.now()
            time_str = timestamp.strftime("%H:%M:%S")
            return f"👁️ MONITOR: {time_str} - Active Check"
        except:
            return "👁️ MONITOR: Active Check"
    
    @staticmethod
    def get_fixtures_advance_name(
        source_collection: str, 
        destination_collection: str, 
        fixture_id: Optional[int] = None
    ) -> str:
        """Generate contextual name for fixture advancement"""
        try:
            if fixture_id:
                # Try to get fixture details for rich name
                fixture = None
                if source_collection == "fixtures_staging":
                    fixture = store.fixtures_staging.find_one({"fixture_id": fixture_id})
                elif source_collection == "fixtures_active":
                    fixture = store.fixtures_active.find_one({"fixture_id": fixture_id})
                
                if fixture:
                    home_team = fixture.get("team_names", {}).get("home", "Home")
                    away_team = fixture.get("team_names", {}).get("away", "Away")
                    match_name = f"{home_team} vs {away_team}"
                    
                    if destination_collection == "fixtures_active":
                        return f"🚀 KICKOFF: {match_name} [#{fixture_id}]"
                    elif destination_collection == "fixtures_processed":
                        return f"🏁 COMPLETED: {match_name} [#{fixture_id}]"
                    else:
                        return f"🔄 ADVANCE: {match_name} [#{fixture_id}]"
                else:
                    return f"🔄 ADVANCE: Match #{fixture_id}"
            else:
                return f"🔄 ADVANCE: {source_collection} → {destination_collection}"
        except:
            return f"🔄 ADVANCE: {source_collection} → {destination_collection}"
    
    @staticmethod
    def get_twitter_search_name(goal_id: Optional[str] = None) -> str:
        """Generate contextual name for Twitter search flow"""
        try:
            if goal_id:
                # Try to get goal details from database
                goal_doc = store.goals_active.find_one({"_id": goal_id})
                if goal_doc:
                    return f"⚽ {goal_doc['team_name']}: {goal_doc['player_name']} ({goal_doc['minute']}') [#{goal_doc['fixture_id']}]"
                else:
                    # Fallback parsing from goal_id
                    parts = goal_id.split('_')
                    if len(parts) >= 3:
                        fixture_id = parts[0]
                        minute = parts[1]
                        return f"🔍 TWITTER: Goal {minute}' [#{fixture_id}] ({goal_id})"
                    else:
                        return f"🔍 TWITTER: Goal {goal_id}"
            else:
                return "🔍 TWITTER: No Goal ID"
        except:
            return f"🔍 TWITTER: Goal {goal_id or 'Unknown'}"

    @staticmethod
    def generate_flow_run_name(flow_name: str, parameters: Dict[str, Any]) -> str:
        """Universal flow name generator based on flow type and parameters"""
        try:
            if flow_name == "fixtures-ingest-flow":
                return FlowNamingService.get_fixtures_ingest_name(
                    parameters.get("date_str"),
                    parameters.get("team_count")
                )
            elif flow_name == "fixtures-monitor-flow":
                return FlowNamingService.get_fixtures_monitor_name()
            elif flow_name == "fixtures-advance-flow":
                return FlowNamingService.get_fixtures_advance_name(
                    parameters.get("source_collection", "unknown"),
                    parameters.get("destination_collection", "unknown"),
                    parameters.get("fixture_id")
                )
            elif flow_name == "twitter-search-flow":
                return FlowNamingService.get_twitter_search_name(
                    parameters.get("goal_id")
                )
            else:
                return f"❓ UNKNOWN: {flow_name}"
        except Exception as e:
            return f"❌ ERROR: {flow_name} ({str(e)[:50]})"

# ✅ CONVENIENCE FUNCTIONS FOR EASY IMPORT
def get_fixtures_ingest_name(date_str=None, team_count=None):
    return FlowNamingService.get_fixtures_ingest_name(date_str, team_count)

def get_fixtures_monitor_name(timestamp=None):
    return FlowNamingService.get_fixtures_monitor_name(timestamp)

def get_fixtures_advance_name(source_collection, destination_collection, fixture_id=None):
    return FlowNamingService.get_fixtures_advance_name(source_collection, destination_collection, fixture_id)

def get_twitter_search_name(goal_id=None):
    return FlowNamingService.get_twitter_search_name(goal_id)

def generate_flow_run_name(flow_name, parameters):
    return FlowNamingService.generate_flow_run_name(flow_name, parameters)