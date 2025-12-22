"""
RAG Activities - Team Alias Lookup

Activities for the RAGWorkflow.

Current stub implementation returns team name as-is.
Future: Query local LLM (Ollama) for team variations.
"""
from temporalio import activity
from typing import List


@activity.defn
async def get_team_aliases(team_name: str) -> List[str]:
    """
    Get team name aliases for Twitter search.
    
    STUB IMPLEMENTATION:
    Returns the team name as a single-element list.
    Twitter workflow will use this for search queries.
    
    FUTURE RAG IMPLEMENTATION:
    Will query local LLM to get variations like:
    - "Atletico de Madrid" → ["Atletico", "Atleti", "ATM"]
    - "Manchester United"  → ["Man United", "Man Utd", "MUFC"]
    - "Liverpool"          → ["Liverpool", "LFC", "Reds"]
    
    Args:
        team_name: Full team name from API (e.g., "Liverpool")
    
    Returns:
        List of aliases for Twitter search (currently just [team_name])
    """
    activity.logger.info(f"🔍 Getting aliases for team: {team_name}")
    
    # STUB: Return team name as single element
    # TODO: Replace with RAG/LLM lookup
    aliases = [team_name]
    
    activity.logger.info(f"📋 Resolved aliases: {aliases}")
    return aliases


@activity.defn
async def save_team_aliases(fixture_id: int, event_id: str, aliases: List[str]) -> bool:
    """
    Save resolved aliases to event in MongoDB.
    
    Stores in _twitter_aliases field for debugging/visibility.
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID
        aliases: List of resolved team aliases
    
    Returns:
        True if successful
    """
    from src.data.mongo_store import FootyMongoStore
    from src.data.models import EventFields
    
    activity.logger.info(f"💾 Saving aliases for {event_id}: {aliases}")
    
    store = FootyMongoStore()
    
    try:
        result = store.fixtures_active.update_one(
            {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
            {"$set": {f"events.$.{EventFields.TWITTER_ALIASES}": aliases}}
        )
        
        if result.modified_count > 0:
            activity.logger.info(f"✅ Saved aliases for {event_id}")
            return True
        else:
            activity.logger.warning(f"⚠️ No document modified for {event_id}")
            return False
            
    except Exception as e:
        activity.logger.error(f"❌ Error saving aliases: {e}")
        return False
