import os
from pymongo import MongoClient, UpdateOne, ASCENDING
from datetime import datetime, timezone, timedelta
from typing import Dict, List, Optional

class FootyMongoStore:
    """MongoDB storage for football application data - Raw API schema only"""
    
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
        """Create indexes - _id will be fixture.id"""
        try:
            # ✅ _id indexes are automatic, but we can create indexes on nested fields
            # Status indexes for workflow routing
            self.fixtures_staging.create_index([("fixture.status.short", ASCENDING)])
            self.fixtures_active.create_index([("fixture.status.short", ASCENDING)])
            self.fixtures_completed.create_index([("fixture.status.short", ASCENDING)])
            
            # Date indexes for time-based queries
            self.fixtures_staging.create_index([("fixture.date", ASCENDING)])
            self.fixtures_active.create_index([("fixture.date", ASCENDING)])
            self.fixtures_completed.create_index([("fixture.date", ASCENDING)])

            # Team indexes for filtering
            self.fixtures_staging.create_index([("teams.home.id", ASCENDING)])
            self.fixtures_staging.create_index([("teams.away.id", ASCENDING)])
            self.fixtures_active.create_index([("teams.home.id", ASCENDING)])
            self.fixtures_active.create_index([("teams.away.id", ASCENDING)])
            self.fixtures_completed.create_index([("teams.home.id", ASCENDING)])
            self.fixtures_completed.create_index([("teams.away.id", ASCENDING)])

            # Goals collections
            self.goals_pending.create_index([("fixture_id", ASCENDING)])
            self.goals_processed.create_index([("fixture_id", ASCENDING)])
            self.goals_pending.create_index([("player_id", ASCENDING)])
            self.goals_processed.create_index([("player_id", ASCENDING)])
            
            print("✅ MongoDB indexes created successfully")
        except Exception as e:
            print(f"⚠️ Error creating indexes: {e}")

    @staticmethod
    def _extract_fixture_id(raw_item: dict) -> int:
        """Extract fixture.id from raw API schema"""
        try:
            return int(raw_item["fixture"]["id"])
        except Exception:
            return 0

    @staticmethod
    def _extract_team_names(raw_item: dict) -> tuple:
        """Extract team names from raw API schema"""
        try:
            home_name = raw_item["teams"]["home"]["name"]
            away_name = raw_item["teams"]["away"]["name"]
            return home_name, away_name
        except Exception:
            return "Unknown", "Unknown"

    @staticmethod
    def _extract_current_goals(raw_item: dict) -> dict:
        """Extract goals from raw API schema"""
        try:
            return {
                "home": raw_item["goals"]["home"] or 0,
                "away": raw_item["goals"]["away"] or 0
            }
        except Exception:
            return {"home": 0, "away": 0}

    @staticmethod
    def _extract_status(raw_item: dict) -> str:
        """Extract status from raw API schema"""
        try:
            return raw_item["fixture"]["status"]["short"]
        except Exception:
            return "NS"

    def fixtures_advance(self, source_collection_name: str, destination_collection_name: str, fixture_id: int = None) -> dict:
        """Move fixtures between collections"""
        try:
            source_collection = getattr(self, source_collection_name)
            destination_collection = getattr(self, destination_collection_name)
            
            # ✅ Query by _id (which is fixture.id)
            query = {"_id": fixture_id} if fixture_id else {}
            source_docs = list(source_collection.find(query))
            
            if not source_docs:
                return {"status": "success", "advanced_count": 0}
            
            # Move documents - _id will be preserved
            for doc in source_docs:
                destination_collection.replace_one({"_id": doc["_id"]}, doc, upsert=True)
        
            # Delete from source
            source_collection.delete_many(query)
            
            print(f"📋 Advanced {len(source_docs)} fixtures: {source_collection_name} → {destination_collection_name}")
            return {"status": "success", "advanced_count": len(source_docs)}
        
        except Exception as e:
            print(f"❌ Error advancing fixtures: {e}")
            return {"status": "error", "advanced_count": 0, "error": str(e)}

    def bulk_insert_fixtures(self, fixtures_data: list[dict], collection_name: str) -> int:
        """Store raw API-Football items exactly as they come from API."""
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

        processed_count = 0
        for item in fixtures_data:
            fixture_id = self._extract_fixture_id(item)
            if fixture_id <= 0:
                continue
            
            # ✅ Store exactly as from API, use fixture.id as _id
            doc = dict(item)  # Raw API data only
            doc["_id"] = fixture_id  # Set _id to fixture.id
            
            try:
                # ✅ Complete replacement - removes all extra fields
                result = target.replace_one(
                    {"_id": fixture_id},
                    doc,  # Complete document replacement
                    upsert=True
                )
                if result.upserted_id or result.modified_count > 0:
                    processed_count += 1
                    
            except Exception as e:
                print(f"❌ Error storing fixture {fixture_id}: {e}")
                continue

        return processed_count

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
        """Compare current fixture with API data"""
        try:
            # ✅ Query by _id (which is fixture.id)
            current_fixture = self.fixtures_active.find_one({"_id": fixture_id})
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

    def fixtures_update(self, fixture_id: int, api_data: dict) -> bool:
        """Update fixture with latest API data - store raw API data only"""
        try:
            # ✅ Store raw API data exactly as it comes, _id = fixture.id
            doc = dict(api_data)
            doc["_id"] = fixture_id
            
            result = self.fixtures_active.replace_one(
                {"_id": fixture_id},
                doc,
                upsert=True
            )
            
            return result.modified_count > 0 or result.upserted_id is not None
        
        except Exception as e:
            print(f"❌ Error updating fixture {fixture_id}: {e}")
            return False

    def store_goal_pending(self, fixture_id: int, goal_data: dict) -> bool:
        """Store goal in goals_pending collection - raw API data only"""
        try:
            # ✅ Only process actual goals
            if goal_data.get("type") != "Goal":
                return False
            
            # ✅ Extract time data for correct _id format
            time_data = goal_data.get("time", {})
            elapsed = time_data.get("elapsed", 0)
            extra = time_data.get("extra")
            
            # ✅ NEW: Use your specified _id format
            if extra is not None:
                goal_id = f"{fixture_id}_{elapsed}_{extra}"
            else:
                goal_id = f"{fixture_id}_{elapsed}"
            
            # ✅ Store exactly as from API, use custom _id
            goal_doc = dict(goal_data)  # Raw API data only
            goal_doc["_id"] = goal_id   # Set custom _id format
            
            self.goals_pending.replace_one({"_id": goal_id}, goal_doc, upsert=True)
            
            # Extract for logging
            player_name = goal_data.get("player", {}).get("name", "Unknown")
            team_name = goal_data.get("team", {}).get("name", "Unknown")
            print(f"✅ Stored goal: {team_name} - {player_name} ({elapsed}')")
            return True
        
        except Exception as e:
            print(f"❌ Error storing goal: {e}")
            return False

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

    def get_existing_goal_ids(self, fixture_id: int) -> set:
        """Get existing goal IDs for a fixture using your _id format"""
        try:
            # Since _id format is "{fixture_id}_{elapsed}[_{extra}]", we can use regex
            import re
            pattern = re.compile(f"^{fixture_id}_")
            
            # Check both pending and processed collections
            pending_goals = list(self.goals_pending.find({"_id": pattern}, {"_id": 1}))
            processed_goals = list(self.goals_processed.find({"_id": pattern}, {"_id": 1}))
            
            # Extract _id values
            existing_ids = set()
            for goal in pending_goals + processed_goals:
                existing_ids.add(goal["_id"])
            
            return existing_ids
            
        except Exception as e:
            print(f"❌ Error getting existing goal IDs for fixture {fixture_id}: {e}")
            return set()
