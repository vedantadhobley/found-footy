import os  # ✅ ADD: Missing import at the top
from pymongo import MongoClient, UpdateOne, ASCENDING
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
        """Indexes on raw schema + convenience duplicate 'fixture_id' field"""
        try:
            # ✅ FIX: Don't create _id indexes - MongoDB does this automatically
            # Unique by the nested fixture id (raw) and by duplicate field
            self.fixtures_staging.create_index([("fixture.id", ASCENDING)], unique=True)
            self.fixtures_active.create_index([("fixture.id", ASCENDING)], unique=True)
            self.fixtures_completed.create_index([("fixture.id", ASCENDING)], unique=True)

            self.fixtures_staging.create_index([("fixture.status.short", ASCENDING)])
            self.fixtures_active.create_index([("fixture.status.short", ASCENDING)])
            self.fixtures_completed.create_index([("fixture.status.short", ASCENDING)])

            # Duplicate flat key for easy lookups
            self.fixtures_staging.create_index([("fixture_id", ASCENDING)])
            self.fixtures_active.create_index([("fixture_id", ASCENDING)])
            self.fixtures_completed.create_index([("fixture_id", ASCENDING)])

            # ✅ FIX: Goals collections - only create field indexes, not _id
            self.goals_pending.create_index([("fixture_id", ASCENDING)])
            self.goals_processed.create_index([("fixture_id", ASCENDING)])
            self.goals_pending.create_index([("player_id", ASCENDING)])
            self.goals_processed.create_index([("player_id", ASCENDING)])
            
            print("✅ MongoDB indexes created successfully")
        except Exception as e:
            print(f"⚠️ Error creating indexes: {e}")

    @staticmethod
    def _extract_fixture_id(raw_item: dict) -> int:
        """Safely extract fixture.id from raw API schema; fallback to legacy keys."""
        try:
            return int(raw_item["fixture"]["id"])
        except Exception:
            return int(raw_item.get("fixture_id") or raw_item.get("id") or 0)

    @staticmethod
    def _extract_team_names(raw_item: dict) -> tuple:
        """Extract team names from raw API schema"""
        try:
            home_name = raw_item["teams"]["home"]["name"]
            away_name = raw_item["teams"]["away"]["name"]
            return home_name, away_name
        except Exception:
            # Fallback to flat schema if raw doesn't work
            return raw_item.get("home", "Home"), raw_item.get("away", "Away")

    @staticmethod
    def _extract_current_goals(raw_item: dict) -> dict:
        """Extract goals from raw API schema"""
        try:
            return {
                "home": raw_item["goals"]["home"] or 0,
                "away": raw_item["goals"]["away"] or 0
            }
        except Exception:
            # Fallback
            return {"home": 0, "away": 0}

    @staticmethod
    def _extract_status(raw_item: dict) -> str:
        """Extract status from raw API schema"""
        try:
            return raw_item["fixture"]["status"]["short"]
        except Exception:
            return raw_item.get("status", "NS")

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

    def bulk_insert_fixtures(self, fixtures_data: list[dict], collection_name: str) -> int:
        """
        Upsert raw API-Football items into the chosen collection without reshaping.
        Adds a duplicate 'fixture_id' = item.fixture.id for convenience.
        """
        if not fixtures_data:
            return 0

        collection_map = {
            "fixtures_staging": self.fixtures_staging,
            "fixtures_active": self.fixtures_active,
            "fixtures_completed": self.fixtures_completed,
        }
        target = collection_map.get(collection_name)
        if target is None:
            print(f"⚠️ Unknown collection: {collection_name}")
            return 0

        ops = []
        for item in fixtures_data:
            fid = self._extract_fixture_id(item)
            if fid <= 0:
                continue
            # Store raw + convenience fields
            doc = dict(item)
            doc["fixture_id"] = fid
            ops.append(
                UpdateOne(
                    {"fixture.id": fid},
                    {"$set": doc},
                    upsert=True,
                )
            )

        if not ops:
            return 0

        try:
            result = target.bulk_write(ops, ordered=False)
            return (result.upserted_count or 0) + (result.modified_count or 0)
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
        """Pure comparison with centralized status logic using raw schema"""
        try:
            # Get current fixture data
            current_fixture = self.fixtures_active.find_one({"fixture_id": fixture_id})
            if not current_fixture:
                return {"status": "not_found", "fixture_id": fixture_id}
            
            # Extract API data using raw schema
            api_status = self._extract_status(api_data)
            api_goals = self._extract_current_goals(api_data)
            
            # Current goals from stored data
            current_goals = self._extract_current_goals(current_fixture)
            
            # Check for changes
            goals_changed = (api_goals.get("home", 0) != current_goals.get("home", 0) or 
                           api_goals.get("away", 0) != current_goals.get("away", 0))
            
            status_changed_to_completed = (api_status in ["FT", "AET", "PEN"] and 
                                         self._extract_status(current_fixture) not in ["FT", "AET", "PEN"])
            
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
            # Store the complete raw API data, don't modify nested structure
            update_data = {
                "$set": {
                    "last_updated": datetime.now(timezone.utc)
                }
            }
            
            result = self.fixtures_active.update_one(
                {"fixture_id": fixture_id},
                update_data
            )
            
            return result.modified_count > 0
        
        except Exception as e:
            print(f"❌ Error updating fixture {fixture_id}: {e}")
            return False

    def store_goal_pending(self, fixture_id: int, goal_data: dict) -> bool:
        """Store goal in goals_pending collection with enhanced validation"""
        try:
            # Extract from raw goal event schema
            minute = goal_data.get("time", {}).get("elapsed", 0)
            player_id = goal_data.get("player", {}).get("id", 0)
            player_name = goal_data.get("player", {}).get("name", "Unknown")
            team_name = goal_data.get("team", {}).get("name", "Unknown")

            goal_id = f"{fixture_id}_{minute}_{player_id}"
            
            goal_doc = {
                "_id": goal_id,
                "fixture_id": fixture_id,
                "minute": minute,
                "player_id": player_id,
                "player_name": player_name,
                "team_name": team_name,
                "raw_event": goal_data,  # Store complete raw event
                "created_at": datetime.now(timezone.utc),
                "status": "pending_twitter_search"
            }
            
            self.goals_pending.replace_one({"_id": goal_id}, goal_doc, upsert=True)
            print(f"✅ Stored goal: {team_name} - {player_name} ({minute}')")
            return True
        
        except Exception as e:
            print(f"❌ Error storing goal: {e}")
            return False

    # ✅ LEGACY: Keep for backward compatibility
    def store_goal_active(self, fixture_id: int, goal_data: dict) -> bool:
        """Legacy method - calls store_goal_pending"""
        return self.store_goal_pending(fixture_id, goal_data)

    def get_active_fixtures(self) -> List[Dict]:
        """Get all fixtures from fixtures_active collection"""
        try:
            return list(self.fixtures_active.find({}))
        except Exception as e:
            print(f"❌ Error getting active fixtures: {e}")
            return []
