# ✅ CREATE: found_footy/utils/events.py
from prefect import get_run_logger
from datetime import datetime, timezone
from typing import List

def goal_trigger(fixture_id: int, new_goal_events: List[dict] = None):
    """Helper function: Emit goal events with rich context for automation"""
    logger = get_run_logger()
    logger.info(f"⚽ Goal trigger for fixture {fixture_id}")
    
    try:
        if not new_goal_events:
            return 0
        
        # ✅ NEW: Get fixture context for rich naming
        from found_footy.storage.mongo_store import FootyMongoStore
        store = FootyMongoStore()
        fixture = store.fixtures_active.find_one({"fixture_id": fixture_id})
        
        # Extract team names for opponent context
        home_team = fixture.get("team_names", {}).get("home", "Home") if fixture else "Home"
        away_team = fixture.get("team_names", {}).get("away", "Away") if fixture else "Away"
        
        events_emitted = 0
        current_time = datetime.now(timezone.utc)
        
        for goal_event in new_goal_events:
            minute = goal_event.get("time", {}).get("elapsed", 0)
            player_id = goal_event.get("player", {}).get("id", 0)
            goal_id = f"{fixture_id}_{minute}_{player_id}"
            
            team_name = goal_event.get("team", {}).get("name", "Unknown Team")
            player_name = goal_event.get("player", {}).get("name", "Unknown Player")
            goal_type = goal_event.get("detail", "Goal")
            
            # ✅ ENHANCED: Determine opponent for context
            opponent_name = away_team if team_name == home_team else home_team
            
            logger.info(f"📡 Emitting goal.detected event for goal {goal_id}")
            try:
                from prefect.events import emit_event
                
                emit_event(
                    event="goal.detected",
                    resource={"prefect.resource.id": f"goal.{goal_id}"},
                    payload={
                        "goal_id": goal_id,
                        "fixture_id": fixture_id,
                        "team_name": team_name,
                        "player_name": player_name,
                        "minute": str(minute),  # ✅ ENSURE STRING
                        "goal_type": goal_type,
                        "opponent_name": opponent_name,
                        "home_team": home_team,
                        "away_team": away_team,
                        "detected_at": current_time.isoformat(),
                    }
                )
                events_emitted += 1
                
                logger.info(f"✅ Event emitted: {team_name} | {player_name} ({minute}') vs {opponent_name}")
                
            except Exception as e:
                logger.error(f"❌ Failed to emit event for goal {goal_id}: {e}")
        
        return events_emitted
        
    except Exception as e:
        logger.error(f"❌ Error in goal_trigger: {e}")
        return 0

