import os  # ✅ ADD: Missing import at the top
from pymongo import MongoClient, UpdateOne
from datetime import datetime, timezone, timedelta
from typing import Dict, List, Optional

class FootyMongoStore:
    """MongoDB storage for football application data - 5 collections architecture"""
    
    def __init__(self, connection_url=None):
        if connection_url is None:
            connection_url = os.getenv('MONGODB_URL', 'mongodb://localhost:27017/')
        
        self.client = MongoClient(connection_url)
        self.db = self.client.found_footy
        
        # Collections
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
                return {"status": "success", "advanced_count": 0}  # ✅ FIX: Complete the line
            
            # Move documents
            for doc in source_docs:
                destination_collection.replace_one({"fixture_id": doc["fixture_id"]}, doc, upsert=True)  # ✅ FIX: Complete the line
        
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
                raise ValueError(f"Unknown collection: {collection_name}")  # ✅ FIX: Complete the line
            
            print(f"💾 Bulk inserting {len(fixtures_data)} fixtures into {collection_name}...")
            
            documents = []
            for fixture in fixtures_data:
                # Add common fields
                fixture["created_at"] = datetime.now(timezone.utc)  # ✅ FIX: Complete the line
                fixture["fixture_id"] = fixture["id"]
                documents.append(fixture)
    
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

    def check_collections_empty(self, collection_names: List[str]) -> bool:
        """Check if specified collections are empty"""
        try:
            for collection_name in collection_names:
                collection = getattr(self, collection_name)  # ✅ FIX: Complete the line
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
            # Get current fixture data
            current_fixture = self.fixtures_active.find_one({"fixture_id": fixture_id})
            if not current_fixture:
                return {"status": "fixture_not_found"}
            
            # Extract API data
            api_status = api_data.get("fixture", {}).get("status", {}).get("short", "NS")
            api_goals = api_data.get("goals", {"home": 0, "away": 0})
            
            # Current goals
            current_goals = current_fixture.get("current_goals", {"home": 0, "away": 0})
            
            # Check for changes
            goals_changed = (api_goals.get("home", 0) != current_goals.get("home", 0) or 
                           api_goals.get("away", 0) != current_goals.get("away", 0))
            
            status_changed_to_completed = (api_status in ["FT", "AET", "PEN"] and 
                                         current_fixture.get("status") not in ["FT", "AET", "PEN"])
            
            return {
                "goals_changed": goals_changed,
                "status_changed_to_completed": status_changed_to_completed,
                "current_goals": api_goals,
                "previous_goals": current_goals,
                "total_goal_increase": (api_goals.get("home", 0) + api_goals.get("away", 0)) - 
                                     (current_goals.get("home", 0) + current_goals.get("away", 0)),
                "new_status": api_status
            }
            
        except Exception as e:
            print(f"❌ Error in fixtures delta: {e}")
            return {"status": "error", "error": str(e)}

    def fixtures_update(self, fixture_id: int, delta_result: dict) -> bool:
        """Update fixture with latest API data"""
        try:
            update_data = {
                "current_goals": delta_result.get("current_goals", {}),
                "status": delta_result.get("new_status", "NS"),
                "last_updated": datetime.now(timezone.utc)
            }
            
            result = self.fixtures_active.update_one(
                {"fixture_id": fixture_id},
                {"$set": update_data}
            )
            
            return result.modified_count > 0
        
        except Exception as e:
            print(f"❌ Error updating fixture {fixture_id}: {e}")
            return False

    def store_goal_pending(self, fixture_id: int, goal_data: dict) -> bool:
        """Store goal in goals_pending collection with enhanced validation"""
        try:
            minute = goal_data.get("time", {}).get("elapsed", 0)
            player_id = goal_data.get("player", {}).get("id", 0)
            player_name = goal_data.get("player", {}).get("name", "")
            team_name = goal_data.get("team", {}).get("name", "")
            
            # Enhanced validation
            if not player_name or not team_name or minute <= 0 or player_id <= 0:
                print(f"⚠️ Invalid goal data: {goal_data}")  # ✅ FIX: Complete the line
                return False
            
            goal_id = f"{fixture_id}_{minute}_{player_id}"
            
            # Prevent duplicates
            if (self.goals_pending.find_one({"_id": goal_id}) or 
                self.goals_processed.find_one({"_id": goal_id})):
                print(f"⚠️ Duplicate goal: {goal_id}")  # ✅ FIX: Complete the line
                return False
            
            # Store goal with complete data
            assist_name = goal_data.get("assist", {}).get("name", "")
            assist_id = goal_data.get("assist", {}).get("id", 0) if goal_data.get("assist") else None

            goal_doc = {  # ✅ FIX: Complete the line
                "_id": goal_id,
                "fixture_id": fixture_id,
                "minute": minute,
                "player_id": player_id,
                "player_name": player_name,
                "team_name": team_name,
                "assist_name": assist_name,
                "assist_id": assist_id,
                "goal_type": goal_data.get("detail", "Unknown"),
                "created_at": datetime.now(timezone.utc),
                "status": "pending_video_search"
            }
            
            self.goals_pending.insert_one(goal_doc)
            print(f"✅ Stored goal: {team_name} - {player_name} ({minute}')")
            return True
        
        except Exception as e:
            print(f"❌ Error storing goal: {e}")  # ✅ FIX: Complete the line
            return False

    # ✅ LEGACY: Keep for backward compatibility
    def store_goal_active(self, fixture_id: int, goal_data: dict) -> bool:
        """Legacy method - calls store_goal_pending"""
        return self.store_goal_pending(fixture_id, goal_data)

    def get_active_fixtures(self) -> List[Dict]:
        """Get all fixtures from fixtures_active collection"""
        try:
            return list(self.fixtures_active.find({}))  # ✅ FIX: Complete the line
        except Exception as e:
            print(f"❌ Error getting active fixtures: {e}")
            return []
