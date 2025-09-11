# ✅ FIXED: found_footy/storage/mongo_store.py - Add missing os import and complete methods
import os  # ✅ ADD: Missing import
from pymongo import MongoClient, UpdateOne
from datetime import datetime, timezone, timedelta
from typing import Dict, List, Optional

class FootyMongoStore:
    """MongoDB storage for football application data - 5 collections architecture (no teams)"""
    
    def __init__(self, connection_url=None):
        if connection_url is None:
            connection_url = os.getenv('MONGODB_URL', 'mongodb://localhost:27017/')
        
        self.client = MongoClient(connection_url)
        self.db = self.client.found_footy
        
        # ✅ CLEANED: Only 5 collections - teams removed
        self.fixtures_staging = self.db.fixtures_staging
        self.fixtures_active = self.db.fixtures_active
        self.fixtures_completed = self.db.fixtures_completed
        self.goals_pending = self.db.goals_pending
        self.goals_processed = self.db.goals_processed
        
        self._create_indexes()
    
    def _create_indexes(self):
        """Create database indexes for better query performance"""
        try:
            # Fixture indexes
            self.fixtures_staging.create_index([("fixture_id", 1)], unique=True)
            self.fixtures_staging.create_index([("kickoff_time", 1)])
            
            self.fixtures_active.create_index([("fixture_id", 1)], unique=True)
            self.fixtures_active.create_index([("status", 1)])
            
            self.fixtures_completed.create_index([("fixture_id", 1)], unique=True)
            self.fixtures_completed.create_index([("created_at", 1)])
            
            # Goal indexes
            self.goals_pending.create_index([("fixture_id", 1), ("minute", 1), ("player_id", 1)], unique=True)
            self.goals_processed.create_index([("fixture_id", 1), ("minute", 1), ("player_id", 1)], unique=True)
            
            print("✅ MongoDB indexes created successfully")
        except Exception as e:
            print(f"⚠️ Error creating indexes: {e}")

    def fixtures_advance(self, source_collection_name: str, destination_collection_name: str, fixture_id: int = None) -> dict:
        """Move fixtures between collections with consistent error handling"""
        try:
            source_collection = getattr(self, source_collection_name)
            destination_collection = getattr(self, destination_collection_name)
            
            query = {"fixture_id": fixture_id} if fixture_id else {}
            source_docs = list(source_collection.find(query))
            
            if not source_docs:
                print(f"⚠️ No documents found to advance from {source_collection_name}")
                return {"status": "success", "advanced_count": 0}
            
            # Move documents
            for doc in source_docs:
                doc["moved_at"] = datetime.now(timezone.utc)
                destination_collection.replace_one({"_id": doc["_id"]}, doc, upsert=True)
        
            # Delete from source
            source_collection.delete_many(query)
            
            print(f"📋 Advanced {len(source_docs)} fixtures: {source_collection_name} → {destination_collection_name}")
            return {"status": "success", "advanced_count": len(source_docs)}
        
        except Exception as e:
            print(f"❌ Error advancing fixtures: {e}")
            return {"status": "error", "advanced_count": 0, "error": str(e)}

    def bulk_insert_fixtures(self, fixtures_data: List[dict], collection_name: str) -> int:
        """Universal bulk insert fixtures into any collection"""
        if not fixtures_data:
            return 0
        
        try:
            collection_map = {
                "fixtures_staging": self.fixtures_staging,
                "fixtures_active": self.fixtures_active,
                "fixtures_completed": self.fixtures_completed
            }
            
            target_collection = collection_map.get(collection_name)
            if target_collection is None:
                raise ValueError(f"Unknown collection: {collection_name}")
            
            print(f"💾 Bulk inserting {len(fixtures_data)} fixtures into {collection_name}...")
            
            documents = []
            for fixture in fixtures_data:
                document = {
                    "_id": fixture["id"],
                    "fixture_id": fixture["id"],
                    "home_team_id": fixture["home_id"],
                    "away_team_id": fixture["away_id"],
                    "team_names": {
                        "home": fixture["home"],
                        "away": fixture["away"]
                    },
                    "teams": {
                        "home": fixture["home_id"],
                        "away": fixture["away_id"]
                    },
                    "league": fixture["league"],
                    "league_id": fixture["league_id"],
                    "kickoff_time": datetime.fromisoformat(fixture["time"].replace('Z', '+00:00')),
                    "raw_fixture_data": fixture,
                    "status": fixture.get("status", "NS"),
                    "goals": fixture.get("current_goals", {"home": 0, "away": 0}),
                    "last_checked": datetime.now(timezone.utc),
                    "created_at": datetime.now(timezone.utc)
                }
                documents.append(document)
    
            bulk_operations = [
                UpdateOne(
                    {"fixture_id": doc["fixture_id"]},
                    {"$set": doc},
                    upsert=True
                ) for doc in documents
            ]
            
            result = target_collection.bulk_write(bulk_operations)
            inserted_count = result.upserted_count + result.modified_count
            
            print(f"✅ Bulk insert {collection_name} complete: {inserted_count} fixtures")
            return inserted_count
            
        except Exception as e:
            print(f"❌ Error bulk inserting into {collection_name}: {e}")
            return 0

    def get_all_active_fixtures(self) -> List[dict]:
        """Get all fixtures from active collection"""
        try:
            return list(self.fixtures_active.find({}))
        except Exception as e:
            print(f"❌ Error getting active fixtures: {e}")
            return []

    def store_goal_pending(self, fixture_id: int, goal_data: dict) -> bool:
        """Store goal in goals_pending collection with enhanced validation"""
        try:
            minute = goal_data.get("time", {}).get("elapsed", 0)
            player_id = goal_data.get("player", {}).get("id", 0)
            player_name = goal_data.get("player", {}).get("name", "")
            team_name = goal_data.get("team", {}).get("name", "")
            
            # Enhanced validation
            if not player_name or not team_name or minute <= 0 or player_id <= 0:
                print(f"⚠️ Skipping goal - incomplete data for fixture {fixture_id}")
                return False
            
            goal_id = f"{fixture_id}_{minute}_{player_id}"
            
            # Prevent duplicates
            if (self.goals_pending.find_one({"_id": goal_id}) or 
                self.goals_processed.find_one({"_id": goal_id})):
                print(f"⚠️ Goal {goal_id} already exists - skipping")
                return False
            
            # Store goal with complete data
            assist_name = goal_data.get("assist", {}).get("name", "")
            assist_id = goal_data.get("assist", {}).get("id", 0) if goal_data.get("assist") else None
            
            document = {
                "_id": goal_id,
                "fixture_id": fixture_id,
                "minute": minute,
                "minute_extra": goal_data.get("time", {}).get("extra"),
                "team_id": goal_data.get("team", {}).get("id"),
                "team_name": team_name,
                "player_id": player_id,
                "player_name": player_name,
                "assist_id": assist_id,
                "assist_name": assist_name if assist_name else None,
                "goal_type": goal_data.get("detail", "Goal"),
                "raw_goal_data": goal_data,
                "created_at": datetime.now(timezone.utc),
                "status": "pending_twitter",
                "data_quality": "complete"
            }
            
            self.goals_pending.insert_one(document)
            
            print(f"✅ Stored COMPLETE goal: {team_name} - {player_name} ({minute}') [{goal_id}]")
            if assist_name:
                print(f"   🎯 Assist: {assist_name}")
        
            return True
            
        except Exception as e:
            print(f"❌ Error storing goal: {e}")
            return False

    def check_collections_empty(self, collection_names: List[str]) -> bool:
        """Check if specified collections are empty"""
        try:
            for collection_name in collection_names:
                collection = getattr(self, collection_name)
                if collection.count_documents({}) > 0:
                    return False
        
            print(f"✅ All specified collections are empty: {collection_names}")
            return True
            
        except Exception as e:
            print(f"❌ Error checking collections: {e}")
            return False

    def fixtures_delta(self, fixture_id: int, api_data: dict) -> dict:
        """Pure comparison with centralized status logic"""
        try:
            current_fixture = self.fixtures_active.find_one({"fixture_id": fixture_id})
            if not current_fixture:
                print(f"⚠️ Fixture {fixture_id} not found in active collection")
                return {"status": "not_found", "goals_changed": False}
            
            # Extract current API state
            goals_data = api_data.get("goals", {"home": 0, "away": 0})
            status = api_data.get("fixture", {}).get("status", {}).get("short", "UNKNOWN")
            
            current_home = goals_data.get("home") or 0
            current_away = goals_data.get("away") or 0
            
            # Get previous state
            previous_home = current_fixture.get("goals", {}).get("home", 0)
            previous_away = current_fixture.get("goals", {}).get("away", 0)
            
            # Delta detection
            goals_changed = current_home > previous_home or current_away > previous_away
            
            # Use centralized status logic
            try:
                from found_footy.utils.fixture_status import is_fixture_completed
                fixture_completed = is_fixture_completed(status)
            except Exception as e:
                print(f"⚠️ Could not load status logic: {e}")
                fixture_completed = status in {"FT", "AET", "PEN", "PST", "CANC", "ABD", "AWD", "WO"}
            
            return {
                "status": "success",
                "goals_changed": goals_changed,
                "fixture_completed": fixture_completed,
                "total_goal_increase": (current_home - previous_home) + (current_away - previous_away),
                "current_goals": {"home": current_home, "away": current_away},
                "api_status": status,
                "fixture_id": fixture_id,
                "completion_reason": f"status_{status}"
            }
            
        except Exception as e:
            print(f"❌ Error in fixtures_delta for fixture {fixture_id}: {e}")
            return {"status": "error", "goals_changed": False, "error": str(e)}

    def fixtures_update(self, fixture_id: int, delta_result: dict) -> bool:
        """Update fixture with latest API data"""
        try:
            if delta_result["status"] != "success":
                return False
            
            update_data = {
                "goals": delta_result["current_goals"],
                "status": delta_result["api_status"],
                "last_checked": datetime.now(timezone.utc)
            }
        
            result = self.fixtures_active.update_one(
                {"fixture_id": fixture_id},
                {"$set": update_data}
            )
        
            return result.modified_count > 0
        
        except Exception as e:
            print(f"❌ Error updating fixture {fixture_id}: {e}")
            return False

    # ✅ LEGACY: Keep for backward compatibility
    def store_goal_active(self, fixture_id: int, goal_data: dict) -> bool:
        """Legacy method - calls store_goal_pending"""
        return self.store_goal_pending(fixture_id, goal_data)
