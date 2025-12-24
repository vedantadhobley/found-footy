"""
RAG Activities - Team Alias Lookup with Wikidata + Ollama LLM

Activities for the RAGWorkflow.

True RAG Flow:
1. Check MongoDB cache (team_aliases collection by team_id)
2. If miss: Retrieve aliases from Wikidata API
3. Augment LLM prompt with Wikidata aliases as context
4. Generate: LLM selects/derives best 3 from Wikidata list
5. Normalize (remove diacritics)
6. Cache and return

This is RAG - Retrieval Augmented Generation:
- Retrieval: Wikidata provides authoritative aliases
- Augmented: LLM prompt includes retrieved data
- Generation: LLM derives best Twitter search terms from that data
"""
from temporalio import activity
from typing import List, Optional
import httpx
import json
import os
import unicodedata
from datetime import datetime, timezone


# Environment-aware URL (set in docker-compose)
OLLAMA_URL = os.getenv("OLLAMA_URL", "http://found-footy-dev-ollama:11434")
OLLAMA_MODEL = os.getenv("OLLAMA_MODEL", "phi3:mini")

# Wikidata API endpoints
WIKIDATA_SEARCH_URL = "https://www.wikidata.org/w/api.php"
WIKIDATA_ENTITY_URL = "https://www.wikidata.org/wiki/Special:EntityData"

# RAG System Prompt - LLM derives from Wikidata, not from training data
SYSTEM_PROMPT = """You are a football alias selector. Given a list of official aliases from Wikidata, derive the 3 best for Twitter search.

Rules:
- Return ONLY a JSON array of exactly 3 strings
- Choose short names (1-2 words) that fans use on Twitter
- Prefer: abbreviations (ATM, LFC), short names (Atleti, Barca), common nicknames
- You MAY simplify aliases: "El Atleti" → "Atleti", "FC Barcelona" → "Barcelona"
- All output must be DERIVED from the Wikidata list (substrings/simplifications OK)
- DO NOT invent aliases that aren't grounded in the Wikidata data
- No explanations, just the JSON array"""


def _normalize_alias(alias: str) -> str:
    """
    Normalize alias for Twitter search by removing diacritics.
    
    Examples:
        "Atlético" → "Atletico"
        "München" → "Munchen"
    """
    normalized = unicodedata.normalize('NFD', alias)
    return ''.join(c for c in normalized if unicodedata.category(c) != 'Mn')


async def _search_wikidata_qid(team_name: str) -> Optional[str]:
    """
    Search Wikidata for team QID.
    
    Args:
        team_name: Team name to search for
        
    Returns:
        Wikidata QID (e.g., "Q8701") or None
    """
    try:
        async with httpx.AsyncClient(timeout=10.0) as client:
            response = await client.get(
                WIKIDATA_SEARCH_URL,
                params={
                    "action": "wbsearchentities",
                    "search": team_name,
                    "language": "en",
                    "format": "json",
                    "type": "item",
                    "limit": 5
                }
            )
            response.raise_for_status()
            data = response.json()
            
            # Look for football club in results
            for result in data.get("search", []):
                desc = result.get("description", "").lower()
                if "football" in desc or "soccer" in desc:
                    return result.get("id")
            
            # Fallback: return first result if any
            if data.get("search"):
                return data["search"][0].get("id")
                
    except Exception as e:
        activity.logger.warning(f"⚠️ Wikidata search failed: {e}")
    
    return None


async def _fetch_wikidata_aliases(qid: str) -> List[str]:
    """
    Fetch all aliases from Wikidata entity.
    
    Args:
        qid: Wikidata QID (e.g., "Q8701")
        
    Returns:
        List of all aliases from Wikidata (labels + aliases in all languages)
    """
    aliases = []
    try:
        async with httpx.AsyncClient(timeout=10.0) as client:
            response = await client.get(
                f"{WIKIDATA_ENTITY_URL}/{qid}.json"
            )
            response.raise_for_status()
            data = response.json()
            
            entity = data.get("entities", {}).get(qid, {})
            
            # Get label (primary name)
            labels = entity.get("labels", {})
            for lang_data in labels.values():
                if lang_data.get("value"):
                    aliases.append(lang_data["value"])
            
            # Get all aliases
            all_aliases = entity.get("aliases", {})
            for lang_aliases in all_aliases.values():
                for alias_entry in lang_aliases:
                    if alias_entry.get("value"):
                        aliases.append(alias_entry["value"])
            
            # Deduplicate while preserving order
            seen = set()
            unique_aliases = []
            for alias in aliases:
                normalized = alias.lower()
                if normalized not in seen:
                    seen.add(normalized)
                    unique_aliases.append(alias)
            
            return unique_aliases
            
    except Exception as e:
        activity.logger.warning(f"⚠️ Wikidata entity fetch failed: {e}")
    
    return []


def _parse_llm_response(text: str) -> Optional[List[str]]:
    """Parse JSON array from LLM response, handling markdown wrapping."""
    # Handle markdown wrapping: ```json\n[...]\n```
    if "```" in text:
        parts = text.split("```")
        for part in parts:
            part = part.strip()
            if part.startswith("json"):
                text = part[4:].strip()
                break
            elif part.startswith("["):
                text = part
                break
    
    # Find the JSON array
    start = text.find("[")
    end = text.rfind("]") + 1
    if start >= 0 and end > start:
        text = text[start:end]
    
    try:
        aliases = json.loads(text.strip())
        if isinstance(aliases, list) and len(aliases) >= 3:
            return [str(a).strip() for a in aliases[:3]]
    except json.JSONDecodeError:
        pass
    
    return None


def _derive_fallback_aliases(team_name: str) -> List[str]:
    """
    Deterministic fallback when Wikidata unavailable.
    
    Derives aliases from team name:
    - Full name
    - First word (if multi-word)
    - Initials (if 2+ words)
    """
    words = team_name.split()
    aliases = [team_name]
    
    if len(words) > 1:
        aliases.append(words[0])
        initials = "".join(w[0].upper() for w in words if w[0].isalpha())
        if len(initials) >= 2:
            aliases.append(initials)
    
    while len(aliases) < 3:
        aliases.append(team_name)
    
    return aliases[:3]


def _select_best_wikidata_aliases(wikidata_aliases: List[str]) -> List[str]:
    """
    Heuristic selection of best 3 aliases from Wikidata list.
    
    Priority:
    1. Short uppercase strings (abbreviations like ATM, LFC)
    2. Short single words (Atleti, Barca)
    3. Two-word names
    4. Everything else
    """
    if len(wikidata_aliases) <= 3:
        return wikidata_aliases[:3] if wikidata_aliases else []
    
    scored = []
    for alias in wikidata_aliases:
        score = 0
        words = alias.split()
        
        # Prefer abbreviations (all caps, 2-4 chars)
        if alias.isupper() and 2 <= len(alias) <= 4:
            score = 100
        # Prefer short single words
        elif len(words) == 1 and len(alias) <= 10:
            score = 80
        # Prefer two-word names
        elif len(words) == 2:
            score = 60
        # Penalize very long names
        elif len(alias) > 30:
            score = 10
        else:
            score = 40
        
        scored.append((score, alias))
    
    # Sort by score descending, then alphabetically
    scored.sort(key=lambda x: (-x[0], x[1]))
    
    # Take top 3, ensure diversity (no duplicates after normalization)
    selected = []
    seen_normalized = set()
    for _, alias in scored:
        normalized = _normalize_alias(alias).lower()
        if normalized not in seen_normalized:
            seen_normalized.add(normalized)
            selected.append(alias)
            if len(selected) == 3:
                break
    
    return selected


def _get_cached_aliases(store, team_id: int) -> Optional[dict]:
    """Check MongoDB cache for existing aliases by team_id."""
    return store.db.team_aliases.find_one({"_id": team_id})


def _cache_aliases(
    store, 
    team_id: int, 
    team_name: str,
    twitter_aliases: List[str], 
    model: str,
    wikidata_qid: Optional[str] = None,
    wikidata_aliases: Optional[List[str]] = None
) -> None:
    """
    Store aliases in MongoDB cache using team_id as _id.
    
    Also stores Wikidata info for traceability.
    """
    update_data = {
        "team_name": team_name,
        "twitter_aliases": twitter_aliases,
        "model": model,
        "updated_at": datetime.now(timezone.utc)
    }
    
    if wikidata_qid:
        update_data["wikidata_qid"] = wikidata_qid
    if wikidata_aliases:
        update_data["wikidata_aliases"] = wikidata_aliases
    
    store.db.team_aliases.update_one(
        {"_id": team_id},
        {
            "$set": update_data,
            "$setOnInsert": {
                "created_at": datetime.now(timezone.utc)
            }
        },
        upsert=True
    )


@activity.defn
async def get_team_aliases(team_id: int, team_name: str) -> List[str]:
    """
    Get team name aliases via Wikidata RAG + Ollama LLM with MongoDB caching.
    
    True RAG Flow:
    1. Check cache by team_id (O(1) lookup)
    2. RETRIEVE: Fetch aliases from Wikidata API
    3. AUGMENT: Include Wikidata aliases in LLM prompt
    4. GENERATE: LLM selects/derives best 3 from Wikidata list
    5. Normalize (remove diacritics)
    6. Cache and return
    
    Args:
        team_id: API-Football team ID (used as cache key)
        team_name: Full team name from API (for Wikidata search)
    
    Returns:
        List of 3 normalized aliases for Twitter search
    """
    from src.data.mongo_store import FootyMongoStore
    
    activity.logger.info(f"🔍 Getting aliases for team {team_id}: {team_name}")
    
    store = FootyMongoStore()
    
    # 1. Check cache by team_id
    cached = _get_cached_aliases(store, team_id)
    if cached:
        aliases = cached.get("twitter_aliases", [])
        activity.logger.info(f"📦 Cache hit: {aliases}")
        return aliases
    
    activity.logger.info(f"🔄 Cache miss, starting RAG pipeline...")
    
    # 2. RETRIEVE: Get aliases from Wikidata
    wikidata_aliases = []
    qid = await _search_wikidata_qid(team_name)
    if qid:
        activity.logger.info(f"📚 Found Wikidata QID: {qid}")
        wikidata_aliases = await _fetch_wikidata_aliases(qid)
        activity.logger.info(f"📚 Wikidata aliases ({len(wikidata_aliases)}): {wikidata_aliases[:10]}...")
    else:
        activity.logger.warning(f"⚠️ No Wikidata QID found for: {team_name}")
    
    # 3. AUGMENT + GENERATE: LLM selects from Wikidata
    if wikidata_aliases:
        try:
            async with httpx.AsyncClient(timeout=30.0) as client:
                # RAG prompt - includes retrieved Wikidata data
                prompt = f"""Team: {team_name}
Wikidata aliases: {json.dumps(wikidata_aliases)}

Select the 3 best aliases for Twitter search from the Wikidata list above. Return ONLY a JSON array."""
                
                response = await client.post(
                    f"{OLLAMA_URL}/api/generate",
                    json={
                        "model": OLLAMA_MODEL,
                        "prompt": prompt,
                        "system": SYSTEM_PROMPT,
                        "stream": False,
                        "options": {
                            "temperature": 0.2,
                            "num_predict": 40,
                        }
                    }
                )
                response.raise_for_status()
                
                result = response.json()
                text = result.get("response", "").strip()
                
                llm_aliases = _parse_llm_response(text)
                
                if llm_aliases:
                    # Validate: LLM output must be grounded in Wikidata (exact match OR substring)
                    wikidata_text = " ".join(wikidata_aliases).lower()
                    valid_aliases = [
                        a for a in llm_aliases 
                        if a.lower() in wikidata_text  # "Atleti" found in "El Atleti"
                    ]
                    
                    if len(valid_aliases) < 3:
                        activity.logger.warning(f"⚠️ LLM returned ungrounded aliases: {llm_aliases}, using heuristic")
                        valid_aliases = _select_best_wikidata_aliases(wikidata_aliases)
                    else:
                        valid_aliases = valid_aliases[:3]
                    
                    # 4. Normalize aliases (remove diacritics)
                    twitter_aliases = [_normalize_alias(a) for a in valid_aliases]
                    
                    activity.logger.info(f"✅ RAG result: {valid_aliases} → Normalized: {twitter_aliases}")
                    
                    # 5. Cache for future use (include Wikidata QID for traceability)
                    _cache_aliases(
                        store, 
                        team_id=team_id,
                        team_name=team_name,
                        twitter_aliases=twitter_aliases,
                        model=OLLAMA_MODEL,
                        wikidata_qid=qid,
                        wikidata_aliases=wikidata_aliases
                    )
                    return twitter_aliases
                
                activity.logger.warning(f"⚠️ LLM returned invalid format: {text}")
                
        except httpx.ConnectError:
            activity.logger.warning("⚠️ Ollama not available, using heuristic selection")
        except Exception as e:
            activity.logger.warning(f"⚠️ LLM error: {e}")
        
        # Fallback: heuristic selection from Wikidata
        selected = _select_best_wikidata_aliases(wikidata_aliases)
        twitter_aliases = [_normalize_alias(a) for a in selected]
        activity.logger.info(f"📋 Heuristic selection from Wikidata: {twitter_aliases}")
        
        _cache_aliases(
            store, 
            team_id=team_id,
            team_name=team_name,
            twitter_aliases=twitter_aliases,
            model="heuristic",
            wikidata_qid=qid,
            wikidata_aliases=wikidata_aliases
        )
        return twitter_aliases
    
    # No Wikidata data - use deterministic fallback (don't cache - try again later)
    fallback = _derive_fallback_aliases(team_name)
    fallback = [_normalize_alias(a) for a in fallback]
    activity.logger.info(f"📋 No Wikidata, fallback aliases: {fallback}")
    return fallback


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
