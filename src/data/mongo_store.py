import os
from pymongo import MongoClient, ASCENDING
from datetime import datetime, timezone
from typing import List

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
        # ✅ SINGLE goals collection
        self.goals = self.db.goals
        
        self._create_indexes()

    def _create_indexes(self):
        """Create indexes"""
        try:
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

            # ✅ Single goals collection indexes
            self.goals.create_index([("fixture_id", ASCENDING)])
            self.goals.create_index([("player_id", ASCENDING)])
            self.goals.create_index([("processing_status", ASCENDING)])
            
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

    def fixtures_advance(self, source_collection_name: str, destination_collection_name: str, fixture_id: int | None = None) -> dict:
        """Move fixtures between collections"""
        try:
            source_collection = getattr(self, source_collection_name)
            destination_collection = getattr(self, destination_collection_name)
            
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
            
            doc = dict(item)
            doc["_id"] = fixture_id
            
            try:
                result = target.replace_one(
                    {"_id": fixture_id},
                    doc,
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
                if collection_name == "fixtures_active":
                    count = self.fixtures_active.count_documents({})
                elif collection_name == "fixtures_staging":
                    count = self.fixtures_staging.count_documents({})
                elif collection_name == "fixtures_completed":
                    count = self.fixtures_completed.count_documents({})
                elif collection_name == "goals":
                    count = self.goals.count_documents({})
                else:
                    continue
                
                if count > 0:
                    return False
            return True
        
        except Exception as e:
            print(f"❌ Error checking collections: {e}")
            return True

    def fixtures_delta(self, fixture_id: int, api_data: dict) -> dict:
        """Compare current fixture with API data"""
        try:
            current_fixture = self.fixtures_active.find_one({"_id": fixture_id})
            if not current_fixture:
                return {"status": "not_found", "fixture_id": fixture_id}
            
            api_status = self._extract_status(api_data)
            api_goals = self._extract_current_goals(api_data)
            current_goals = self._extract_current_goals(current_fixture)
            
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
        """Update fixture with latest API data"""
        try:
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

    def store_goal(self, fixture_id: int, goal_data: dict, processing_status: str = "discovered") -> bool:
        """Store goal in single goals collection with processing status"""
        try:
            # Only process actual goals
            if goal_data.get("type") != "Goal" or goal_data.get("detail") == "Missed Penalty":
                return False
            
            # Extract time data for goal ID
            time_data = goal_data.get("time", {})
            elapsed = time_data.get("elapsed", 0)
            extra = time_data.get("extra")
            
            # Generate goal ID with + format for extra time
            if extra is not None and extra > 0:
                goal_id = f"{fixture_id}_{elapsed}+{extra}"
            else:
                goal_id = f"{fixture_id}_{elapsed}"
            
            # Check if goal already exists
            existing_goal = self.goals.find_one({"_id": goal_id})
            if existing_goal:
                print(f"🔄 Goal {goal_id} already exists - updating data only")
                # Update existing with latest API data but preserve processing fields
                goal_doc = dict(goal_data)
                goal_doc["_id"] = goal_id
                goal_doc["fixture_id"] = fixture_id
                
                # Preserve processing fields
                for field in ["processing_status", "discovered_videos", "successful_uploads", "processing_completed_at"]:
                    if field in existing_goal:
                        goal_doc[field] = existing_goal[field]
                
                self.goals.replace_one({"_id": goal_id}, goal_doc)
                return False  # Not a new goal
            
            # Store new goal
            goal_doc = dict(goal_data)
            goal_doc["_id"] = goal_id
            goal_doc["fixture_id"] = fixture_id
            goal_doc["processing_status"] = processing_status
            goal_doc["created_at"] = datetime.now(timezone.utc).isoformat()
            
            self.goals.insert_one(goal_doc)
            print(f"✅ New goal stored: {goal_id}")
            return True
            
        except Exception as e:
            print(f"❌ Error storing goal: {e}")
            return False

    def get_existing_goal_ids(self, fixture_id: int) -> set:
        """Get existing goal IDs for a fixture from goals collection"""
        try:
            import re
            pattern = re.compile(f"^{fixture_id}_\\d+(?:\\+\\d+)?$")
            
            goals = list(self.goals.find({"_id": pattern}, {"_id": 1}))
            return {goal["_id"] for goal in goals}
            
        except Exception as e:
            print(f"❌ Error getting existing goal IDs for fixture {fixture_id}: {e}")
            return set()

    def validate_goal_count(self, fixture_id: int, expected_total_goals: int) -> dict:
        """Validate stored goals match expected count from fixture data"""
        try:
            stored_goals_count = self.goals.count_documents({"fixture_id": fixture_id})
            
            is_valid = stored_goals_count == expected_total_goals
            
            return {
                "is_valid": is_valid,
                "stored_count": stored_goals_count,
                "expected_count": expected_total_goals,
                "difference": expected_total_goals - stored_goals_count
            }
            
        except Exception as e:
            print(f"❌ Error validating goal count for fixture {fixture_id}: {e}")
            return {
                "is_valid": False,
                "stored_count": 0,
                "expected_count": expected_total_goals,
                "error": str(e)
            }

    def update_goal_processing_status(self, goal_id: str, status: str, **kwargs) -> bool:
        """Update goal processing status and additional fields"""
        try:
            update_data = {"processing_status": status}
            
            for key, value in kwargs.items():
                update_data[key] = value
            
            if status == "completed":
                update_data["processing_completed_at"] = datetime.now(timezone.utc).isoformat()
            
            result = self.goals.update_one(
                {"_id": goal_id},
                {"$set": update_data}
            )
            
            return result.modified_count > 0
            
        except Exception as e:
            print(f"❌ Error updating goal status: {e}")
            return False

    def get_goals_by_status(self, fixture_id: int | None = None, status: str | None = None) -> List[dict]:
        """Get goals filtered by fixture and/or processing status"""
        try:
            query = {}
            if fixture_id:
                query["fixture_id"] = fixture_id
            if status:
                query["processing_status"] = status
                
            return list(self.goals.find(query))
            
        except Exception as e:
            print(f"❌ Error getting goals: {e}")
            return []

    def get_active_fixtures(self) -> List[dict]:
        """Get all active fixtures for monitoring"""
        try:
            return list(self.fixtures_active.find({}))
        except Exception as e:
            print(f"❌ Error getting active fixtures: {e}")
            return []