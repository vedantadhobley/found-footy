from pymongo import MongoClient
from datetime import datetime, timezone
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
        self.teams = self.db.teams
        self.youtube_results = self.db.youtube_results
        
        # Create indexes for performance
        self._create_indexes()
    
    def _create_indexes(self):
        """Create database indexes for better query performance"""
        try:
            # ✅ No need for fixture_id index since it's now _id
            # Fixture indexes
            self.fixtures.create_index([("status", 1)])
            self.fixtures.create_index([("fixture_date", 1)])
            self.fixtures.create_index([("home_team_name", 1)])
            self.fixtures.create_index([("away_team_name", 1)])
            self.fixtures.create_index([("league_id", 1)])
            
            # Event indexes
            self.events.create_index([("fixture_id", 1)])
            self.events.create_index([("event_type", 1)])
            
            # ✅ No need for team_id index since it's now _id
            # Team indexes  
            self.teams.create_index([("league_id", 1)])
            self.teams.create_index([("season", 1)])
            
            print("✅ MongoDB indexes created successfully")
        except Exception as e:
            print(f"⚠️ Error creating indexes: {e}")
    
    def store_fixture(self, fixture_data: dict) -> bool:
        """Store fixture data with fixture_id as _id"""
        try:
            fixture_info = fixture_data["fixture"]
            teams_info = fixture_data["teams"]
            goals_info = fixture_data["goals"]
            league_info = fixture_data["league"]
            
            # ✅ Use fixture_id as MongoDB _id
            document = {
                "_id": fixture_info["id"],  # ✅ Custom _id instead of fixture_id field
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
                
                # Store complete API response for future use
                "raw_api_data": fixture_data,
                
                # Metadata
                "stored_at": datetime.now(timezone.utc),
                "updated_at": datetime.now(timezone.utc)
            }
            
            # ✅ Use _id for upsert
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
                        "_id": event_id,  # ✅ Custom composite _id
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
                    
                    # ✅ Use _id for upsert
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
    
    def get_completed_fixtures(self, date_filter=None) -> List[dict]:
        """Get completed fixtures, optionally filtered by date"""
        try:
            query = {"status": {"$in": ["FT", "AET", "PEN"]}}
            
            if date_filter:
                query["fixture_date"] = {
                    "$gte": datetime.combine(date_filter, datetime.min.time()),
                    "$lt": datetime.combine(date_filter, datetime.max.time())
                }
            
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
    
    def store_teams(self, teams_data: list, season: int) -> bool:
        """Store teams data with team_id as _id"""
        try:
            for team_data in teams_data:
                # ✅ Create composite _id for teams (team_id + season)
                team_doc_id = f"{team_data['team_id']}_{season}"
                
                document = {
                    "_id": team_doc_id,  # ✅ Custom composite _id
                    "team_id": team_data["team_id"],  # Keep for backward compatibility
                    "team_name": team_data["team_name"],
                    "league_id": team_data["league_id"],
                    "league_name": team_data["league_name"],
                    "season": season,
                    "stored_at": datetime.now(timezone.utc)
                }
                
                self.teams.replace_one(
                    {"_id": team_doc_id}, 
                    document, 
                    upsert=True
                )
            
            print(f"✅ Stored {len(teams_data)} teams for season {season}")
            return True
            
        except Exception as e:
            print(f"❌ Error storing teams: {e}")
            return False
    
    def get_team_ids(self, season: int) -> set:
        """Get all team IDs for a season"""
        try:
            teams = self.teams.find({"season": season}, {"team_id": 1})
            return {team["team_id"] for team in teams}
        except Exception as e:
            print(f"❌ Error getting team IDs: {e}")
            return set()
    
    def store_youtube_result(self, fixture_id: int, youtube_data: dict) -> bool:
        """Store YouTube search results with fixture_id as _id"""
        try:
            document = {
                "_id": fixture_id,  # ✅ Use fixture_id as _id
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
                "teams_count": self.teams.count_documents({}),
                "youtube_results_count": self.youtube_results.count_documents({}),
                "completed_fixtures": self.fixtures.count_documents({"status": {"$in": ["FT", "AET", "PEN"]}}),
                "goal_events": self.events.count_documents({"event_type": "Goal"})
            }
        except Exception as e:
            print(f"❌ Error getting stats: {e}")
            return {}