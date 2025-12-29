"""
RAG Activities - Team Alias Lookup with Wikidata + LLM

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
# Using llama.cpp server with OpenAI-compatible API
LLAMA_URL = os.getenv("LLAMA_URL", "http://llama-server:8080")
LLAMA_MODEL = os.getenv("LLAMA_MODEL", "Qwen3-8B")

# Wikidata API endpoints
WIKIDATA_SEARCH_URL = "https://www.wikidata.org/w/api.php"
WIKIDATA_ENTITY_URL = "https://www.wikidata.org/wiki/Special:EntityData"

# User-Agent required by Wikidata API
HEADERS = {
    "User-Agent": "FoundFooty/1.0 (https://github.com/vedanta/found-footy; contact@example.com)"
}

# RAG System Prompt - LLM selects from preprocessed words
SYSTEM_PROMPT = """Select the best words for Twitter search from the provided list.

PRIORITY:
1. Acronyms (MUFC, BVB, PSG, ATM, LFC) - most valuable
2. Short nicknames (Spurs, Juve, Barca, Atleti, Bayern)
3. Distinctive words (Devils, Blues, Gunners, Reds)

RULES:
- ONLY select from the provided list
- Return ALL good options (no limit)
- Skip generic words

Output: JSON array ["MUFC", "Utd", "Devils"]"""

# Nationality mapping for national teams
NATIONALITY_MAP = {
    'argentina': ['Argentinian', 'Argentine'],
    'brazil': ['Brazilian', 'Brasil'],
    'france': ['French'],
    'england': ['English'],
    'germany': ['German', 'Deutschland'],
    'spain': ['Spanish', 'Espana'],
    'italy': ['Italian', 'Italia'],
    'portugal': ['Portuguese'],
    'netherlands': ['Dutch', 'Holland'],
    'belgium': ['Belgian'],
    'croatia': ['Croatian', 'Hrvatska'],
    'morocco': ['Moroccan'],
    'colombia': ['Colombian'],
    'usa': ['American', 'America'],
    'mexico': ['Mexican'],
    'japan': ['Japanese'],
    'south korea': ['Korean'],
    'australia': ['Australian'],
    'poland': ['Polish'],
    'switzerland': ['Swiss'],
    'denmark': ['Danish'],
    'sweden': ['Swedish'],
    'norway': ['Norwegian'],
    'wales': ['Welsh'],
    'scotland': ['Scottish'],
    'ireland': ['Irish'],
    'turkey': ['Turkish'],
    'greece': ['Greek'],
    'serbia': ['Serbian'],
    'ukraine': ['Ukrainian'],
    'czech republic': ['Czech'],
    'austria': ['Austrian'],
    'hungary': ['Hungarian'],
    'romania': ['Romanian'],
    'senegal': ['Senegalese'],
    'ghana': ['Ghanaian'],
    'nigeria': ['Nigerian'],
    'cameroon': ['Cameroonian'],
    'egypt': ['Egyptian'],
    'algeria': ['Algerian'],
    'tunisia': ['Tunisian'],
    'uruguay': ['Uruguayan'],
    'chile': ['Chilean'],
    'peru': ['Peruvian'],
    'ecuador': ['Ecuadorian'],
    'canada': ['Canadian'],
}


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


def _preprocess_aliases_to_words(raw_aliases: List[str], team_name: str, team_type: str = "club") -> List[str]:
    """
    Heavy preprocessing to convert raw Wikidata aliases to clean single words.
    """
    # Words to skip
    skip_words = {
        'fc', 'club', 'the', 'la', 'el', 'los', 'las', 'le', 'les', 'de', 'of', 
        'and', 'del', 'der', 'die', 'das', 'ac', 'as', 'sc', 'cf', 'cd', 'ss',
        'futbol', 'football', 'soccer', 'calcio', 'association', 'sporting',
        'national', 'team', "men's", 'mens', 'royal', 'real', 'nt', 'united',
        'one', 'red', 'west', 'sport', 'three'
    }
    
    team_words = {w.lower() for w in team_name.split()}
    
    words = set()
    acronyms = set()
    
    for alias in raw_aliases:
        # Normalize unicode
        normalized = unicodedata.normalize('NFKD', alias)
        normalized = ''.join(c for c in normalized if not unicodedata.combining(c))
        
        # Skip non-ASCII
        if not all(c.isascii() or c.isspace() for c in normalized):
            continue
        
        # Skip concatenated junk like "FCBarcelona"
        if len(normalized) > 4 and ' ' not in normalized and not normalized.isupper():
            is_concat = False
            for i in range(1, len(normalized)):
                if normalized[i-1].islower() and normalized[i].isupper():
                    is_concat = True
                    break
            if is_concat:
                continue
            if len(normalized) > 3 and normalized[:2].isupper() and normalized[2].isupper() and normalized[3:4].islower():
                continue
        
        # Handle acronyms with periods
        if '.' in normalized:
            no_periods = normalized.replace('.', '').replace(' ', '').strip()
            if 2 <= len(no_periods) <= 6 and no_periods.isupper():
                if no_periods.lower() not in acronyms:
                    acronyms.add(no_periods.lower())
                    words.add(no_periods)
            continue
        
        # Check if it's a clean acronym
        no_space = normalized.replace(' ', '')
        if no_space.isupper() and 2 <= len(no_space) <= 6 and ' ' not in normalized:
            if no_space in {'SAD', 'PLC', 'LTD', 'INC', 'IM'}:
                continue
            if no_space.lower() not in acronyms:
                acronyms.add(no_space.lower())
                words.add(no_space)
            continue
        
        # Skip weird spaced "acronyms"
        if len(normalized) <= 4 and ' ' in normalized:
            continue
        
        # Split multi-word aliases
        for word in normalized.split():
            word_clean = ''.join(c for c in word if c.isalnum())
            word_lower = word_clean.lower()
            
            if len(word_clean) < 2:
                continue
            if word_lower in skip_words:
                continue
            if word_lower in team_words:
                continue
            if word_lower in {'sad', 'plc', 'ltd', 'inc', 'ev'}:
                continue
            
            words.add(word_clean)
    
    # For national teams, add nationality adjectives
    if team_type == "national":
        team_lower = team_name.lower()
        if team_lower in NATIONALITY_MAP:
            for adj in NATIONALITY_MAP[team_lower]:
                if adj.lower() not in {w.lower() for w in words}:
                    words.add(adj)
    
    # Filter 1-char words
    words = {w for w in words if len(w) > 1}
    
    # Sort: acronyms first, then by length
    result = sorted(words, key=lambda w: (not w.isupper(), len(w), w.lower()))
    
    return result


def _add_team_name_words(aliases: List[str], team_name: str) -> List[str]:
    """Add words from team name to the BEGINNING of the alias list.
    
    Team name words are added first because they're the most reliable search terms.
    E.g., "Liverpool" for Liverpool FC, "Madrid" for Real Madrid.
    """
    aliases_lower = {a.lower() for a in aliases}
    team_words = []  # Collect team name words to prepend
    
    name_normalized = team_name.replace('-', ' ')
    words = name_normalized.split()
    
    for word in words:
        word_clean = _normalize_alias(word)
        word_lower = word_clean.lower()
        
        if len(word_clean) < 2:
            continue
        
        # Skip short all-caps (FC, AC) unless only word
        if word_clean.isupper() and len(word_clean) <= 3 and len(words) > 1:
            continue
        
        if word_lower in aliases_lower:
            continue
        
        team_words.append(word_clean)
        aliases_lower.add(word_lower)
    
    # Prepend team name words at the beginning
    return team_words + list(aliases)


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
    
    Args:
        team_id: API-Football team ID (cache key)
        team_name: Full team name
        team_type: "club", "national", or None (auto-detect via API) - DEPRECATED, kept for backward compat
        country: Country name (for better search) - can be overridden by API data
    
    Returns:
        List of normalized aliases for Twitter search
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
        activity.logger.info(f"📚 Wikidata aliases ({len(wikidata_aliases)}): {wikidata_aliases[:5]}...")
    else:
        activity.logger.warning(f"⚠️ No Wikidata QID found for: {team_name}")
    
    # 4. Preprocess to single words
    preprocessed = _preprocess_aliases_to_words(wikidata_aliases, team_name, team_type_str) if wikidata_aliases else []
    activity.logger.info(f"🔧 Preprocessed to words: {preprocessed}")
    
    # 5. AUGMENT + GENERATE: LLM selects from preprocessed words
    if preprocessed:
        try:
            # Short timeout - fail fast to fallback if LLM is frozen
            async with httpx.AsyncClient(timeout=10.0) as client:
                prompt = f"""Words: {json.dumps(preprocessed)}

Select the best words for Twitter search. Return a JSON array. /no_think"""
                
                response = await client.post(
                    f"{LLAMA_URL}/v1/chat/completions",
                    json={
                        "messages": [
                            {"role": "system", "content": SYSTEM_PROMPT},
                            {"role": "user", "content": prompt}
                        ],
                        "max_tokens": 100,
                        "temperature": 0.1
                    }
                )
                response.raise_for_status()
                
                result = response.json()
                text = result["choices"][0]["message"]["content"].strip()
                
                llm_aliases = _parse_llm_response(text)
                
                if llm_aliases:
                    # Validate: must be from preprocessed list
                    preprocessed_lower = {p.lower() for p in preprocessed}
                    valid = [a for a in llm_aliases if a.lower() in preprocessed_lower]
                    
                    if valid:
                        activity.logger.info(f"✅ LLM selected: {valid}")
                        
                        # Add team name words
                        valid = _add_team_name_words(valid, team_name)
                        
                        # Add nationality adjectives for national teams
                        if national:
                            team_lower = team_name.lower()
                            if team_lower in NATIONALITY_MAP:
                                valid_lower = {v.lower() for v in valid}
                                for adj in NATIONALITY_MAP[team_lower]:
                                    if adj.lower() not in valid_lower:
                                        valid.append(adj)
                        
                        # Normalize all aliases
                        twitter_aliases = [_normalize_alias(a) for a in valid]
                        
                        activity.logger.info(f"✅ Final aliases: {twitter_aliases}")
                        
                        # Cache with all API data
                        _cache_aliases(
                            store, team_id, team_name, national, twitter_aliases,
                            LLAMA_MODEL, api_country, api_city, qid, wikidata_aliases
                        )
                        return twitter_aliases
                
                activity.logger.warning(f"⚠️ LLM returned invalid format: {text}")
                
        except httpx.ConnectError:
            activity.logger.warning("⚠️ LLM server not available, using fallback")
        except Exception as e:
            activity.logger.warning(f"⚠️ LLM error: {e}")
    
    # Fallback: use preprocessed + team name words
    fallback = preprocessed[:5] if preprocessed else []
    fallback = _add_team_name_words(fallback, team_name)
    
    # Add nationality adjectives for national teams
    if national:
        team_lower = team_name.lower()
        if team_lower in NATIONALITY_MAP:
            fallback_lower = {f.lower() for f in fallback}
            for adj in NATIONALITY_MAP[team_lower]:
                if adj.lower() not in fallback_lower:
                    fallback.append(adj)
    
    twitter_aliases = [_normalize_alias(a) for a in fallback]
    activity.logger.info(f"📋 Fallback aliases: {twitter_aliases}")
    
    # Only cache if we got Wikidata data
    if qid:
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
