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
import asyncio
import httpx
import json
import os
import unicodedata
from datetime import datetime, timezone

from src.utils.config import LLAMA_CHAT_URL
from src.utils.footy_logging import log

MODULE = "rag"

# Use centralized config
LLAMA_URL = LLAMA_CHAT_URL

# Semaphore to limit concurrent LLM requests per worker process.
# llama-chat runs --parallel 4 with 2 workers, so allow 2 concurrent per worker.
_LLM_SEMAPHORE = asyncio.Semaphore(2)
LLAMA_MODEL = os.getenv("LLAMA_MODEL", "Qwen3-8B")

# Wikidata API endpoints
WIKIDATA_SEARCH_URL = "https://www.wikidata.org/w/api.php"
WIKIDATA_ENTITY_URL = "https://www.wikidata.org/wiki/Special:EntityData"

# User-Agent required by Wikidata API
HEADERS = {
    "User-Agent": "FoundFooty/1.0 (https://github.com/vedanta/found-footy; contact@example.com)"
}

# Maximum aliases for Twitter search (now using OR syntax, so just 1 search per attempt)
# Limit is mainly to keep queries focused and avoid false matches
MAX_ALIASES = 10

# =============================================================================
# LLM System Prompts - Different for clubs vs national teams
# =============================================================================

CLUB_SYSTEM_PROMPT = """Pick UP TO 10 words from the list for Twitter search. Pick all good options.

Priority:
1. City/team name (Liverpool, Bremen, Newcastle, Celta, Vigo, Atletico, Madrid)
2. Distinctive team words (Real for Real Madrid, City for Man City, United for Man United)
3. Acronyms 3+ letters (LFC, MUFC, BVB, PSG, ATM)
4. Nicknames (Spurs, Juve, Barca, Atleti, Gunners, Reds)

SKIP generic words: Club, SAD, Association, Society, Futbol, Football

Output JSON array only. Include all useful words up to 10."""

NATIONAL_SYSTEM_PROMPT = """Pick 3-5 words for Twitter search for a national team.

MUST INCLUDE:
1. Country name FIRST (Brazil, France, Argentina)
2. Nationality adjective (Brazilian, French, Argentine)
3. Nicknames from the list if available (Selecao, Albiceleste, Bleus)

NEVER pick: National, Team, Football, Selection

Output JSON array only. Country name must be first."""


# In-memory cache for country variations (populated by LLM)
_country_variations_cache: dict = {}


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


async def _get_country_variations(country: str) -> List[str]:
    """
    Get country name variations using LLM (for Wikidata search matching).
    
    Examples:
        Spain → ["spain", "spanish", "espana", "espanol"]
        Germany → ["germany", "german", "deutschland", "deutsch"]
        England → ["england", "english", "britain", "british"]
    
    Results are cached in memory to avoid repeated LLM calls.
    """
    if not country:
        return []
    
    country_lower = country.lower()
    
    # Check cache first
    if country_lower in _country_variations_cache:
        return _country_variations_cache[country_lower]
    
    # Default: just the country name itself
    variations = [country_lower]
    
    try:
        async with httpx.AsyncClient(timeout=10.0) as client:
            prompt = f"""Country: {country}

List ALL common variations of this country name that might appear in text, including:
- The country name itself
- Nationality adjective (Spanish, German, French, etc.)
- Native language name (España, Deutschland, etc.)
- Common abbreviations

Return JSON array of lowercase strings only. No explanation.
Example for Spain: ["spain", "spanish", "espana"]
Example for Germany: ["germany", "german", "deutschland"]

/no_think"""
            
            response = await client.post(
                f"{LLAMA_URL}/v1/chat/completions",
                json={
                    "messages": [{"role": "user", "content": prompt}],
                    "max_tokens": 100,
                    "temperature": 0.1
                }
            )
            response.raise_for_status()
            
            result = response.json()
            text = result["choices"][0]["message"]["content"].strip()
            
            # Parse JSON array
            parsed = _parse_llm_response(text)
            if parsed:
                variations = [v.lower().strip() for v in parsed if v.strip()]
                # Always include the original country name
                if country_lower not in variations:
                    variations.insert(0, country_lower)
                    
    except Exception as e:
        # Fallback: simple prefix match (spain matches spanish)
        pass
    
    # Cache the result
    _country_variations_cache[country_lower] = variations
    return variations


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
    
    # Get country variations for matching (e.g., Spain → ["spain", "spanish", "espana"])
    country_variations = await _get_country_variations(country) if country else []
    
    # Build search terms - FOOTBALL-SPECIFIC FIRST
    search_terms = []
    
    if team_type == "club":
        # 1. Try football-specific searches FIRST (most likely to find the club)
        search_terms.append(f"{normalized} FC")
        search_terms.append(f"{normalized} football club")
        search_terms.append(f"FC {normalized}")
        # 2. Try with city (if available - very specific)
        if city:
            search_terms.append(f"{normalized} {city}")
            search_terms.append(f"{normalized} FC {city}")
        # 3. Try with country
        if country:
            search_terms.append(f"{normalized} FC {country}")
            search_terms.append(f"{normalized} {country} football")
        # 4. Common suffixes
        search_terms.append(f"{normalized} United")
        search_terms.append(f"{normalized} City")
        # 5. Bare name as last resort
        search_terms.append(normalized)
    else:
        # National teams
        name_for_search = "United States" if normalized.upper() == "USA" else normalized
        search_terms.append(f"{name_for_search} national football team")
        search_terms.append(f"{name_for_search} men's national football team")
        search_terms.append(f"{name_for_search} national soccer team")
    
    skip_keywords = ["women", "femen", "reserve", "youth", "junior", " b ", " c ", 
                     "under-", "u-19", "u-21", "academy", "futsal", "beach", "basketball",
                     "stadium", "arena", "list article", "disambiguation"]
    
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
                        
                        # Check for country match using LLM-generated variations
                        # e.g., country_variations for "Spain" = ["spain", "spanish", "espana"]
                        country_matches = False
                        if country_variations:
                            for variation in country_variations:
                                if variation in desc:
                                    country_matches = True
                                    break
                        
                        # Big boost for city match in description
                        # Also handle spelling variations (Sevilla vs Seville)
                        city_matches = False
                        if city:
                            city_lower = city.lower()
                            # Exact match or first 5 chars match (Sevilla/Seville, München/Munich)
                            if city_lower in desc or (len(city_lower) >= 5 and city_lower[:5] in desc):
                                city_matches = True
                        
                        if city_matches:
                            desc_quality += 200
                            log.info(activity.logger, MODULE, "city_match", "City match found",
                                     qid=qid, label=label, description=desc)
                            return qid  # Perfect match - return immediately
                        
                        # Boost for country match
                        if country_matches:
                            desc_quality += 100
                        
                        # Boost for location mention
                        if " in " in desc:
                            desc_quality += 50
                        
                        if desc_quality > best_desc_length:
                            best_candidate = qid
                            best_desc_length = desc_quality
                            log.debug(activity.logger, MODULE, "better_candidate", "Better candidate found",
                                      qid=qid, label=label, desc_quality=desc_quality)
                        
                        # If we have a country match, that's good enough
                        if country_matches:
                            log.info(activity.logger, MODULE, "country_match", "Country match found",
                                     qid=qid, label=label, description=desc)
                            return qid
                    else:
                        if "national" not in desc or ("football" not in desc and "soccer" not in desc):
                            continue
                        # For national teams, first valid match is usually correct
                        return qid
                    
            except Exception as e:
                log.warning(activity.logger, MODULE, "search_error", "Wikidata search error",
                            search_term=search_term, error=str(e))
                continue
    
    # Return best candidate found (even if not perfect)
    if best_candidate:
        log.info(activity.logger, MODULE, "best_candidate", "Best candidate selected",
                 qid=best_candidate, desc_quality=best_desc_length,
                 search_terms_tried=len(search_terms))
    else:
        log.warning(activity.logger, MODULE, "no_wikidata_match", "No Wikidata match found after all search terms",
                    team_name=team_name, team_type=team_type, search_terms_tried=len(search_terms))
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
        log.warning(activity.logger, MODULE, "wikidata_fetch_error", "Wikidata fetch error",
                    error=str(e))
    
    return []


def _clean_wikidata_aliases(raw_aliases: List[str]) -> List[str]:
    """
    Clean and normalize aliases into individual words for LLM selection.
    
    Processing steps:
    1. Normalize unicode (remove diacritics)
    2. Drop periods entirely
    3. Replace dashes with spaces
    4. Drop CamelCase concatenations (SevillaFC, FCBarcelona)
    5. Split multi-word aliases into individual words
    6. Dedupe words
    
    The LLM handles all the smart filtering (generic words, etc.)
    """
    words = set()
    
    for alias in raw_aliases:
        # Step 1: Normalize unicode (remove diacritics)
        normalized = unicodedata.normalize('NFKD', alias)
        normalized = ''.join(c for c in normalized if not unicodedata.combining(c))
        
        # Skip non-ASCII (can't search Twitter effectively)
        if not all(c.isascii() or c.isspace() or c == '-' for c in normalized):
            continue
        
        # Skip very long aliases (probably descriptions, not names)
        if len(normalized) > 40:
            continue
        
        # Step 2: Drop periods and commas entirely
        normalized = normalized.replace('.', '').replace(',', '')
        
        # Step 3: Replace dashes with spaces
        normalized = normalized.replace('-', ' ')
        
        # Step 4: Drop CamelCase concatenations 
        # Examples: SevillaFC, FCBarcelona, ParisFC, ASRoma, TorinoFC
        # Detect: lowercase→uppercase OR uppercase→lowercase transitions (except at start)
        # But keep pure acronyms (PSG, LFC) and normal words (Liverpool, Torino)
        if len(normalized) > 4 and ' ' not in normalized:
            # Skip if all same case (pure acronym or normal word)
            if normalized.isupper() or normalized.islower():
                pass  # Keep it, will be split below
            else:
                # Mixed case - check for concatenation pattern
                has_concat = False
                for i in range(1, len(normalized)):
                    prev_lower = normalized[i-1].islower()
                    prev_upper = normalized[i-1].isupper()
                    curr_lower = normalized[i].islower()
                    curr_upper = normalized[i].isupper()
                    # lower→UPPER (SevillaFC) or UPPER→lower mid-word (ASRoma, but not Roma)
                    if (prev_lower and curr_upper) or (prev_upper and curr_lower and i > 1):
                        has_concat = True
                        break
                if has_concat:
                    continue
        
        # Step 5: Split into individual words
        for word in normalized.split():
            word = word.strip()
            
            # Skip words 2 chars or less (FC, AC, RC, SC, etc. are all useless)
            if len(word) <= 2:
                continue
            
            # Skip pure numbers (like "1899", "1907")
            if word.isdigit():
                continue
            
            # Skip generic organizational words (useless for Twitter search)
            if word.lower() in {'club', 'football', 'futbol', 'calcio', 'fussball', 
                               'association', 'society', 'sportiva', 'associazione',
                               'sad', 'sport', 'the'}:
                continue
            
            # Step 6: Add to set (automatic dedup)
            words.add(word)
    
    # Return as list, sorted: acronyms first (all caps), then by length
    return sorted(words, key=lambda x: (not x.isupper(), len(x), x.lower()))


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
        log.info(activity.logger, MODULE, "cache_hit", "Cache hit",
                 aliases=aliases)
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
        
        log.info(activity.logger, MODULE, "api_team_info", "API team info retrieved",
                 team_name=team_name, national=national, country=api_country, city=api_city)
    else:
        # API failed, try to use the passed hints
        national = team_type == "national" if team_type else False
        log.warning(activity.logger, MODULE, "api_lookup_failed", "API lookup failed, using hints",
                    team_id=team_id)
    
    # Use API country if available, otherwise use passed country
    effective_country = api_country or country
    
    team_type_str = "national" if national else "club"
    
    log.info(activity.logger, MODULE, "get_aliases_started", "Getting aliases for team",
             team_id=team_id, team_name=team_name, team_type=team_type_str)
    log.info(activity.logger, MODULE, "cache_miss", "Cache miss, starting RAG pipeline")
    
    # 3. RETRIEVE: Get aliases from Wikidata (pass country AND city for better search)
    wikidata_aliases = []
    qid = await _search_wikidata_qid(team_name, team_type_str, effective_country, api_city)
    if qid:
        log.info(activity.logger, MODULE, "wikidata_qid_found", "Found Wikidata QID",
                 qid=qid)
        wikidata_aliases = await _fetch_wikidata_aliases(qid)
        log.info(activity.logger, MODULE, "wikidata_aliases", "Wikidata aliases retrieved",
                 count=len(wikidata_aliases), sample=wikidata_aliases[:10])
    else:
        log.warning(activity.logger, MODULE, "no_wikidata_qid", "No Wikidata QID found",
                    team_name=team_name)
    
    # 4. Process ALL sources into single words: team name + Wikidata aliases
    # The team name from API is treated the same as Wikidata aliases
    all_raw_aliases = [team_name] + wikidata_aliases
    cleaned_aliases = _clean_wikidata_aliases(all_raw_aliases)
    log.info(activity.logger, MODULE, "cleaned_aliases", "Cleaned word list",
             count=len(cleaned_aliases), sample=cleaned_aliases[:15])
    
    # 5. AUGMENT + GENERATE: LLM selects best aliases
    # Use different prompts for clubs vs national teams
    system_prompt = NATIONAL_SYSTEM_PROMPT if national else CLUB_SYSTEM_PROMPT
    
    if cleaned_aliases or national:  # National teams can derive nationality even without Wikidata
        try:
            # Build context-rich prompt for the LLM
            if national:
                prompt = f"""Country: {effective_country or team_name}
Available words: {json.dumps(cleaned_aliases) if cleaned_aliases else "[]"}

Pick 3-5 words from the list above. You MUST also include the country name and nationality adjective.
Output JSON array only. /no_think"""
            else:
                prompt = f"""Team: {team_name}
Available words: {json.dumps(cleaned_aliases)}

Pick 3-5 words from the list above. Only return words that appear in the list.
Output JSON array only. /no_think"""
            
            log.info(activity.logger, MODULE, "llm_request", "Sending to LLM",
                     prompt_preview=prompt[:100])
            
            result = None
            for attempt in range(1, 4):
                try:
                    async with _LLM_SEMAPHORE:
                        async with httpx.AsyncClient(timeout=45.0) as client:
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
                            break
                except (httpx.TimeoutException, httpx.ReadError) as retry_err:
                    if attempt < 3:
                        wait = 3 * attempt
                        log.warning(activity.logger, MODULE, "llm_retry",
                                    f"LLM {type(retry_err).__name__}, retrying in {wait}s",
                                    attempt=attempt, error_type=type(retry_err).__name__)
                        await asyncio.sleep(wait)
                        continue
                    raise
            
            if not result:
                raise httpx.TimeoutException("All LLM retries exhausted")
            
            text = result["choices"][0]["message"]["content"].strip()
            log.info(activity.logger, MODULE, "llm_response", "LLM response received",
                     response=text)
            
            llm_aliases = _parse_llm_response(text)
            
            if llm_aliases and len(llm_aliases) >= 1:
                # Normalize and split any multi-word responses into single words
                # Also filter out 2-letter-or-less words (FC, AC, RC, etc.)
                allowed_words_lower = {w.lower() for w in cleaned_aliases}
                twitter_aliases = []
                hallucinated_count = 0
                for alias in llm_aliases:
                    normalized = _normalize_alias(alias)
                    for word in normalized.split():
                        word = word.strip()
                        # Skip 2-letter-or-less words and duplicates
                        if len(word) <= 2 or word in twitter_aliases:
                            continue
                        
                        # For NATIONAL teams: Trust the LLM to generate nationality adjectives
                        # (e.g., Argentina → Argentine, Argentinian)
                        # For CLUBS: Only accept words from Wikidata to prevent hallucination
                        if national:
                            # National teams: accept LLM output (it knows nationality adjectives)
                            twitter_aliases.append(word)
                        elif word.lower() in allowed_words_lower:
                            # Clubs: must be in Wikidata list
                            twitter_aliases.append(word)
                        else:
                            hallucinated_count += 1
                            log.debug(activity.logger, MODULE, "hallucination_rejected", "Rejected hallucinated word",
                                        word=word)
                
                # Log summary of hallucinations if any
                if hallucinated_count > 0:
                    log.warning(activity.logger, MODULE, "hallucinations_filtered", 
                                f"Filtered {hallucinated_count} hallucinated words from LLM response",
                                rejected=hallucinated_count, accepted=len(twitter_aliases))
                
                # Enforce max limit
                twitter_aliases = twitter_aliases[:MAX_ALIASES]
                
                log.info(activity.logger, MODULE, "final_aliases", "Final aliases selected",
                         count=len(twitter_aliases), aliases=twitter_aliases)
                
                # Cache with all data
                _cache_aliases(
                    store, team_id, team_name, national, twitter_aliases,
                    LLAMA_MODEL, api_country, api_city, qid, wikidata_aliases
                )
                return twitter_aliases
            
            log.warning(activity.logger, MODULE, "llm_invalid", "LLM returned invalid/empty",
                        response=text)
                
        except httpx.ConnectError:
            log.warning(activity.logger, MODULE, "llm_unavailable", "LLM server not available, using fallback",
                        llm_url=LLAMA_URL)
        except Exception as e:
            log.warning(activity.logger, MODULE, "llm_error", "LLM error",
                        error=str(e))
    
    # Fallback: Just use first few cleaned aliases + team name
    fallback = cleaned_aliases[:3] if cleaned_aliases else []
    
    # Add team name as fallback (most reliable search term)
    team_words = [w for w in team_name.split() if len(w) > 2 and w.lower() not in {'fc', 'ac', 'sc', 'cf'}]
    for word in team_words:
        if len(fallback) < MAX_ALIASES and word not in fallback:
            fallback.append(word)
    
    twitter_aliases = [_normalize_alias(a) for a in fallback][:MAX_ALIASES]
    
    log.info(activity.logger, MODULE, "fallback_aliases", "Using fallback aliases",
             count=len(twitter_aliases), aliases=twitter_aliases)
    
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
    
    log.info(activity.logger, MODULE, "save_aliases_started", "Saving aliases",
             event_id=event_id, aliases=aliases)
    
    store = FootyMongoStore()
    
    try:
        result = store.fixtures_active.update_one(
            {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
            {"$set": {f"events.$.{EventFields.TWITTER_ALIASES}": aliases}}
        )
        
        if result.modified_count > 0:
            log.info(activity.logger, MODULE, "save_aliases_success", "Saved aliases",
                     event_id=event_id)
            return True
        else:
            log.warning(activity.logger, MODULE, "save_aliases_no_modify", "No document modified",
                        event_id=event_id)
            return False
            
    except Exception as e:
        log.error(activity.logger, MODULE, "save_aliases_error", "Error saving aliases",
                  error=str(e))
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
