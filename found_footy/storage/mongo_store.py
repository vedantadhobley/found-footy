"""MongoDB storage with immediate connection - clean and simple"""
import os
from pymongo import MongoClient, UpdateOne, ASCENDING
from datetime import datetime, timezone, timedelta
from typing import Dict, List, Optional

# ✅ FIXED: Use proper logging
from found_footy.utils.logging import get_logger, log_error_with_trace

class FootyMongoStore:
    """MongoDB storage with immediate connection and proper error handling"""
    
    def __init__(self, connection_url=None):
        self.logger = get_logger(self.__class__.__name__)
        
        if connection_url is None:
            connection_url = os.getenv('MONGODB_URL', 'mongodb://localhost:27017/')
        
        self.connection_url = connection_url
        
        # ✅ IMMEDIATE CONNECTION - No lazy loading
        try:
            self.logger.info(f"🔗 Connecting to MongoDB: {self.connection_url}")
            self._client = MongoClient(self.connection_url)
            self._db = self._client.found_footy
            
            # Set up collection references immediately
            self.fixtures_staging = self._db.fixtures_staging
            self.fixtures_active = self._db.fixtures_active
            self.fixtures_completed = self._db.fixtures_completed
            self.goals_pending = self._db.goals_pending
            self.goals_processed = self._db.goals_processed
            
            # Create indexes immediately
            self._create_indexes()
            
            self.logger.info("✅ MongoDB connection established")
            
        except Exception as e:
            log_error_with_trace(self.logger, "❌ MongoDB connection failed", e)
            raise
    
    def _create_indexes(self):
        """Create indexes immediately on connection"""
        try:
            self.logger.info("📋 Creating MongoDB indexes...")
            
            # Unique by the nested fixture id and by duplicate field
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

            # Goals collections
            self.goals_pending.create_index([("fixture_id", ASCENDING)])
            self.goals_processed.create_index([("fixture_id", ASCENDING)])
            self.goals_pending.create_index([("player_id", ASCENDING)])
            self.goals_processed.create_index([("player_id", ASCENDING)])
            
            self.logger.info("✅ MongoDB indexes created successfully")
        except Exception as e:
            log_error_with_trace(self.logger, "⚠️ Error creating indexes", e)

    # ✅ ALL METHODS: Now just use the collections directly (no _ensure_connection)
    def bulk_insert_fixtures(self, fixtures_data: list[dict], collection_name: str) -> int:
        """Store PURE API schema + minimal fixture_id only"""
        if not fixtures_data:
            return 0

        collection_map = {
            "fixtures_staging": self.fixtures_staging,
            "fixtures_active": self.fixtures_active,
            "fixtures_completed": self.fixtures_completed,
        }
        target = collection_map.get(collection_name)
        if target is None:
            return 0

        ops = []
        for item in fixtures_data:
            fid = self._extract_fixture_id(item)
            if fid <= 0:
                continue
            
            # ✅ CLEAN: Store exact API data + only fixture_id convenience field
            doc = dict(item)  # Copy exact API structure
            doc["fixture_id"] = fid  # Add ONLY this convenience field
            # ✅ REMOVED: All other random fields
            
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
            self.logger.error(f"❌ Error bulk inserting into {collection_name}: {e}")
            return 0

    def get_active_fixtures(self) -> List[Dict]:
        """Get all fixtures from fixtures_active collection"""
        try:
            return list(self.fixtures_active.find())
        except Exception as e:
            self.logger.error(f"❌ Error getting active fixtures: {e}")
            return []

    def check_collections_empty(self, collection_names: List[str]) -> bool:
        """Check if specified collections are empty - FIXED"""
        try:
            for collection_name in collection_names:
                collection = getattr(self, collection_name, None)
                # ✅ FIX: Don't use bool() on collection objects
                if collection is not None and collection.count_documents({}) > 0:
                    return False
            return True
        except Exception as e:
            log_error_with_trace(self.logger, "❌ Error checking collections", e)
            return True

    def store_goal_pending(self, fixture_id: int, goal_data: dict) -> bool:
        """Store goal with new minute-based ID system - BOOLEAN RETURNS"""
        try:
            minute = goal_data.get("time", {}).get("elapsed", 0)
            extra_time = goal_data.get("time", {}).get("extra") or 0
            
            # ✅ NEW: Minute-based goal ID with added time
            if extra_time > 0:
                goal_id = f"{fixture_id}_{minute}+{extra_time}"
            else:
                goal_id = f"{fixture_id}_{minute}"
            
            # ✅ Check for existing goal (this will UPDATE existing instead of creating duplicates)
            existing = self.goals_pending.find_one({"_id": goal_id})
            
            # ✅ MINIMAL SCHEMA: Just the essential fields
            goal_doc = {
                "_id": goal_id,
                "fixture_id": fixture_id,
                "minute": minute,
                "extra_time": extra_time,
                "player_id": goal_data.get("player", {}).get("id", 0),
                "player_name": goal_data.get("player", {}).get("name", "Unknown"),
                "team_id": goal_data.get("team", {}).get("id", 0),
                "team_name": goal_data.get("team", {}).get("name", "Unknown"),
                "assist_id": goal_data.get("assist", {}).get("id"),
                "assist_name": goal_data.get("assist", {}).get("name"),
                "raw_event": goal_data,
                "created_at": datetime.now(timezone.utc) if not existing else existing.get("created_at"),
                "updated_at": datetime.now(timezone.utc)
            }
            
            # ✅ Always upsert (insert or update)
            self.goals_pending.replace_one({"_id": goal_id}, goal_doc, upsert=True)
            
            if existing:
                self.logger.info(f"🔄 Updated goal: {goal_doc['team_name']} - {goal_doc['player_name']} ({minute}')")
                return False  # ✅ BOOLEAN: False = Updated existing goal
            else:
                self.logger.info(f"✅ Created goal: {goal_doc['team_name']} - {goal_doc['player_name']} ({minute}')")
                return True   # ✅ BOOLEAN: True = Created new goal
            
        except Exception as e:
            log_error_with_trace(self.logger, "❌ Error storing goal", e)
            return False  # ✅ BOOLEAN: False = Error occurred

    def fixtures_delta(self, fixture_id: int, api_data: dict) -> dict:
        """Pure comparison with centralized status logic using raw schema"""
        try:
            stored_fixture = self.fixtures_active.find_one({"fixture_id": fixture_id})
            if not stored_fixture:
                return {"error": "fixture_not_found"}
            
            # Compare goals using raw schema
            stored_goals = self._extract_current_goals(stored_fixture)
            current_goals = self._extract_current_goals(api_data)
            
            goals_changed = stored_goals != current_goals
            
            if goals_changed:
                total_stored = stored_goals.get("home", 0) + stored_goals.get("away", 0)
                total_current = current_goals.get("home", 0) + current_goals.get("away", 0)
                total_goal_increase = total_current - total_stored
            else:
                total_goal_increase = 0
            
            # Check status change
            stored_status = self._extract_status(stored_fixture)
            current_status = self._extract_status(api_data)
            status_changed = stored_status != current_status
            
            return {
                "goals_changed": goals_changed,
                "status_changed": status_changed,
                "current_goals": current_goals,
                "previous_goals": stored_goals,
                "total_goal_increase": total_goal_increase,
                "current_status": current_status,
                "previous_status": stored_status
            }
            
        except Exception as e:
            log_error_with_trace(self.logger, "❌ Error in fixtures_delta", e)
            return {"error": str(e)}

    def fixtures_update(self, fixture_id: int, delta_result: dict) -> bool:
        """Update fixture with latest API data"""
        try:
            if delta_result.get("goals_changed") or delta_result.get("status_changed"):
                update_data = {
                    "goals": delta_result.get("current_goals", {}),
                    "status": delta_result.get("current_status"),
                    "last_updated": datetime.now(timezone.utc)
                }
                
                result = self.fixtures_active.update_one(
                    {"fixture_id": fixture_id},
                    {"$set": update_data}
                )
                
                if result.modified_count > 0:
                    self.logger.info(f"✅ Updated fixture {fixture_id}")
                    return True
                
            return False
        except Exception as e:
            log_error_with_trace(self.logger, "❌ Error updating fixture", e)
            return False

    def fixtures_advance(self, source_collection_name: str, destination_collection_name: str, fixture_id: int = None) -> dict:
        """Move fixtures between collections with consistent error handling"""
        try:
            source_collection = getattr(self, source_collection_name)
            destination_collection = getattr(self, destination_collection_name)
            
            if fixture_id:
                # Move specific fixture
                fixture = source_collection.find_one({"fixture_id": fixture_id})
                if not fixture:
                    return {"status": "not_found", "advanced_count": 0}
                
                # Insert into destination
                destination_collection.replace_one(
                    {"fixture_id": fixture_id},
                    fixture,
                    upsert=True
                )
                
                # Remove from source
                source_collection.delete_one({"fixture_id": fixture_id})
                
                return {"status": "success", "advanced_count": 1}
            else:
                # Move all fixtures
                fixtures = list(source_collection.find())
                if not fixtures:
                    return {"status": "success", "advanced_count": 0}
                
                # Bulk operations
                if fixtures:
                    destination_collection.insert_many(fixtures)
                    source_collection.delete_many({})
                
                return {"status": "success", "advanced_count": len(fixtures)}
                
        except Exception as e:
            log_error_with_trace(self.logger, "❌ Error in fixtures_advance", e)
            return {"status": "error", "error": str(e), "advanced_count": 0}

    # ✅ Static methods don't need connection
    @staticmethod
    def _extract_fixture_id(raw_item: dict) -> int:
        try:
            return int(raw_item["fixture"]["id"])
        except (KeyError, TypeError, ValueError):
            return 0

    @staticmethod
    def _extract_team_names(raw_item: dict) -> tuple:
        try:
            home_team = raw_item["teams"]["home"]["name"]
            away_team = raw_item["teams"]["away"]["name"]
            return home_team, away_team
        except (KeyError, TypeError):
            return "Unknown", "Unknown"

    @staticmethod
    def _extract_current_goals(raw_item: dict) -> dict:
        try:
            return {
                "home": int(raw_item["goals"]["home"] or 0),
                "away": int(raw_item["goals"]["away"] or 0)
            }
        except (KeyError, TypeError, ValueError):
            return {"home": 0, "away": 0}

    @staticmethod
    def _extract_status(raw_item: dict) -> str:
        try:
            return raw_item["fixture"]["status"]["short"]
        except (KeyError, TypeError):
            return "NS"
