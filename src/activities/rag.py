"""
RAG Activities - Team Alias Lookup with Wikidata + LLM

Activities for the RAGWorkflow.

True RAG Flow:
1. Check MongoDB cache (team_aliases collection by team_id)
2. If miss: Retrieve aliases from Wikidata API
3. Augment LLM prompt with Wikidata aliases as context
4. Generate: LLM selects/derives best 5 Twitter search terms
5. Normalize (remove diacritics)
6. Cache and return

This is RAG - Retrieval Augmented Generation:
- Retrieval: Wikidata provides authoritative aliases
- Augmented: LLM prompt includes retrieved data
- Generation: LLM derives best Twitter search terms from that data

The LLM handles ALL the intelligence:
- Filtering out generic words (FC, Club, Real, etc.)
- Selecting best acronyms (MUFC, LFC, BVB)
- Deriving nationality adjectives for national teams (Argentina → Argentine)
- Picking distinctive nicknames (Gunners, Spurs, Reds)
"""
from temporalio import activity
from typing import List, Optional
import httpx
import json
import os
import unicodedata
from datetime import datetime, timezone

from src.utils.config import LLAMA_CHAT_URL

# Use centralized config
LLAMA_URL = LLAMA_CHAT_URL
LLAMA_MODEL = os.getenv("LLAMA_MODEL", "Qwen3-8B")

# Wikidata API endpoints
WIKIDATA_SEARCH_URL = "https://www.wikidata.org/w/api.php"
WIKIDATA_ENTITY_URL = "https://www.wikidata.org/wiki/Special:EntityData"

# User-Agent required by Wikidata API
HEADERS = {
    "User-Agent": "FoundFooty/1.0 (https://github.com/vedanta/found-footy; contact@example.com)"
}

# Maximum aliases for Twitter search (10 attempts × N aliases = N×10 searches per goal)
MAX_ALIASES = 5

# =============================================================================
# LLM System Prompts - Different for clubs vs national teams
# =============================================================================

CLUB_SYSTEM_PROMPT = """You are selecting Twitter search terms for a football CLUB.

From the Wikidata aliases provided, select the TOP 5 best terms for finding goal videos on Twitter.

PRIORITY ORDER:
1. ACRONYMS (2-5 letters, all caps) - MOST VALUABLE for Twitter search
   Examples: MUFC, LFC, BVB, PSG, FCB, MCFC, AFC, THFC
2. SHORT NICKNAMES (≤10 chars) - Very valuable
   Examples: Spurs, Juve, Barca, Atleti, Bayern, Utd, Gunners
3. DISTINCTIVE WORDS - Fan names, mascots, colors
   Examples: Devils, Blues, Reds, Magpies, Hammers, Wolves

SKIP THESE (too generic, will pollute search results):
- Generic football words: FC, Club, United, City, Real, Athletic, Sporting
- Articles/prepositions: The, La, El, De, Du, Of, And
- Generic org words: Association, Society, Union, Racing

OUTPUT: JSON array with exactly 3-5 aliases, acronym first if available.
Example: ["MUFC", "Utd", "Devils", "Manchester"]"""

NATIONAL_SYSTEM_PROMPT = """You are selecting Twitter search terms for a NATIONAL football team.

From the Wikidata aliases provided, select the TOP 5 best terms for finding goal videos on Twitter.
IMPORTANT: You MUST derive the nationality adjective from the country name!

PRIORITY ORDER:
1. NATIONALITY ADJECTIVE - DERIVE THIS from the country name!
   Argentina → Argentine, Brazil → Brazilian, France → French, Germany → German
   England → English, Spain → Spanish, Italy → Italian, Morocco → Moroccan
   This is THE MOST IMPORTANT term for national team searches!
2. ACRONYMS if available (rare for national teams)
3. SHORT NICKNAMES or team monikers
   Examples: Selecao (Brazil), Albiceleste (Argentina), Les Bleus (France)
   Examples: Three Lions (England), La Roja (Spain), Azzurri (Italy)

SKIP THESE (too generic):
- The country name itself (redundant with player name)
- Generic words: National, Team, Football, Men's, Selection

OUTPUT: JSON array with exactly 3-5 aliases, nationality adjective FIRST.
Example for Argentina: ["Argentine", "Argentinian", "Albiceleste"]
Example for Brazil: ["Brazilian", "Selecao", "Brasil"]"""


def _normalize_alias(alias: str) -> str:
    """Remove diacritics and strip article prefixes."""
    # Remove diacritics
    normalized = unicodedata.normalize('NFD', alias)
    result = ''.join(c for c in normalized if unicodedata.category(c) != 'Mn')
    
    # Strip article prefixes
    for prefix in ('The ', 'La ', 'El ', 'Los ', 'Las ', 'Le ', 'Les ', 'Gli ', 'Die ', 'Der ', 'Das '):
        if result.lower().startswith(prefix.lower()):
            result = result[len(prefix):]
            break
    
    return result.strip()


async def _search_wikidata_qid(team_name: str, team_type: str = "club", country: str = None, city: str = None) -> Optional[str]:
    """
    Search Wikidata for team QID using multiple search strategies.
    
    Strategy: Try most specific searches first, then broaden.
    For clubs, prefer results with detailed descriptions (mentioning city/country).
    
    Args:
        team_name: Team name from API (e.g., "Newcastle")
        team_type: "club" or "national"
        country: Country name (e.g., "England")
        city: City name from venue (e.g., "Newcastle upon Tyne")
    """
    # Normalize for search
    normalized = _normalize_alias(team_name)
    
    # Build search terms - MOST SPECIFIC FIRST
    search_terms = []
    
    if team_type == "club":
        # 1. Try with city first (most specific - "Newcastle upon Tyne" is unambiguous)
        if city:
            search_terms.append(f"{normalized} {city}")
        # 2. Try with country
        if country:
            search_terms.append(f"{normalized} {country}")
            search_terms.append(f"{normalized} FC {country}")
        # 3. Standard variations (many teams have suffixes dropped in API)
        search_terms.append(f"{normalized} United")
        search_terms.append(f"{normalized} City")
        search_terms.append(f"{normalized} FC")
        search_terms.append(f"FC {normalized}")
        search_terms.append(f"{normalized} football club")
        search_terms.append(normalized)
    else:
        # National teams
        name_for_search = "United States" if normalized.upper() == "USA" else normalized
        search_terms.append(f"{name_for_search} national football team")
        search_terms.append(f"{name_for_search} men's national football team")
        search_terms.append(f"{name_for_search} national soccer team")
    
    skip_keywords = ["women", "femen", "reserve", "youth", "junior", " b ", " c ", 
                     "under-", "u-19", "u-21", "academy", "futsal", "beach", "basketball"]
    
    # Track candidates - prefer those with detailed descriptions
    best_candidate = None
    best_desc_length = 0
    
    async with httpx.AsyncClient(timeout=10.0, headers=HEADERS) as client:
        for search_term in search_terms:
            try:
                response = await client.get(
                    WIKIDATA_SEARCH_URL,
                    params={
                        "action": "wbsearchentities",
                        "search": search_term,
                        "language": "en",
                        "format": "json",
                        "type": "item",
                        "limit": 10
                    }
                )
                response.raise_for_status()
                data = response.json()
                
                for result in data.get("search", []):
                    qid = result.get("id", "")
                    label = result.get("label", "")
                    desc = (result.get("description") or "").lower()
                    
                    if not qid.startswith("Q"):
                        continue
                    
                    # Skip women's, youth, reserve teams
                    if any(kw in desc for kw in skip_keywords) or any(kw in label.lower() for kw in skip_keywords):
                        continue
                    
                    # Skip B/C team suffixes
                    label_parts = label.split()
                    if len(label_parts) > 1 and label_parts[-1] in ("B", "C", "II", "III"):
                        continue
                    
                    # Must be football-related
                    if team_type == "club":
                        is_football = "football" in desc or "soccer" in desc or "fútbol" in desc
                        is_multisport = "multisport" in desc or "sports club" in desc
                        if not (is_football or is_multisport):
                            continue
                        
                        # Score the result quality
                        desc_quality = len(desc)
                        
                        # Big boost for city match in description
                        if city and city.lower() in desc:
                            desc_quality += 200
                            activity.logger.info(f"✅ City match: {qid} ({label}) - {desc}")
                            return qid  # Perfect match - return immediately
                        
                        # Boost for country match
                        if country and country.lower() in desc:
                            desc_quality += 100
                        
                        # Boost for location mention
                        if " in " in desc:
                            desc_quality += 50
                        
                        if desc_quality > best_desc_length:
                            best_candidate = qid
                            best_desc_length = desc_quality
                            activity.logger.debug(f"📊 Better candidate: {qid} ({label}) - desc_quality={desc_quality}")
                        
                        # If we have a country match, that's good enough
                        if country and country.lower() in desc:
                            activity.logger.info(f"✅ Country match: {qid} ({label}) - {desc}")
                            return qid
                    else:
                        if "national" not in desc or ("football" not in desc and "soccer" not in desc):
                            continue
                        # For national teams, first valid match is usually correct
                        return qid
                    
            except Exception as e:
                activity.logger.warning(f"⚠️ Search error for '{search_term}': {e}")
                continue
    
    # Return best candidate found (even if not perfect)
    if best_candidate:
        activity.logger.info(f"📋 Best candidate: {best_candidate} (desc_quality={best_desc_length})")
    return best_candidate


async def _fetch_wikidata_aliases(qid: str) -> List[str]:
    """Fetch ENGLISH aliases only from Wikidata entity."""
    aliases = []
    try:
        async with httpx.AsyncClient(timeout=10.0, headers=HEADERS) as client:
            response = await client.get(f"{WIKIDATA_ENTITY_URL}/{qid}.json")
            response.raise_for_status()
            data = response.json()
            
            entity = data.get("entities", {}).get(qid, {})
            
            # Get English label only
            en_label = entity.get("labels", {}).get("en", {}).get("value")
            if en_label:
                aliases.append(en_label)
            
            # Get English aliases only
            en_aliases = entity.get("aliases", {}).get("en", [])
            for alias_entry in en_aliases:
                if alias_entry.get("value"):
                    aliases.append(alias_entry["value"])
            
            return aliases
            
    except Exception as e:
        activity.logger.warning(f"⚠️ Wikidata fetch error: {e}")
    
    return []


def _clean_wikidata_aliases(raw_aliases: List[str], team_name: str) -> List[str]:
    """
    Light cleanup of Wikidata aliases - just remove obvious junk.
    
    The LLM handles all the smart filtering (generic words, etc.)
    We only do basic cleanup here:
    - Remove non-ASCII aliases
    - Remove concatenated junk (FCBarcelona)
    - Normalize acronyms with periods (F.C. → FC)
    - Dedupe
    
    NO skip_words list - the LLM decides what's useful!
    """
    cleaned = set()
    
    for alias in raw_aliases:
        # Normalize unicode (remove diacritics for comparison)
        normalized = unicodedata.normalize('NFKD', alias)
        normalized = ''.join(c for c in normalized if not unicodedata.combining(c))
        
        # Skip non-ASCII (can't search Twitter effectively)
        if not all(c.isascii() or c.isspace() for c in normalized):
            continue
        
        # Skip very long aliases (probably descriptions, not names)
        if len(normalized) > 30:
            continue
        
        # Skip concatenated junk like "FCBarcelona" (camelCase without spaces)
        if len(normalized) > 5 and ' ' not in normalized and not normalized.isupper():
            has_camel = False
            for i in range(1, len(normalized)):
                if normalized[i-1].islower() and normalized[i].isupper():
                    has_camel = True
                    break
            if has_camel:
                continue
        
        # Normalize acronyms with periods: F.C. → FC, A.C. → AC
        if '.' in normalized:
            no_periods = normalized.replace('.', '').replace(' ', '').strip()
            if 2 <= len(no_periods) <= 6 and no_periods.isupper():
                cleaned.add(no_periods)
                continue
        
        # Keep the alias
        cleaned.add(normalized.strip())
    
    # Return as list, sorted by length (shorter = more likely useful)
    return sorted(cleaned, key=lambda x: (len(x), x.lower()))


def _parse_llm_response(text: str) -> Optional[List[str]]:
    """Parse JSON array from LLM response."""
    # Handle markdown wrapping
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
    
    start = text.find("[")
    end = text.rfind("]") + 1
    if start >= 0 and end > start:
        text = text[start:end]
    
    try:
        aliases = json.loads(text.strip())
        if isinstance(aliases, list) and len(aliases) >= 1:
            return [str(a).strip() for a in aliases]
    except json.JSONDecodeError:
        pass
    
    return None


def _get_cached_aliases(store, team_id: int) -> Optional[dict]:
    """Check MongoDB cache for existing aliases by team_id."""
    return store.get_team_alias(team_id)


def _cache_aliases(
    store, 
    team_id: int, 
    team_name: str,
    national: bool,
    twitter_aliases: List[str], 
    model: str,
    country: Optional[str] = None,
    city: Optional[str] = None,
    wikidata_qid: Optional[str] = None,
    wikidata_aliases: Optional[List[str]] = None
) -> None:
    """Store aliases in MongoDB cache using store helper method."""
    store.upsert_team_alias(
        team_id=team_id,
        team_name=team_name,
        national=national,
        twitter_aliases=twitter_aliases,
        model=model,
        country=country,
        city=city,
        wikidata_qid=wikidata_qid,
        wikidata_aliases=wikidata_aliases,
    )


@activity.defn
async def get_team_aliases(team_id: int, team_name: str, team_type: Optional[str] = None, country: str = None) -> List[str]:
    """
    Get team name aliases via Wikidata RAG + LLM with MongoDB caching.
    
    The LLM handles ALL intelligence:
    - Selecting best acronyms (MUFC, LFC, BVB)
    - Filtering generic words (FC, Club, Real)
    - For national teams: deriving nationality adjectives (Argentina → Argentine)
    
    Args:
        team_id: API-Football team ID (cache key)
        team_name: Full team name
        team_type: "club", "national", or None (auto-detect via API) - DEPRECATED, kept for backward compat
        country: Country name (for better search) - can be overridden by API data
    
    Returns:
        List of normalized aliases for Twitter search (max 5)
    """
    from src.data.mongo_store import FootyMongoStore
    from src.api.api_client import get_team_info
    from src.data.models import TeamAliasFields
    
    store = FootyMongoStore()
    
    # 1. Check cache first
    cached = _get_cached_aliases(store, team_id)
    if cached:
        aliases = cached.get(TeamAliasFields.TWITTER_ALIASES, [])
        activity.logger.info(f"📦 Cache hit: {aliases}")
        return aliases
    
    # 2. Get FULL team info from API (do this ONCE, use all the data)
    team_info = get_team_info(team_id)
    
    # Extract all useful data from API response
    api_country = None
    api_city = None
    national = False
    
    if team_info:
        team_data = team_info.get("team", {})
        venue_data = team_info.get("venue", {})
        
        national = team_data.get("national", False)
        api_country = team_data.get("country")  # e.g., "England"
        api_city = venue_data.get("city")       # e.g., "Newcastle upon Tyne"
        
        activity.logger.info(
            f"🔍 API team info: {team_name} | national={national} | "
            f"country={api_country} | city={api_city}"
        )
    else:
        # API failed, try to use the passed hints
        national = team_type == "national" if team_type else False
        activity.logger.warning(f"⚠️ API lookup failed for team {team_id}, using hints")
    
    # Use API country if available, otherwise use passed country
    effective_country = api_country or country
    
    team_type_str = "national" if national else "club"
    
    activity.logger.info(f"🔍 Getting aliases for team {team_id}: {team_name} ({team_type_str})")
    activity.logger.info(f"🔄 Cache miss, starting RAG pipeline...")
    
    # 3. RETRIEVE: Get aliases from Wikidata (pass country AND city for better search)
    wikidata_aliases = []
    qid = await _search_wikidata_qid(team_name, team_type_str, effective_country, api_city)
    if qid:
        activity.logger.info(f"📚 Found Wikidata QID: {qid}")
        wikidata_aliases = await _fetch_wikidata_aliases(qid)
        activity.logger.info(f"📚 Wikidata aliases ({len(wikidata_aliases)}): {wikidata_aliases[:10]}...")
    else:
        activity.logger.warning(f"⚠️ No Wikidata QID found for: {team_name}")
    
    # 4. Light cleanup (no skip_words - LLM decides what's useful)
    cleaned_aliases = _clean_wikidata_aliases(wikidata_aliases, team_name) if wikidata_aliases else []
    activity.logger.info(f"🧹 Cleaned aliases: {cleaned_aliases[:15]}...")
    
    # 5. AUGMENT + GENERATE: LLM selects best aliases
    # Use different prompts for clubs vs national teams
    system_prompt = NATIONAL_SYSTEM_PROMPT if national else CLUB_SYSTEM_PROMPT
    
    if cleaned_aliases or national:  # National teams can derive nationality even without Wikidata
        try:
            async with httpx.AsyncClient(timeout=15.0) as client:
                # Build context-rich prompt for the LLM
                if national:
                    prompt = f"""Team: {team_name} (NATIONAL TEAM)
Country: {effective_country or team_name}
Wikidata aliases: {json.dumps(cleaned_aliases) if cleaned_aliases else "[]"}

Select the best 3-5 Twitter search terms. REMEMBER: Derive the nationality adjective!
Output JSON array only. /no_think"""
                else:
                    prompt = f"""Team: {team_name} (CLUB)
City: {api_city or "Unknown"}
Country: {effective_country or "Unknown"}  
Wikidata aliases: {json.dumps(cleaned_aliases)}

Select the best 3-5 Twitter search terms. Include acronym if available.
Output JSON array only. /no_think"""
                
                activity.logger.info(f"🤖 Sending to LLM: {prompt[:100]}...")
                
                response = await client.post(
                    f"{LLAMA_URL}/v1/chat/completions",
                    json={
                        "messages": [
                            {"role": "system", "content": system_prompt},
                            {"role": "user", "content": prompt}
                        ],
                        "max_tokens": 150,
                        "temperature": 0.1
                    }
                )
                response.raise_for_status()
                
                result = response.json()
                text = result["choices"][0]["message"]["content"].strip()
                activity.logger.info(f"🤖 LLM response: {text}")
                
                llm_aliases = _parse_llm_response(text)
                
                if llm_aliases and len(llm_aliases) >= 1:
                    # Normalize all aliases (remove diacritics)
                    twitter_aliases = [_normalize_alias(a) for a in llm_aliases]
                    
                    # Enforce max limit
                    twitter_aliases = twitter_aliases[:MAX_ALIASES]
                    
                    activity.logger.info(f"✅ Final aliases ({len(twitter_aliases)}): {twitter_aliases}")
                    
                    # Cache with all data
                    _cache_aliases(
                        store, team_id, team_name, national, twitter_aliases,
                        LLAMA_MODEL, api_country, api_city, qid, wikidata_aliases
                    )
                    return twitter_aliases
                
                activity.logger.warning(f"⚠️ LLM returned invalid/empty: {text}")
                
        except httpx.ConnectError:
            activity.logger.warning("⚠️ LLM server not available, using fallback")
        except Exception as e:
            activity.logger.warning(f"⚠️ LLM error: {e}")
    
    # Fallback: Just use first few cleaned aliases + team name
    fallback = cleaned_aliases[:3] if cleaned_aliases else []
    
    # Add team name as fallback (most reliable search term)
    team_words = [w for w in team_name.split() if len(w) > 2 and w.lower() not in {'fc', 'ac', 'sc', 'cf'}]
    for word in team_words:
        if len(fallback) < MAX_ALIASES and word not in fallback:
            fallback.append(word)
    
    twitter_aliases = [_normalize_alias(a) for a in fallback][:MAX_ALIASES]
    
    activity.logger.info(f"📋 Fallback aliases ({len(twitter_aliases)}): {twitter_aliases}")
    
    # Only cache if we got something
    if twitter_aliases:
        _cache_aliases(
            store, team_id, team_name, national, twitter_aliases,
            "fallback", api_country, api_city, qid, wikidata_aliases
        )
    
    return twitter_aliases


@activity.defn
async def save_team_aliases(fixture_id: int, event_id: str, aliases: List[str]) -> bool:
    """
    Save resolved aliases to event in MongoDB for debugging/visibility.
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


@activity.defn
async def get_cached_team_aliases(team_id: int) -> List[str]:
    """
    Get cached aliases for a team. Returns empty list if not cached.
    
    Used by monitor to quickly lookup pre-cached aliases.
    """
    from src.data.mongo_store import FootyMongoStore
    from src.data.models import TeamAliasFields
    
    store = FootyMongoStore()
    cached = _get_cached_aliases(store, team_id)
    
    if cached:
        return cached.get(TeamAliasFields.TWITTER_ALIASES, [])
    
    return []
