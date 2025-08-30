from pymongo import MongoClient
from datetime import datetime, timezone, timedelta
from typing import Dict, List, Optional
import os

class FootyMongoStore:
    """MongoDB storage for football application data"""
    
    def __init__(self, connection_url=None):
        if connection_url is None:
            connection_url = os.getenv('MONGODB_URL', 'mongodb://localhost:27017/')
        
        self.client = MongoClient(connection_url)
        self.db = self.client.found_footy
        
        # Collections
        self.fixtures = self.db.fixtures
        self.events = self.db.events
        self.youtube_results = self.db.youtube_results
        self.leagues = self.db.leagues
        self.teams = self.db.teams
        self.live_fixtures = self.db.live_fixtures  # ✅ NEW: Active monitoring collection
        
        # Create indexes for performance
        self._create_indexes()
    
    def _create_indexes(self):
        """Create database indexes for better query performance"""
        try:
            # Fixture indexes
            self.fixtures.create_index([("status", 1)])
            self.fixtures.create_index([("fixture_date", 1)])
            self.fixtures.create_index([("home_team_name", 1)])
            self.fixtures.create_index([("away_team_name", 1)])
            self.fixtures.create_index([("home_team_id", 1)])
            self.fixtures.create_index([("away_team_id", 1)])
            self.fixtures.create_index([("league_id", 1)])
            self.fixtures.create_index([("league_id", 1), ("fixture_date", 1)])
            
            # Compound indexes for team-based queries
            self.fixtures.create_index([("home_team_id", 1), ("fixture_date", 1)])
            self.fixtures.create_index([("away_team_id", 1), ("fixture_date", 1)])
            
            # Event indexes
            self.events.create_index([("fixture_id", 1)])
            self.events.create_index([("event_type", 1)])
            
            # League indexes (backward compatibility)
            self.leagues.create_index([("league_id", 1)], unique=True)
            self.leagues.create_index([("league_type", 1)])
            
            # Team indexes
            self.teams.create_index([("team_id", 1)], unique=True)
            self.teams.create_index([("country", 1)])
            self.teams.create_index([("uefa_ranking", 1)])
            
            # ✅ NEW: Live fixtures indexes
            self.live_fixtures.create_index([("fixture_id", 1)], unique=True)
            self.live_fixtures.create_index([("status", 1)])
            self.live_fixtures.create_index([("kickoff_time", 1)])
            self.live_fixtures.create_index([("monitoring_status", 1)])
            
            print("✅ MongoDB indexes created successfully")
        except Exception as e:
            print(f"⚠️ Error creating indexes: {e}")

    # ✅ NEW: Live fixtures management methods
    def add_fixture_to_live_monitoring(self, fixture_data: dict) -> bool:
        """Add fixture to live monitoring collection"""
        try:
            fixture_id = fixture_data["id"]
            
            document = {
                "_id": fixture_id,
                "fixture_id": fixture_id,
                "home_team": fixture_data["home"],
                "home_team_id": fixture_data["home_id"],
                "away_team": fixture_data["away"],
                "away_team_id": fixture_data["away_id"],
                "league": fixture_data["league"],
                "league_id": fixture_data["league_id"],
                "kickoff_time": datetime.fromisoformat(fixture_data["time"].replace('Z', '+00:00')),
                "monitoring_status": "scheduled",  # scheduled, active, completed
                "status": "NS",  # Not Started
                "last_goals": {"home": 0, "away": 0},
                "last_events_count": 0,
                "added_to_monitoring": datetime.now(timezone.utc),
                "last_api_check": None,
                "last_api_data": None
            }
            
            result = self.live_fixtures.replace_one(
                {"_id": fixture_id}, 
                document, 
                upsert=True
            )
            
            print(f"✅ Added to live monitoring: {fixture_data['home']} vs {fixture_data['away']}")
            return True
            
        except Exception as e:
            print(f"❌ Error adding fixture to live monitoring: {e}")
            return False
    
    def get_fixtures_ready_for_monitoring(self, minutes_before_kickoff: int = 5) -> List[dict]:
        """Get fixtures that should start monitoring (5 minutes before kickoff)"""
        try:
            now = datetime.now(timezone.utc)
            monitoring_threshold = now + timedelta(minutes=minutes_before_kickoff)
            
            query = {
                "monitoring_status": "scheduled",
                "kickoff_time": {"$lte": monitoring_threshold}
            }
            
            return list(self.live_fixtures.find(query))
        except Exception as e:
            print(f"❌ Error getting fixtures ready for monitoring: {e}")
            return []
    
    def get_active_live_fixtures(self) -> List[dict]:
        """Get all fixtures currently being monitored"""
        try:
            query = {"monitoring_status": "active"}
            return list(self.live_fixtures.find(query))
        except Exception as e:
            print(f"❌ Error getting active live fixtures: {e}")
            return []
    
    def activate_fixture_monitoring(self, fixture_id: int) -> bool:
        """Activate monitoring for a fixture"""
        try:
            result = self.live_fixtures.update_one(
                {"_id": fixture_id},
                {
                    "$set": {
                        "monitoring_status": "active",
                        "monitoring_started": datetime.now(timezone.utc)
                    }
                }
            )
            
            if result.modified_count > 0:
                print(f"✅ Activated monitoring for fixture {fixture_id}")
                return True
            else:
                print(f"⚠️ Fixture {fixture_id} not found for activation")
                return False
                
        except Exception as e:
            print(f"❌ Error activating fixture monitoring: {e}")
            return False
    
    def update_live_fixture_data(self, fixture_id: int, api_data: dict, events_data: list) -> dict:
        """Update live fixture with latest API data and detect changes"""
        try:
            # Get current state
            current_fixture = self.live_fixtures.find_one({"_id": fixture_id})
            if not current_fixture:
                print(f"⚠️ Live fixture {fixture_id} not found")
                return {"changes_detected": False}
            
            # Extract current API data
            fixture_details = api_data.get("fixture", {})
            goals_data = api_data.get("goals", {"home": 0, "away": 0})
            status = fixture_details.get("status", {}).get("short", "UNKNOWN")
            
            current_home_goals = goals_data.get("home") or 0
            current_away_goals = goals_data.get("away") or 0
            
            # Get previous state
            previous_home_goals = current_fixture.get("last_goals", {}).get("home", 0)
            previous_away_goals = current_fixture.get("last_goals", {}).get("away", 0)
            previous_events_count = current_fixture.get("last_events_count", 0)
            
            # Detect changes
            changes_detected = {
                "goals_changed": False,
                "new_home_goals": 0,
                "new_away_goals": 0,
                "status_changed": False,
                "new_events": [],
                "fixture_completed": False
            }
            
            # Check for goal changes
            if current_home_goals > previous_home_goals or current_away_goals > previous_away_goals:
                changes_detected["goals_changed"] = True
                changes_detected["new_home_goals"] = current_home_goals - previous_home_goals
                changes_detected["new_away_goals"] = current_away_goals - previous_away_goals
                print(f"🚨 GOALS CHANGED for {fixture_id}: {previous_home_goals}-{previous_away_goals} → {current_home_goals}-{current_away_goals}")
            
            # Check for new events (goals specifically)
            goal_events = [event for event in events_data if event.get("type") == "Goal"]
            if len(goal_events) > previous_events_count:
                new_goal_events = goal_events[previous_events_count:]
                changes_detected["new_events"] = new_goal_events
                print(f"⚽ NEW GOAL EVENTS for {fixture_id}: {len(new_goal_events)} new goals")
            
            # Check if fixture completed
            if status in ["FT", "AET", "PEN"]:
                changes_detected["fixture_completed"] = True
                print(f"🏁 Fixture {fixture_id} completed with status {status}")
            
            # Update the live fixture record
            update_data = {
                "status": status,
                "last_goals": {"home": current_home_goals, "away": current_away_goals},
                "last_events_count": len(goal_events),
                "last_api_check": datetime.now(timezone.utc),
                "last_api_data": api_data
            }
            
            # Mark as completed if finished
            if changes_detected["fixture_completed"]:
                update_data["monitoring_status"] = "completed"
                update_data["completed_at"] = datetime.now(timezone.utc)
            
            self.live_fixtures.update_one(
                {"_id": fixture_id},
                {"$set": update_data}
            )
            
            changes_detected["changes_detected"] = (
                changes_detected["goals_changed"] or 
                len(changes_detected["new_events"]) > 0 or 
                changes_detected["fixture_completed"]
            )
            
            return changes_detected
            
        except Exception as e:
            print(f"❌ Error updating live fixture data: {e}")
            return {"changes_detected": False}
    
    def remove_completed_fixtures_from_monitoring(self) -> int:
        """Remove completed fixtures from live monitoring"""
        try:
            result = self.live_fixtures.delete_many({
                "monitoring_status": "completed"
            })
            
            deleted_count = result.deleted_count
            if deleted_count > 0:
                print(f"🧹 Removed {deleted_count} completed fixtures from live monitoring")
            
            return deleted_count
            
        except Exception as e:
            print(f"❌ Error removing completed fixtures: {e}")
            return 0

    # ✅ Keep all existing methods...
    def store_fixture(self, fixture_data: dict) -> bool:
        """Store fixture data with fixture_id as _id"""
        try:
            fixture_info = fixture_data["fixture"]
            teams_info = fixture_data["teams"]
            goals_info = fixture_data["goals"]
            league_info = fixture_data["league"]
            
            document = {
                "_id": fixture_info["id"],
                "home_team_id": teams_info["home"]["id"],
                "home_team_name": teams_info["home"]["name"],
                "away_team_id": teams_info["away"]["id"],
                "away_team_name": teams_info["away"]["name"],
                "home_goals": goals_info["home"],
                "away_goals": goals_info["away"],
                "status": fixture_info["status"]["short"],
                "fixture_date": datetime.fromisoformat(fixture_info["date"].replace('Z', '+00:00')),
                "league_id": league_info["id"],
                "league_name": league_info["name"],
                "venue": fixture_data.get("fixture", {}).get("venue", {}),
                "raw_api_data": fixture_data,
                "stored_at": datetime.now(timezone.utc),
                "updated_at": datetime.now(timezone.utc)
            }
            
            result = self.fixtures.replace_one(
                {"_id": fixture_info["id"]}, 
                document, 
                upsert=True
            )
            
            match_info = f"{teams_info['home']['name']} {goals_info['home']} - {goals_info['away']} {teams_info['away']['name']}"
            print(f"✅ Stored fixture result: {match_info}")
            return True
            
        except Exception as e:
            print(f"❌ Error storing fixture in MongoDB: {e}")
            return False
    
    def store_fixture_events(self, fixture_id: int, events_data: list) -> int:
        """Store fixture events - only Goal events"""
        try:
            goal_events_stored = 0
            
            for event in events_data:
                if event.get("type") == "Goal":
                    time_info = event.get("time", {})
                    team_info = event.get("team", {})
                    player_info = event.get("player", {})
                    assist_info = event.get("assist", {}) or {}
                    
                    # ✅ Create unique _id for events (composite key)
                    event_id = f"{fixture_id}_{time_info.get('elapsed', 0)}_{player_info.get('id', 0)}_{event.get('type', 'Goal')}"
                    
                    document = {
                        "_id": event_id,
                        "fixture_id": fixture_id,
                        "time_elapsed": time_info.get("elapsed"),
                        "time_extra": time_info.get("extra"),
                        "team_id": team_info.get("id"),
                        "team_name": team_info.get("name", "Unknown"),
                        "player_id": player_info.get("id"),
                        "player_name": player_info.get("name", "Unknown"),
                        "assist_id": assist_info.get("id"),
                        "assist_name": assist_info.get("name"),
                        "event_type": event.get("type"),
                        "event_detail": event.get("detail"),
                        "comments": event.get("comments"),
                        
                        # Store complete event data
                        "raw_event_data": event,
                        
                        # Metadata
                        "created_at": datetime.now(timezone.utc)
                    }
                    
                    self.events.replace_one({"_id": event_id}, document, upsert=True)
                    goal_events_stored += 1
            
            print(f"✅ Stored {goal_events_stored} goal events for fixture {fixture_id}")
            return goal_events_stored
            
        except Exception as e:
            print(f"❌ Error storing events for fixture {fixture_id}: {e}")
            return 0
    
    def get_fixture(self, fixture_id: int) -> Optional[dict]:
        """Get fixture by ID - now using _id"""
        try:
            return self.fixtures.find_one({"_id": fixture_id})
        except Exception as e:
            print(f"❌ Error getting fixture {fixture_id}: {e}")
            return None
    
    def get_completed_fixtures(self, date_filter=None, league_ids=None) -> List[dict]:
        """Get completed fixtures, optionally filtered by date and league IDs"""
        try:
            query = {"status": {"$in": ["FT", "AET", "PEN"]}}
            
            if date_filter:
                query["fixture_date"] = {
                    "$gte": datetime.combine(date_filter, datetime.min.time()),
                    "$lt": datetime.combine(date_filter, datetime.max.time())
                }
            
            # ✅ NEW: Filter by league IDs
            if league_ids:
                query["league_id"] = {"$in": league_ids}
            
            return list(self.fixtures.find(query))
        except Exception as e:
            print(f"❌ Error getting completed fixtures: {e}")
            return []
    
    def search_fixtures_by_team(self, team_name: str) -> List[dict]:
        """Search fixtures by team name (case-insensitive)"""
        try:
            query = {
                "$or": [
                    {"home_team_name": {"$regex": team_name, "$options": "i"}},
                    {"away_team_name": {"$regex": team_name, "$options": "i"}}
                ]
            }
            return list(self.fixtures.find(query))
        except Exception as e:
            print(f"❌ Error searching fixtures by team: {e}")
            return []
    
    # ✅ NEW: Team-based fixture queries
    def get_fixtures_by_teams(self, team_ids: List[int], date_filter=None) -> List[dict]:
        """Get fixtures by team IDs and optional date"""
        try:
            query = {
                "$or": [
                    {"home_team_id": {"$in": team_ids}},
                    {"away_team_id": {"$in": team_ids}}
                ]
            }
            
            if date_filter:
                query["fixture_date"] = {
                    "$gte": datetime.combine(date_filter, datetime.min.time()),
                    "$lt": datetime.combine(date_filter, datetime.max.time())
                }
            
            return list(self.fixtures.find(query))
        except Exception as e:
            print(f"❌ Error getting fixtures by teams: {e}")
            return []

    def store_team_metadata(self, team_data: dict) -> bool:
        """Store team metadata for future reference"""
        try:
            document = {
                "_id": team_data["team_id"],
                "team_id": team_data["team_id"],
                "team_name": team_data["team_name"],
                "country": team_data.get("country", "Unknown"),
                "season": team_data.get("season"),
                "uefa_ranking": team_data.get("uefa_ranking"),
                "stored_at": datetime.now(timezone.utc)
            }
            
            self.teams.replace_one(
                {"_id": team_data["team_id"]}, 
                document, 
                upsert=True
            )
            
            print(f"✅ Stored team metadata: {team_data['team_name']} (UEFA #{team_data.get('uefa_ranking', 'N/A')})")
            return True
            
        except Exception as e:
            print(f"❌ Error storing team metadata: {e}")
            return False
    
    def get_available_teams(self) -> List[dict]:
        """Get all available teams"""
        try:
            return list(self.teams.find({}).sort("uefa_ranking", 1))
        except Exception as e:
            print(f"❌ Error getting available teams: {e}")
            return []

    # ✅ BACKWARD COMPATIBILITY: Keep league methods
    def get_fixtures_by_leagues(self, league_ids: List[int], date_filter=None) -> List[dict]:
        """DEPRECATED: Use get_fixtures_by_teams instead"""
        print("⚠️ WARNING: get_fixtures_by_leagues is deprecated. Using team-based approach.")
        # Get all top 25 team IDs from teams collection
        teams = self.get_available_teams()
        team_ids = [team["team_id"] for team in teams]
        return self.get_fixtures_by_teams(team_ids, date_filter)
    
    def store_league_metadata(self, league_data: dict) -> bool:
        """Store league metadata for future reference"""
        try:
            document = {
                "_id": league_data["league_id"],
                "league_id": league_data["league_id"],
                "league_name": league_data["league_name"],
                "league_type": league_data.get("league_type", "domestic"),  # domestic, cup, international
                "country": league_data.get("country", "Unknown"),
                "season": league_data.get("season"),
                "stored_at": datetime.now(timezone.utc)
            }
            
            self.leagues.replace_one(
                {"_id": league_data["league_id"]}, 
                document, 
                upsert=True
            )
            
            print(f"✅ Stored league metadata: {league_data['league_name']}")
            return True
            
        except Exception as e:
            print(f"❌ Error storing league metadata: {e}")
            return False
    
    def get_available_leagues(self) -> List[dict]:
        """Get all available leagues"""
        try:
            return list(self.leagues.find({}))
        except Exception as e:
            print(f"❌ Error getting available leagues: {e}")
            return []
    
    def store_youtube_result(self, fixture_id: int, youtube_data: dict) -> bool:
        """Store YouTube search results with fixture_id as _id"""
        try:
            document = {
                "_id": fixture_id,
                "search_results": youtube_data,
                "stored_at": datetime.now(timezone.utc)
            }
            
            self.youtube_results.replace_one(
                {"_id": fixture_id}, 
                document, 
                upsert=True
            )
            
            print(f"✅ Stored YouTube data for fixture {fixture_id}")
            return True
            
        except Exception as e:
            print(f"❌ Error storing YouTube data: {e}")
            return False
    
    def get_stats(self) -> dict:
        """Get database statistics"""
        try:
            return {
                "fixtures_count": self.fixtures.count_documents({}),
                "events_count": self.events.count_documents({}),
                "leagues_count": self.leagues.count_documents({}),
                "teams_count": self.teams.count_documents({}),
                "youtube_results_count": self.youtube_results.count_documents({}),
                "completed_fixtures": self.fixtures.count_documents({"status": {"$in": ["FT", "AET", "PEN"]}}),
                "goal_events": self.events.count_documents({"event_type": "Goal"}),
                "top_25_teams": self.teams.count_documents({"uefa_ranking": {"$lte": 25}})
            }
        except Exception as e:
            print(f"❌ Error getting stats: {e}")
            return {}
    
    def drop_all_collections(self):
        """Drop all collections in the database for a fresh start"""
        try:
            collection_names = self.db.list_collection_names()
            
            if not collection_names:
                print("ℹ️ No collections found to drop")
                return True
            
            print(f"🗑️ Dropping {len(collection_names)} collections: {collection_names}")
            
            dropped_count = 0
            for collection_name in collection_names:
                self.db[collection_name].drop()
                print(f"  ✅ Dropped: {collection_name}")
                dropped_count += 1
            
            print(f"🧹 Successfully dropped {dropped_count} collections")
            self._create_indexes()
            return True
            
        except Exception as e:
            print(f"❌ Error dropping collections: {e}")
            return False

    def reset_database(self):
        """Complete database reset - drop all collections and reinitialize"""
        print("🔄 Performing complete MongoDB database reset...")
        success = self.drop_all_collections()
        if success:
            print("✅ MongoDB reset complete - ready for fresh data")
        else:
            print("⚠️ MongoDB reset had issues - check logs")
        return success