import os
from pymongo import MongoClient, UpdateOne
from datetime import datetime, timezone, timedelta
from typing import Dict, List, Optional

class FootyMongoStore:
    """MongoDB storage for football application data - 6 collections architecture"""
    
    def __init__(self, connection_url=None):
        if connection_url is None:
            connection_url = os.getenv('MONGODB_URL', 'mongodb://localhost:27017/')
        
        self.client = MongoClient(connection_url)
        self.db = self.client.found_footy
        
        # ✅ NEW: 6 collections architecture
        self.teams = self.db.teams
        self.fixtures_staging = self.db.fixtures_staging
        self.fixtures_active = self.db.fixtures_active
        self.fixtures_processed = self.db.fixtures_processed
        self.goals_active = self.db.goals_active
        self.goals_processed = self.db.goals_processed
        
        # Create indexes for performance
        self._create_indexes()
    
    def _create_indexes(self):
        """Create database indexes for better query performance"""
        try:
            # Team indexes
            self.teams.create_index([("team_id", 1)], unique=True)
            self.teams.create_index([("country", 1)])
            self.teams.create_index([("team_type", 1)])  # ✅ NEW: Index for team type
            self.teams.create_index([("uefa_ranking", 1)])  # ✅ NEW: UEFA ranking
            self.teams.create_index([("fifa_ranking", 1)])  # ✅ NEW: FIFA ranking
            
            # ✅ NEW: Staging fixtures indexes
            self.fixtures_staging.create_index([("fixture_id", 1)], unique=True)
            self.fixtures_staging.create_index([("kickoff_time", 1)])
            self.fixtures_staging.create_index([("teams.home", 1)])
            self.fixtures_staging.create_index([("teams.away", 1)])
            
            # ✅ NEW: Active fixtures indexes
            self.fixtures_active.create_index([("fixture_id", 1)], unique=True)
            self.fixtures_active.create_index([("status", 1)])
            self.fixtures_active.create_index([("teams.home", 1)])
            self.fixtures_active.create_index([("teams.away", 1)])
            
            # ✅ NEW: Processed fixtures indexes
            self.fixtures_processed.create_index([("fixture_id", 1)], unique=True)
            self.fixtures_processed.create_index([("date", 1)])
            self.fixtures_processed.create_index([("teams.home", 1)])
            self.fixtures_processed.create_index([("teams.away", 1)])
            
            # ✅ FIXED: Active goals indexes - use compound key matching code logic
            self.goals_active.create_index([("fixture_id", 1), ("minute", 1), ("player_id", 1)], unique=True)
            self.goals_active.create_index([("fixture_id", 1)])
            self.goals_active.create_index([("team_id", 1)])
            
            # ✅ FIXED: Processed goals indexes - use compound key matching code logic  
            self.goals_processed.create_index([("fixture_id", 1), ("minute", 1), ("player_id", 1)], unique=True)
            self.goals_processed.create_index([("fixture_id", 1)])
            self.goals_processed.create_index([("team_id", 1)])
            self.goals_processed.create_index([("created_at", 1)])
            
            print("✅ MongoDB indexes created successfully")
        except Exception as e:
            print(f"⚠️ Error creating indexes: {e}")

    # ✅ NEW: Helper method - fixtures_advance
    def fixtures_advance(self, source_collection_name: str, destination_collection_name: str, fixture_id: int = None) -> dict:
        """📋 HELPER: Move fixtures between collections with consistent error handling"""
        try:
            source_collection = getattr(self, source_collection_name)
            destination_collection = getattr(self, destination_collection_name)
            
            # Build query
            query = {"fixture_id": fixture_id} if fixture_id else {}
            source_docs = list(source_collection.find(query))
            
            if not source_docs:
                return {"status": "success", "advanced_count": 0, "message": "No documents found to advance"}
            
            # Move documents
            for doc in source_docs:
                doc["advanced_at"] = datetime.now(timezone.utc)
                destination_collection.replace_one({"_id": doc["_id"]}, doc, upsert=True)
        
            # Delete from source
            delete_result = source_collection.delete_many(query)
            
            print(f"📋 Advanced {len(source_docs)} fixtures: {source_collection_name} → {destination_collection_name}")
            return {"status": "success", "advanced_count": len(source_docs)}
        
        except Exception as e:
            print(f"❌ Error advancing fixtures: {e}")
            return {"status": "error", "advanced_count": 0, "error": str(e)}

    # ✅ NEW: Bulk insert fixtures into staging/active
    def bulk_insert_fixtures(self, fixtures_data: List[dict], collection_name: str) -> int:
        """Universal bulk insert fixtures into any collection"""
        if not fixtures_data:
            return 0
        
        try:
            collection_map = {
                "fixtures_staging": self.fixtures_staging,
                "fixtures_active": self.fixtures_active,
                "fixtures_processed": self.fixtures_processed
            }
            
            target_collection = collection_map.get(collection_name)
            if target_collection is None:
                raise ValueError(f"Unknown collection: {collection_name}")
            
            print(f"💾 Bulk inserting {len(fixtures_data)} fixtures into {collection_name}...")
            
            documents = []
            for fixture in fixtures_data:
                # Convert date string to datetime for proper MongoDB storage
                kickoff_time = datetime.fromisoformat(fixture["time"].replace('Z', '+00:00'))
                fixture_date = kickoff_time.date()
                
                # ✅ SMART: Use current scores if available, otherwise default to 0-0
                current_goals = fixture.get("current_goals", {"home": 0, "away": 0})
                
                # Base document structure
                doc = {
                    "_id": fixture["id"],
                    "fixture_id": fixture["id"],
                    "kickoff_time": kickoff_time,
                    "date": datetime.combine(fixture_date, datetime.min.time()).replace(tzinfo=timezone.utc),
                    "teams": {
                        "home": fixture["home_id"],
                        "away": fixture["away_id"]
                    },
                    "team_names": {
                        "home": fixture["home"],
                        "away": fixture["away"]
                    },
                    "league": {
                        "id": fixture["league_id"],
                        "name": fixture["league"]
                    },
                    "status": fixture.get("status", "NS"),
                    # ✅ SIMPLE: Use API scores if available, default to 0-0
                    "goals": current_goals,
                    "created_at": datetime.now(timezone.utc),
                    "last_checked": datetime.now(timezone.utc)
                }
                
                documents.append(doc)
        
            # Use upsert operations to handle duplicates gracefully
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

    # ✅ NEW: Get all active fixtures for monitoring
    def get_all_active_fixtures(self) -> List[dict]:
        """Get all fixtures from active collection"""
        try:
            return list(self.fixtures_active.find({}))
        except Exception as e:
            print(f"❌ Error getting active fixtures: {e}")
            return []

    # ✅ REMOVED: Old methods replaced by delta/update pattern
    # - update_active_fixture_detect_goals() → replaced by fixtures_delta() + fixtures_update()
    # - diff_goal_events() → replaced by goals_delta()

    # ✅ UPDATED: Store goal with better duplicate handling
    def store_goal_active(self, fixture_id: int, goal_data: dict) -> bool:
        """Store goal in goals_active collection with ENHANCED validation"""
        try:
            minute = goal_data.get("time", {}).get("elapsed", 0)
            player_id = goal_data.get("player", {}).get("id", 0)
            player_name = goal_data.get("player", {}).get("name", "")
            team_name = goal_data.get("team", {}).get("name", "")
            
            # ✅ ENHANCED VALIDATION: Reject incomplete goals
            if not player_name:
                print(f"❌ Goal rejected: Missing player name for fixture {fixture_id}")
                return False
            
            if not team_name:
                print(f"❌ Goal rejected: Missing team name for fixture {fixture_id}")
                return False
            
            if minute <= 0:
                print(f"❌ Goal rejected: Invalid minute {minute} for fixture {fixture_id}")
                return False
            
            if player_id <= 0:
                print(f"❌ Goal rejected: Invalid player ID {player_id} for fixture {fixture_id}")
                return False
            
            goal_id = f"{fixture_id}_{minute}_{player_id}"
            
            # ✅ DOUBLE-CHECK: Prevent duplicates across BOTH collections
            existing_active = self.goals_active.find_one({"_id": goal_id})
            existing_processed = self.goals_processed.find_one({"_id": goal_id})
            
            if existing_active:
                print(f"⚠️ Goal {goal_id} already exists in goals_active - skipping")
                return False
            
            if existing_processed:
                print(f"⚠️ Goal {goal_id} already processed in goals_processed - skipping")
                return False
            
            # ✅ STORE: Goal with complete data
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
            
            result = self.goals_active.insert_one(document)
            
            print(f"✅ Stored COMPLETE goal: {team_name} - {player_name} ({minute}') [{goal_id}]")
            if assist_name:
                print(f"   🎯 Assist: {assist_name}")
        
            return True
            
        except Exception as e:
            print(f"❌ Error storing goal: {e}")
            return False

    # ✅ NEW: Check if collections are empty
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

    # ✅ NEW: Get team IDs from teams collection
    def get_team_ids(self) -> List[int]:
        """Get all team IDs from teams collection"""
        try:
            teams = list(self.teams.find({}, {"team_id": 1}))
            return [team["team_id"] for team in teams]
        except Exception as e:
            print(f"❌ Error getting team IDs: {e}")
            return []

    # ✅ KEEP: Essential existing methods
    def store_team_metadata(self, team_data: dict) -> bool:
        """Store team metadata with enhanced support for different team types"""
        try:
            document = {
                "_id": team_data["team_id"],
                "team_id": team_data["team_id"],
                "name": team_data["team_name"],
                "country": team_data.get("country", "Unknown"),
                "team_type": team_data.get("team_type", "club"),  # ✅ NEW: Track team type
                "uefa_ranking": team_data.get("uefa_ranking"),
                "fifa_ranking": team_data.get("fifa_ranking"),    # ✅ NEW: FIFA ranking
                "created_at": datetime.now(timezone.utc)
            }
            
            self.teams.replace_one(
                {"_id": team_data["team_id"]}, 
                document, 
                upsert=True
            )
            
            return True
            
        except Exception as e:
            print(f"❌ Error storing team metadata: {e}")
            return False

    def drop_all_collections(self):
        """Drop all collections for fresh start"""
        try:
            collection_names = self.db.list_collection_names()
            for collection_name in collection_names:
                self.db.drop_collection(collection_name)
                print(f"🗑️ Dropped collection: {collection_name}")
            
            self._create_indexes()
            return True
        except Exception as e:
            print(f"❌ Error dropping collections: {e}")
            return False

    # ✅ UPDATE: mongo_store.py - Split responsibilities
    def fixtures_delta(self, fixture_id: int, api_data: dict) -> dict:
        """Pure comparison with centralized status logic from Prefect Variables"""
        try:
            current_fixture = self.fixtures_active.find_one({"_id": fixture_id})
            if not current_fixture:
                return {"status": "error", "goals_changed": False, "message": "Fixture not found"}
            
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
            
            # ✅ NEW: Use centralized status logic from Prefect Variables
            try:
                from found_footy.utils.fixture_status import is_fixture_completed
                fixture_completed = is_fixture_completed(status)
            except Exception as e:
                print(f"⚠️ Could not load status config: {e}")
                # Fallback to hardcoded logic
                completed_statuses = {"FT", "AET", "PEN", "PST", "CANC", "ABD", "AWD", "WO"}
                fixture_completed = status in completed_statuses
            
            # Enhanced logging for status decisions
            if fixture_completed:
                print(f"🏁 Fixture {fixture_id} COMPLETED: {status} → moving to fixtures_processed")
            else:
                print(f"🔄 Fixture {fixture_id} ACTIVE: {status} → continue monitoring")
            
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

    # ✅ FIX: Complete the handle_fixture_changes method
    def handle_fixture_changes(self, fixture_id: int, delta_result: dict) -> dict:
        """Enhanced handling with all-or-nothing validation"""
        try:
            goals_processed = 0
            goals_rejected = 0
            
            if delta_result.get("goals_changed", False):
                print(f"🚨 Goals changed for fixture {fixture_id}")
                
                # Get complete goal events
                from found_footy.api.mongo_api import fixtures_events
                complete_goal_events = fixtures_events(fixture_id)
                
                # Enhanced validation with detailed rejection tracking
                valid_goals = []
                rejection_details = []  # Track rejection reasons
                for i, goal_event in enumerate(complete_goal_events):
                    player_name = goal_event.get("player", {}).get("name", "")
                    team_name = goal_event.get("team", {}).get("name", "")
                    minute = goal_event.get("time", {}).get("elapsed", 0)
                    player_id = goal_event.get("player", {}).get("id", 0)
                    
                    if player_name and team_name and minute > 0 and player_id > 0:
                        valid_goals.append(goal_event)
                    else:
                        goals_rejected += 1
                        rejection_details.append({
                            "goal_index": i,
                            "missing_player": not player_name,
                            "missing_team": not team_name,
                            "invalid_minute": minute <= 0,
                            "invalid_player_id": player_id <= 0,
                            "minute": minute
                        })
                
                goals_processed = len(valid_goals)
                
                # All-or-nothing decision logic
                if goals_rejected == 0:
                    # All goals valid - process normally
                    new_goals = self.goals_delta(fixture_id, complete_goal_events, trigger_actions=True)
                    self.fixtures_update(fixture_id, delta_result)
                    print(f"✅ ALL VALID: Processed {goals_processed} complete goals")
                    
                else:
                    # Some goals invalid - reject all and retry later
                    print(f"⚠️ REJECTED UPDATE: {goals_rejected}/{len(complete_goal_events)} goals invalid")
                    print(f"   📊 Valid goals: {goals_processed}, Invalid: {goals_rejected}")
                    print(f"   🔄 Will retry in 3 minutes when API data may be complete")
                    
                    # Log specific rejection reasons for debugging
                    for detail in rejection_details:
                        missing_fields = []
                        if detail["missing_player"]: missing_fields.append("player")
                        if detail["missing_team"]: missing_fields.append("team")
                        if detail["invalid_minute"]: missing_fields.append("minute")
                        if detail["invalid_player_id"]: missing_fields.append("player_id")
                        print(f"     Goal {detail['goal_index']}: Missing {', '.join(missing_fields)}")
    
            else:
                # No goal changes - safe to update
                self.fixtures_update(fixture_id, delta_result)
            
            return {
                "status": "success",
                "goals_processed": goals_processed if goals_rejected == 0 else 0,
                "goals_rejected": goals_rejected,
                "rejection_details": rejection_details,
                "fixture_updated": goals_rejected == 0,
                "will_retry": goals_rejected > 0,
                "retry_in_minutes": 3
            }

        except Exception as e:
            print(f"❌ Error handling fixture changes: {e}")
            return {"status": "error", "goals_processed": 0, "error": str(e)}

    # ✅ FIX: Complete the goals_delta method in mongo_store.py
    def goals_delta(self, fixture_id: int, api_goal_events: List[dict], trigger_actions: bool = False) -> List[dict]:
        """Compare current goals with API goals and return new goals"""
        try:
            new_goal_events = []
        
            for goal_event in api_goal_events:
                minute = goal_event.get("time", {}).get("elapsed", 0)
                player_id = goal_event.get("player", {}).get("id", 0)
                player_name = goal_event.get("player", {}).get("name", "")
                team_name = goal_event.get("team", {}).get("name", "")
                
                # Enhanced validation: Skip goals with missing required data
                if not player_name or not team_name or minute <= 0 or player_id <= 0:
                    print(f"⚠️ Skipping incomplete goal data: player='{player_name}', team='{team_name}', minute={minute}, player_id={player_id}")
                    continue
                
                goal_id = f"{fixture_id}_{minute}_{player_id}"
                
                # Check BOTH active AND processed collections for duplicates
                existing_active = self.goals_active.find_one({"_id": goal_id})
                existing_processed = self.goals_processed.find_one({"_id": goal_id})
                
                if existing_active:
                    print(f"⚠️ Goal {goal_id} already exists in goals_active - skipping")
                    continue
                elif existing_processed:
                    print(f"⚠️ Goal {goal_id} already processed in goals_processed - skipping")
                    continue
                else:
                    # This is a genuinely new goal with complete data
                    new_goal_events.append(goal_event)
                    print(f"✅ NEW goal detected: {player_name} ({minute}') for {team_name}")
                    
                    # Only store and trigger if trigger_actions=True
                    if trigger_actions:
                        # Store the new goal
                        goal_stored = self.store_goal_active(fixture_id, goal_event)
                        if goal_stored:
                            # Trigger automation event for this specific goal
                            from found_footy.utils.events import goal_trigger
                            goal_trigger(fixture_id, [goal_event])

            return new_goal_events
        
        except Exception as e:
            print(f"❌ Error in goals_delta for fixture {fixture_id}: {e}")
            return []

    # ✅ FIX: Complete the goals_update method
    def goals_update(self, fixture_id: int, api_goal_events: List[dict]) -> int:
        """Update goals for a fixture and store new goals in goals_active"""
        try:
            goals_stored = 0
            
            for goal_event in api_goal_events:
                goal_stored = self.store_goal_active(fixture_id, goal_event)
                if goal_stored:
                    goals_stored += 1
        
            return goals_stored
        
        except Exception as e:
            print(f"❌ Error updating goals for fixture {fixture_id}: {e}")
            return 0

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

    def get_teams_by_type(self, team_type: str = "all") -> List[dict]:
        """Get teams filtered by type"""
        try:
            if team_type == "all":
                teams = list(self.teams.find({}))
            else:
                teams = list(self.teams.find({"team_type": team_type}))
            
            return teams
        except Exception as e:
            print(f"❌ Error getting teams by type: {e}")
            return []

    async def get_team_ids_from_variables(self, team_type="all") -> List[int]:
        """Get team IDs from Prefect Variables"""
        try:
            from found_footy.api.mongo_api import get_team_ids_from_variables
            return await get_team_ids_from_variables(team_type)
        except Exception as e:
            print(f"❌ Error getting team IDs from variables: {e}")
            return self.get_team_ids()  # Fallback to database

