"""Centralized flow naming system for Found Footy"""
from datetime import datetime, timezone
from typing import Optional, Dict, Any
from found_footy.storage.mongo_store import FootyMongoStore

store = FootyMongoStore()

class FlowNamingService:
    """Professional flow naming service with rich contextual names"""
    
    @staticmethod
    def get_ingest_flow_name(date_str: Optional[str] = None, team_count: Optional[int] = None) -> str:
        """Generate rich name for ingest flow"""
        try:
            if date_str:
                readable_date = datetime.strptime(date_str[:8], "%Y%m%d").strftime("%a %b %d")
            else:
                readable_date = datetime.now().strftime("%a %b %d")
            
            if team_count is None:
                from found_footy.utils.team_data import get_team_ids
                team_count = len(get_team_ids())
            
            team_suffix = f" ({team_count} teams)" if team_count else " - All Teams"
            return f"📥 INGEST: {readable_date}{team_suffix}"
        except:
            return "📥 INGEST: Date TBD - All Teams"
    
    @staticmethod
    def get_monitor_flow_name(timestamp: Optional[datetime] = None) -> str:
        """Generate timestamped name for monitor flow with active fixture count"""
        try:
            if timestamp is None:
                timestamp = datetime.now()
            time_str = timestamp.strftime("%H:%M:%S")
            
            # Get active fixture count for richer naming
            try:
                active_count = store.fixtures_active.count_documents({})
                if active_count > 0:
                    return f"👁️ MONITOR: {time_str} - {active_count} active fixtures"
                else:
                    return f"👁️ MONITOR: {time_str} - No active fixtures"
            except:
                # Fallback if database is unavailable
                return f"👁️ MONITOR: {time_str} - Active Check"
                
        except:
            return "👁️ MONITOR: Active Check"
    
    @staticmethod
    def get_advance_flow_name(
        source_collection: str, 
        destination_collection: str, 
        fixture_id: Optional[int] = None
    ) -> str:
        """Generate contextual name for advance flow using raw schema"""
        try:
            if fixture_id:
                # Try to get fixture details for rich name AT RUNTIME
                fixture = None
                if source_collection == "fixtures_staging":
                    fixture = store.fixtures_staging.find_one({"_id": fixture_id})
                elif source_collection == "fixtures_active":
                    fixture = store.fixtures_active.find_one({"fixture_id": fixture_id})
                
                if fixture:
                    # Use raw schema extraction
                    home_team, away_team = store._extract_team_names(fixture)
                    current_goals = store._extract_current_goals(fixture)
                    home_score = current_goals.get("home", 0)
                    away_score = current_goals.get("away", 0)
                    
                    if destination_collection == "fixtures_active":
                        return f"🚀 KICKOFF: {home_team} vs {away_team} [#{fixture_id}]"
                    elif destination_collection == "fixtures_completed":
                        return f"🏁 FINAL: {home_team} {home_score}-{away_score} {away_team} [#{fixture_id}]"
                    else:
                        return f"🔄 ADVANCE: {home_team} vs {away_team} → {destination_collection} [#{fixture_id}]"
                else:
                    return f"🔄 ADVANCE: Match #{fixture_id} → {destination_collection}"
            else:
                return f"🔄 ADVANCE: {source_collection} → {destination_collection}"
        except Exception as e:
            print(f"⚠️ Error in advance flow naming: {e}")
            return f"🔄 ADVANCE: {source_collection} → {destination_collection}"
    
    @staticmethod
    def get_twitter_flow_name(goal_id: Optional[str] = None) -> str:
        """Generate contextual name for twitter video scraping flow"""
        try:
            if goal_id:
                # Try to get goal details from database
                goal_doc = store.goals_pending.find_one({"_id": goal_id})
                if goal_doc:
                    player_name = goal_doc.get("player_name", "Unknown")
                    team_name = goal_doc.get("team_name", "Unknown") 
                    minute = goal_doc.get("minute", 0)
                    fixture_id = goal_doc.get("fixture_id", "unknown")
                    
                    # ✅ Extract extra time from goal_id for display
                    minute_display = str(minute)
                    if '+' in goal_id:
                        # Extract the time part after underscore: "12345_45+3" -> "45+3"
                        time_part = '_'.join(goal_id.split('_')[1:])  # Everything after first underscore
                        minute_display = time_part
                    
                    # ✅ Get fixture details for team names and score
                    fixture = store.fixtures_active.find_one({"_id": fixture_id})
                    if fixture:
                        home_team = fixture.get("teams", {}).get("home", {}).get("name", "Unknown")
                        away_team = fixture.get("teams", {}).get("away", {}).get("name", "Unknown")
                        home_goals = fixture.get("goals", {}).get("home", 0) or 0
                        away_goals = fixture.get("goals", {}).get("away", 0) or 0
                        
                        return f"📥 VIDEO: {home_team} {home_goals}-{away_goals} {away_team} | {team_name} - {player_name} ({minute_display}') [#{fixture_id}]"
                    else:
                        return f"📥 VIDEO: {team_name} - {player_name} ({minute_display}') [#{fixture_id}]"
                else:
                    # Fallback parsing from goal_id with + format
                    if '_' in goal_id:
                        parts = goal_id.split('_', 1)  # Split only on first underscore
                        fixture_id = parts[0]
                        minute_display = parts[1] if len(parts) > 1 else "unknown"
                        return f"📥 VIDEO: Goal {minute_display}' [#{fixture_id}]"
                    else:
                        return f"📥 VIDEO: Goal {goal_id}"
            else:
                return "📥 VIDEO SEARCH: No Goal ID"
        except Exception as e:
            print(f"❌ Error in get_twitter_flow_name: {e}")
            return f"📥 VIDEO: Goal {goal_id or 'Unknown'}"

    @staticmethod
    def get_goal_flow_name(fixture_id: int, goal_count: int = 0) -> str:
        """Generate contextual name for goal flow using raw schema"""
        try:
            # ✅ FIX: Use new schema with _id
            fixture = store.fixtures_active.find_one({"_id": fixture_id})
            if fixture:
                home_team, away_team = store._extract_team_names(fixture)
                current_goals = store._extract_current_goals(fixture)
                home_score = current_goals.get("home", 0)
                away_score = current_goals.get("away", 0)
                
                return f"⚽ GOALS: {home_team} {home_score}-{away_score} {away_team} - {goal_count} events [#{fixture_id}]"
            else:
                return f"⚽ GOALS: Match #{fixture_id} - {goal_count} events"
        except Exception as e:
            print(f"❌ Error in get_goal_flow_name: {e}")
            return f"⚽ GOALS: Match #{fixture_id} - {goal_count} events"

    @staticmethod
    def generate_flow_run_name(flow_name: str, parameters: Dict[str, Any]) -> str:
        """Universal flow name generator based on flow type and parameters"""
        try:
            if flow_name == "ingest-flow":
                return FlowNamingService.get_ingest_flow_name(
                    parameters.get("date_str"),
                    parameters.get("team_count")
                )
            elif flow_name == "monitor-flow":
                return FlowNamingService.get_monitor_flow_name()
            elif flow_name == "advance-flow":
                return FlowNamingService.get_advance_flow_name(
                    parameters.get("source_collection", "unknown"),
                    parameters.get("destination_collection", "unknown"),
                    parameters.get("fixture_id")
                )
            elif flow_name == "goal-flow":
                return FlowNamingService.get_goal_flow_name(
                    parameters.get("fixture_id", 0),
                    len(parameters.get("goal_events", []))
                )
            elif flow_name == "twitter-flow":
                return FlowNamingService.get_twitter_flow_name(
                    parameters.get("goal_id")
                )
            else:
                return f"❓ UNKNOWN: {flow_name}"
        except Exception as e:
            return f"❌ ERROR: {flow_name} ({str(e)[:50]})"

    @staticmethod
    def get_download_flow_name(goal_id: Optional[str] = None) -> str:
        """Generate contextual name for download flow"""
        try:
            if goal_id:
                # Try to get goal details from database
                goal_doc = store.goals_pending.find_one({"_id": goal_id}) or store.goals_processed.find_one({"_id": goal_id})
                if goal_doc:
                    player_name = goal_doc.get("player_name", "Unknown")
                    team_name = goal_doc.get("team_name", "Unknown")
                    minute = goal_doc.get("minute", 0)
                    fixture_id = goal_doc.get("fixture_id", "unknown")
                    video_count = len(goal_doc.get("discovered_videos", []))
                    
                    # ✅ Extract extra time from goal_id for display
                    minute_display = str(minute)
                    if '+' in goal_id:
                        time_part = '_'.join(goal_id.split('_')[1:])  # Everything after first underscore
                        minute_display = time_part
                    
                    return f"📥 S3 DOWNLOAD: {team_name} - {player_name} ({minute_display}') - {video_count} videos [#{fixture_id}]"
                else:
                    # Fallback parsing from goal_id with + format
                    if '_' in goal_id:
                        parts = goal_id.split('_', 1)  # Split only on first underscore
                        fixture_id = parts[0]
                        minute_display = parts[1] if len(parts) > 1 else "unknown"
                        return f"📥 S3 DOWNLOAD: Goal {minute_display}' [#{fixture_id}]"
                    else:
                        return f"📥 S3 DOWNLOAD: {goal_id}"
            else:
                return "📥 S3 DOWNLOAD: No Goal ID"
        except Exception as e:
            print(f"❌ Error in get_download_flow_name: {e}")
            return f"📥 S3 DOWNLOAD: Goal {goal_id or 'Unknown'}"

# Add convenience function
def get_download_flow_name(goal_id=None):
    return FlowNamingService.get_download_flow_name(goal_id)

# ✅ RUNTIME NAMING: Functions that get called at runtime
def runtime_advance_flow_name():
    """Runtime naming for advance flow - called when flow actually runs"""
    from prefect.context import get_run_context
    try:
        context = get_run_context()
        parameters = context.flow_run.parameters
        return FlowNamingService.get_advance_flow_name(
            parameters.get("source_collection", "unknown"),
            parameters.get("destination_collection", "unknown"),
            parameters.get("fixture_id")
        )
    except:
        return "🔄 ADVANCE: Runtime naming"

# ✅ IMMEDIATE NAMING: Functions for flows that run immediately
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

def generate_flow_run_name(flow_name, parameters):
    return FlowNamingService.generate_flow_run_name(flow_name, parameters)

# ✅ @FLOW DECORATOR FUNCTIONS: Only for immediate execution flows
def generate_ingest_flow_name():
    return get_ingest_flow_name()

def generate_monitor_flow_name():
    return get_monitor_flow_name()