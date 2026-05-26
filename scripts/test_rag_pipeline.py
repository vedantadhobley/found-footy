#!/usr/bin/env python3
"""
RAG Pipeline Test - Tests Wikidata + Ollama alias generation for all tracked teams.

This script tests the RAG pipeline WITHOUT Temporal by directly calling the
underlying functions. Run inside the worker container to access Ollama.

Usage:
    docker exec found-footy-dev-worker python /workspace/tests/test_rag_pipeline.py
    
    # Test specific team
    docker exec found-footy-dev-worker python /workspace/tests/test_rag_pipeline.py --team "Real Madrid"
    
    # Test clubs only (skip national teams)
    docker exec found-footy-dev-worker python /workspace/tests/test_rag_pipeline.py --clubs-only
"""
import asyncio
import argparse
import json
import sys
import requests
import unicodedata
from typing import List, Optional, Tuple

# Add src to path for imports
sys.path.insert(0, "/workspace/src")
from utils.team_data import get_uefa_teams, get_fifa_teams
USE_DOCKER_EXEC = True

# Wikidata API endpoints
WIKIDATA_SEARCH_URL = "https://www.wikidata.org/w/api.php"
WIKIDATA_ENTITY_URL = "https://www.wikidata.org/wiki/Special:EntityData"

# User-Agent required by Wikidata API
HEADERS = {
    "User-Agent": "FoundFooty/1.0 (https://github.com/vedanta/found-footy; contact@example.com)"
}


def build_tracked_teams():
    """Build tracked teams dict from team_data.py (the source of truth)."""
    tracked = {}
    
    # UEFA clubs - include country for better Wikidata search
    for team_id, data in get_uefa_teams().items():
        tracked[team_id] = {
            "name": data["name"], 
            "type": "club",
            "country": data.get("country", "")
        }
    
    # FIFA national teams
    for team_id, data in get_fifa_teams().items():
        tracked[team_id] = {
            "name": data["name"], 
            "type": "national",
            "country": data.get("country", "")
        }
    
    return tracked


# Load teams from the actual source of truth (team_data.py)
TRACKED_TEAMS = build_tracked_teams()

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


def normalize_alias(alias: str) -> str:
    """Remove diacritics and strip article prefixes: Atlético → Atletico, The Bees → Bees"""
    # Remove diacritics
    normalized = unicodedata.normalize('NFD', alias)
    result = ''.join(c for c in normalized if unicodedata.category(c) != 'Mn')
    
    # Strip article prefixes (The, La, El, Los, Las, Le, Les, Gli, Die, Der, Das)
    # Case-insensitive but preserve original case of the rest
    for prefix in ('The ', 'La ', 'El ', 'Los ', 'Las ', 'Le ', 'Les ', 'Gli ', 'Die ', 'Der ', 'Das '):
        if result.lower().startswith(prefix.lower()):
            result = result[len(prefix):]
            break
    
    return result.strip()


async def search_wikidata_qid(team_name: str, team_type: str, country: str = None) -> Optional[str]:
    """
    Search Wikidata for team QID using the simple search API.
    
    Strategy: Try multiple search terms in order, pick first good football result.
    This mirrors how you'd search on Google: "Real Madrid football club"
    """
    
    # Normalize for search (remove diacritics)
    normalized = normalize_alias(team_name)
    
    # Build search terms to try in order (most specific first)
    search_terms = []
    
    if team_type == "club":
        # Most reliable: "{name} football club" - works for Arsenal, Juventus, etc.
        search_terms.append(f"{normalized} football club")
        # Try with FC prefix/suffix (works well for continental clubs)
        search_terms.append(f"FC {normalized}")
        search_terms.append(f"{normalized} FC")
        # Try with country context
        if country:
            search_terms.append(f"{normalized} {country} football")
        # Just the name as last resort
        search_terms.append(normalized)
    else:
        # National teams
        name_for_search = "United States" if normalized.upper() == "USA" else normalized
        search_terms.append(f"{name_for_search} national football team")
        search_terms.append(f"{name_for_search} men's national football team")
        search_terms.append(f"{name_for_search} national soccer team")
    
    # Skip keywords for filtering bad results
    skip_keywords = ["women", "femen", "reserve", "youth", "junior", " b ", " c ", 
                     "under-", "u-19", "u-21", "academy", "futsal", "beach", "basketball"]
    
    for search_term in search_terms:
        try:
            response = requests.get(
                WIKIDATA_SEARCH_URL,
                params={
                    "action": "wbsearchentities",
                    "search": search_term,
                    "language": "en",
                    "format": "json",
                    "type": "item",
                    "limit": 10
                },
                headers=HEADERS,
                timeout=10
            )
            response.raise_for_status()
            data = response.json()
            
            for result in data.get("search", []):
                qid = result.get("id", "")
                label = result.get("label", "")
                desc = (result.get("description") or "").lower()
                
                # Skip non-Q items
                if not qid.startswith("Q"):
                    continue
                
                # Skip women's, youth, reserve teams
                if any(kw in desc for kw in skip_keywords) or any(kw in label.lower() for kw in skip_keywords):
                    continue
                
                # Skip B/C team suffixes
                label_parts = label.split()
                if len(label_parts) > 1 and label_parts[-1] in ("B", "C", "II", "III"):
                    continue
                
                # Must be football-related (for clubs)
                if team_type == "club":
                    # "association football club" or "football club" or just "football" in description
                    is_football = "football" in desc or "soccer" in desc or "fútbol" in desc
                    # Also accept multisports clubs (like FC Barcelona Q7156)
                    is_multisport = "multisport" in desc or "sports club" in desc
                    
                    if not (is_football or is_multisport):
                        continue
                else:
                    # National team - must have "national" and "football/soccer"
                    if "national" not in desc or ("football" not in desc and "soccer" not in desc):
                        continue
                
                # Found a good match!
                print(f"  📍 Found: {qid} ({label}) - {desc[:60]}...")
                return qid
                
        except Exception as e:
            print(f"  ⚠️ Search error for '{search_term}': {e}")
            continue
    
    print(f"  ❌ No Wikidata match found for {team_name}")
    return None


def normalize_wikidata_alias(alias: str) -> Optional[str]:
    """
    Normalize a single alias for LLM consumption.
    Returns None if the alias should be filtered out entirely.
    """
    import unicodedata
    
    # Normalize unicode (ç -> c, ü -> u, etc.)
    normalized = unicodedata.normalize('NFKD', alias)
    normalized = ''.join(c for c in normalized if not unicodedata.combining(c))
    
    # Skip non-Latin
    if not all(c.isascii() or c.isspace() for c in normalized):
        return None
    
    # Skip concatenated words like "FCBarcelona", "ASRoma", "FCInter"
    if len(normalized) > 4 and ' ' not in normalized and not normalized.isupper():
        for i in range(1, len(normalized)):
            # lowercase followed by uppercase: "ASRoma"
            if normalized[i-1].islower() and normalized[i].isupper():
                return None
        # Also catch FC/AS prefix patterns: "FCBarcelona" (FC + Capital + lowercase)
        if len(normalized) > 3 and normalized[:2].isupper() and normalized[2].isupper() and normalized[3:4].islower():
            return None
    
    # Skip "City SG" style aliases (word + 2-letter code) - these are useless
    parts = normalized.split()
    if len(parts) == 2 and len(parts[1]) <= 2:
        return None
    
    # Skip long descriptive phrases
    lower = normalized.lower()
    bad_phrases = ['national football team', 'national soccer team', 'football club', 
                   'soccer club', 'soccer team', 'football team', "men's national"]
    for phrase in bad_phrases:
        if phrase in lower:
            return None
    
    # Skip endings with FC, NT, Club
    if lower.endswith(' fc') or lower.endswith(' nt') or lower.endswith(' club'):
        return None
    
    # Skip redundant acronym suffixes like "PSGFC" (5+ chars ending in FC)
    # But keep legitimate 4-letter acronyms like NFFC, AVFC, MUFC
    if normalized.isupper() and len(normalized) >= 5:
        if normalized.endswith('FC') or normalized.endswith('NT'):
            return None
    
    # Remove periods from acronyms: "F.C.B." -> "FCB", but skip "A.S.Roma" style
    if '.' in normalized:
        no_periods = normalized.replace('.', '')
        if len(no_periods) <= 5 and no_periods.isupper():
            normalized = no_periods
        elif '.' in normalized:  # Still has periods, not a clean acronym
            return None
    
    # Skip very short or very long
    if len(normalized) < 3 or len(normalized) > 25:
        return None
    
    # Skip partial/truncated words (like "Ars", "Checkered")
    useless_fragments = {'ars', 'checkered', 'royal', 'spanish', 'belgian', 'italian'}
    if normalized.lower() in useless_fragments:
        return None
    
    # Remove periods from acronyms: "F.C.B." -> "FCB"
    if '.' in normalized and len(normalized.replace('.', '')) <= 5:
        normalized = normalized.replace('.', '')
    
    return normalized.strip()


# Country -> nationality adjective(s) AND alternate names for national teams
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
    'usa': ['American', 'America', 'USA', 'United', 'States'],
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


def preprocess_aliases_to_words(raw_aliases: List[str], team_name: str, team_type: str = "club") -> List[str]:
    """
    Heavy preprocessing to convert raw Wikidata aliases to clean single words.
    
    Steps:
    1. Filter out concatenated junk (FCBarcelona, ASRoma)
    2. Handle acronyms with periods (F.C.B. -> FCB, dedupe if FCB exists)
    3. Split multi-word aliases into individual words
    4. Remove skip words (FC, Club, The, La, de, of, etc.)
    5. Remove words that are part of the team name
    6. Dedupe everything
    """
    import unicodedata
    
    # Words to skip
    skip_words = {
        'fc', 'club', 'the', 'la', 'el', 'los', 'las', 'le', 'les', 'de', 'of', 
        'and', 'del', 'der', 'die', 'das', 'ac', 'as', 'sc', 'cf', 'cd', 'ss',
        'futbol', 'football', 'soccer', 'calcio', 'association', 'sporting',
        'national', 'team', "men's", 'mens', 'royal', 'real', 'nt', 'united',
        'one', 'red', 'west', 'sport', '1927', 'three'  # Generic/useless words
    }
    
    # Team name words to skip (lowercase)
    team_words = {w.lower() for w in team_name.split()}
    
    words = set()
    acronyms = set()  # Track acronyms separately for period-stripping dedupe
    
    for alias in raw_aliases:
        # Normalize unicode
        normalized = unicodedata.normalize('NFKD', alias)
        normalized = ''.join(c for c in normalized if not unicodedata.combining(c))
        
        # Skip non-ASCII
        if not all(c.isascii() or c.isspace() for c in normalized):
            continue
        
        # Skip concatenated junk like "FCBarcelona", "ASRoma"
        if len(normalized) > 4 and ' ' not in normalized and not normalized.isupper():
            is_concat = False
            for i in range(1, len(normalized)):
                if normalized[i-1].islower() and normalized[i].isupper():
                    is_concat = True
                    break
            if is_concat:
                continue
            # Also catch FC/AS prefix patterns
            if len(normalized) > 3 and normalized[:2].isupper() and normalized[2].isupper() and normalized[3:4].islower():
                continue
        
        # Handle acronyms with periods: "F.C.B." -> "FCB", "I. M." -> "IM"
        if '.' in normalized:
            no_periods = normalized.replace('.', '').replace(' ', '').strip()
            if 2 <= len(no_periods) <= 6 and no_periods.isupper():
                # Only add if not already seen
                if no_periods.lower() not in acronyms:
                    acronyms.add(no_periods.lower())
                    words.add(no_periods)
            continue  # Don't process further either way
        
        # Check if it's a clean acronym (all caps, 2-6 chars, NO SPACES)
        no_space = normalized.replace(' ', '')
        if no_space.isupper() and 2 <= len(no_space) <= 6 and ' ' not in normalized:
            # Skip corporate junk like SAD
            if no_space in {'SAD', 'PLC', 'LTD', 'INC', 'IM'}:
                continue
            if no_space.lower() not in acronyms:
                acronyms.add(no_space.lower())
                words.add(no_space)
            continue
        
        # Skip weird spaced "acronyms" like "I M"
        if len(normalized) <= 4 and ' ' in normalized:
            continue
        
        # Split multi-word aliases into individual words
        for word in normalized.split():
            # Strip punctuation from word
            word_clean = ''.join(c for c in word if c.isalnum())
            word_lower = word_clean.lower()
            
            # Skip if too short
            if len(word_clean) < 2:
                continue
            
            # Skip stop words
            if word_lower in skip_words:
                continue
            
            # Skip if it's just a team name word
            if word_lower in team_words:
                continue
            
            # Skip corporate/legal junk only
            if word_lower in {'sad', 'plc', 'ltd', 'inc', 'ev'}:
                continue
            
            # Add the word (preserve original case for proper nouns)
            words.add(word_clean)
    
    # For national teams, auto-generate nationality adjectives if not already present
    if team_type == "national":
        team_lower = team_name.lower()
        if team_lower in NATIONALITY_MAP:
            for adj in NATIONALITY_MAP[team_lower]:
                if adj.lower() not in {w.lower() for w in words}:
                    words.add(adj)
    
    # Filter out any 1-char words
    words = {w for w in words if len(w) > 1}
    
    # If we have non-acronym words, drop generic all-caps words (keep only meaningful acronyms)
    # But if acronyms are ALL we have, keep them
    non_caps = [w for w in words if not w.isupper()]
    if non_caps:
        # We have real words - only keep acronyms that look like team acronyms (3-5 chars)
        words = {w for w in words if not w.isupper() or (w.isupper() and 3 <= len(w) <= 5)}
    
    # Sort: acronyms first, then by length (shorter = better)
    result = sorted(words, key=lambda w: (not w.isupper(), len(w), w.lower()))
    
    return result


async def fetch_wikidata_aliases(qid: str) -> List[str]:
    """Fetch ENGLISH aliases only from Wikidata entity, pre-cleaned for LLM."""
    aliases = []
    try:
        response = requests.get(f"{WIKIDATA_ENTITY_URL}/{qid}.json", headers=HEADERS, timeout=10)
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
        
        return aliases  # Return raw - preprocessing happens in test_team
            
    except Exception as e:
        print(f"  ⚠️ Wikidata fetch error: {e}")
    
    return []


async def call_ollama(prompt: str) -> Optional[str]:
    """Call Ollama LLM via docker exec."""
    import subprocess
    
    # Escape the prompt for shell
    escaped_prompt = prompt.replace('"', '\\"').replace("'", "'\\''")
    
    cmd = [
        "docker", "exec", "found-footy-dev-ollama",
        "ollama", "run", "phi3:mini", escaped_prompt
    ]
    
    try:
        result = subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            timeout=30
        )
        return result.stdout.strip()
    except subprocess.TimeoutExpired:
        print("  ⚠️ Ollama timeout")
        return None
    except Exception as e:
        print(f"  ⚠️ Ollama error: {e}")
        return None


async def call_ollama_api(wikidata_aliases: List[str], team_name: str) -> Optional[List[str]]:
    """Call LLM API via llama.cpp server."""
    
    # Build the prompt - input is already single words, just rank them
    prompt = f"""Words: {json.dumps(wikidata_aliases)}

Select the best words for Twitter search. Return a JSON array. /no_think"""

    # Use llama-chat server (set LLAMA_URL env var or defaults to localhost)
    llama_url = os.getenv("LLAMA_URL", "http://localhost:8080")
    
    try:
        response = requests.post(
            f"{llama_url}/v1/chat/completions",
            json={
                "messages": [
                    {"role": "system", "content": SYSTEM_PROMPT},
                    {"role": "user", "content": prompt}
                ],
                "max_tokens": 100,
                "temperature": 0.1
            },
            timeout=30
        )
        response.raise_for_status()
        data = response.json()
        text = data["choices"][0]["message"]["content"]
        
        # Parse JSON array from response
        start = text.find("[")
        end = text.rfind("]") + 1
        if start >= 0 and end > start:
            aliases = json.loads(text[start:end])
            if isinstance(aliases, list) and len(aliases) >= 1:
                return [str(a).strip() for a in aliases[:3]]
    except Exception as e:
        print(f"  ⚠️ LLM API error: {e}")
    
    return None


def is_latin_script(text: str) -> bool:
    """Check if text is primarily Latin script (English-friendly)."""
    latin_chars = sum(1 for c in text if c.isascii() or c in 'áàâäãåéèêëíìîïóòôöõúùûüýÿñçæœ')
    return latin_chars / max(len(text), 1) > 0.8


def is_clean_acronym(text: str) -> bool:
    """Check if text is a clean acronym (no periods, 2-5 uppercase letters)."""
    return text.isupper() and 2 <= len(text) <= 5 and '.' not in text


def is_likely_english(text: str, team_name: str = "") -> bool:
    """
    Check if alias is likely English (not a foreign transliteration).
    
    Filters out things like "Chelsi", "Yuventus", "Arsenali", "Liverpul"
    which are foreign transliterations of English names.
    """
    # Acronyms are fine
    if text.isupper():
        return True
    
    # If it matches the team name closely, it's good
    if team_name and text.lower() in team_name.lower():
        return True
    if team_name and team_name.lower() in text.lower():
        return True
    
    # Common non-English endings that indicate transliteration
    bad_endings = ['ski', 'eli', 'ali', 'pul', 'tus', 'ona', 'sia', 'ana']
    text_lower = text.lower()
    
    # Check if it's a known English word/name - if the text ends weirdly, skip it
    # But allow compound words like "FCBarcelona" or "ManUnited"
    if len(text) > 5 and not text[0].isupper():
        for ending in bad_endings:
            if text_lower.endswith(ending) and not team_name.lower().endswith(ending):
                return False
    
    return True


def select_best_heuristic(aliases: List[str], team_name: str = "") -> List[str]:
    """
    Simple heuristic fallback when LLM fails.
    
    Prioritizes:
    1. Clean acronyms (3-5 uppercase letters, no periods)
    2. Single short words (nicknames like Spurs, Bayern, Atleti)
    3. Team name itself
    
    Avoids: "The X", "La X", "El X", long phrases, concatenated words
    """
    result = []
    seen = set()
    
    # Helper to check if alias is usable
    def is_good(alias):
        if not alias or not is_latin_script(alias):
            return False
        if '.' in alias:  # No periods
            return False
        lower = alias.lower()
        # Skip "The/La/El/Die" prefixes
        if lower.startswith(('the ', 'la ', 'el ', 'los ', 'die ', 'les ')):
            return False
        # Skip long phrases
        if 'football' in lower or 'national' in lower or 'club' in lower or 'team' in lower:
            return False
        # Skip concatenated words like "ArsenalFC"
        if len(alias) > 4 and ' ' not in alias:
            for i in range(1, len(alias) - 1):
                if alias[i-1].islower() and alias[i].isupper():
                    return False
        return True
    
    # 1. Find best acronym (3-5 uppercase letters)
    for alias in aliases:
        if alias.isupper() and 3 <= len(alias) <= 5 and is_good(alias):
            if alias.lower() not in seen:
                result.append(alias)
                seen.add(alias.lower())
                break
    
    # 2. Find short single-word names (nicknames)
    for alias in aliases:
        if len(result) >= 3:
            break
        words = alias.split()
        if len(words) == 1 and 3 <= len(alias) <= 12 and is_good(alias):
            if alias.lower() not in seen:
                result.append(alias)
                seen.add(alias.lower())
    
    # 3. Add team name if we don't have enough
    if len(result) < 3 and team_name and team_name.lower() not in seen:
        result.append(team_name)
        seen.add(team_name.lower())
    
    # 4. Fill with any remaining good short aliases
    for alias in aliases:
        if len(result) >= 3:
            break
        if len(alias) <= 15 and is_good(alias) and alias.lower() not in seen:
            result.append(alias)
            seen.add(alias.lower())
    
    return result[:3] if result else [team_name]


async def test_team(team_id: int, team_name: str, team_type: str, country: str = None) -> Tuple[str, List[str], List[str]]:
    """Test RAG pipeline for a single team."""
    print(f"\n{'='*60}")
    print(f"Team: {team_name} (ID: {team_id}, Type: {team_type}, Country: {country})")
    print(f"{'='*60}")
    
    # 1. Search for Wikidata QID
    qid = await search_wikidata_qid(team_name, team_type, country)
    
    if not qid:
        print(f"  ❌ No Wikidata QID found")
        return team_name, [], []
    
    print(f"  📚 Wikidata QID: {qid}")
    
    # 2. Fetch raw aliases
    raw_aliases = await fetch_wikidata_aliases(qid)
    print(f"  📚 Raw Wikidata aliases ({len(raw_aliases)}): {raw_aliases[:5]}...")
    
    if not raw_aliases:
        print(f"  ❌ No aliases found")
        return team_name, [], []
    
    # 3. Preprocess to single words (pass team_type for nationality generation)
    single_words = preprocess_aliases_to_words(raw_aliases, team_name, team_type)
    print(f"  🔧 Preprocessed to single words: {single_words}")
    
    if not single_words:
        print(f"  ❌ No usable words after preprocessing")
        return team_name, raw_aliases[:10], []
    
    # 4. Call LLM to rank/select the best ones
    llm_result = await call_ollama_api(single_words, team_name)
    
    if llm_result and len(llm_result) >= 1:
        # Grounding check - must be in our preprocessed list
        valid = []
        seen = set()
        words_lower = {w.lower() for w in single_words}
        
        for word in llm_result:
            word_clean = word.strip()
            word_lower = word_clean.lower()
            
            # Must be in our list and not a duplicate
            if word_lower in words_lower and word_lower not in seen:
                # Find original casing from our list
                original = next((w for w in single_words if w.lower() == word_lower), word_clean)
                valid.append(normalize_alias(original))
                seen.add(word_lower)
        
        if valid:
            print(f"  ✅ LLM selected: {llm_result}")
            # Ensure nationality adjectives are always included for national teams
            if team_type == "national":
                team_lower = team_name.lower()
                if team_lower in NATIONALITY_MAP:
                    valid_lower = {v.lower() for v in valid}
                    for adj in NATIONALITY_MAP[team_lower]:
                        if adj.lower() not in valid_lower:
                            valid.append(adj)
            # Add distinctive team name words
            valid = add_team_name_words(valid, team_name)
            print(f"  ✅ Final aliases: {valid}")
            return team_name, raw_aliases[:10], valid
        else:
            print(f"  ⚠️ LLM grounding failed: {llm_result}")
    
    # 5. Fallback - just use the preprocessed list as-is (already sorted by quality)
    fallback = [normalize_alias(w) for w in single_words[:5]]
    # Ensure nationality adjectives are always included for national teams
    if team_type == "national":
        team_lower = team_name.lower()
        if team_lower in NATIONALITY_MAP:
            fallback_lower = {f.lower() for f in fallback}
            for adj in NATIONALITY_MAP[team_lower]:
                if adj.lower() not in fallback_lower:
                    fallback.append(adj)
    # Add distinctive team name words
    fallback = add_team_name_words(fallback, team_name)
    print(f"  📋 Fallback (preprocessed): {fallback}")
    return team_name, raw_aliases[:10], fallback


def add_team_name_words(aliases: List[str], team_name: str) -> List[str]:
    """
    Add words from team name to the alias list.
    
    - Replace hyphens with spaces (Saint-Germain -> Saint Germain)
    - Skip short all-caps abbreviations (FC, AC, SC) UNLESS it's the only word
    - Keep everything else
    """
    aliases_lower = {a.lower() for a in aliases}
    result = list(aliases)
    
    # Replace hyphens with spaces, then split
    name_normalized = team_name.replace('-', ' ')
    words = name_normalized.split()
    
    for word in words:
        # Normalize the word (strip diacritics)
        word_clean = normalize_alias(word)
        word_lower = word_clean.lower()
        
        # Skip if too short
        if len(word_clean) < 2:
            continue
        
        # Skip short all-caps abbreviations (FC, AC, SC, etc.)
        # BUT keep if it's the only word in the team name (like "USA")
        if word_clean.isupper() and len(word_clean) <= 3 and len(words) > 1:
            continue
        
        # Skip if already in aliases
        if word_lower in aliases_lower:
            continue
        
        # Add the word
        result.append(word_clean)
        aliases_lower.add(word_lower)
    
    return result


async def main():
    parser = argparse.ArgumentParser(description="Test RAG pipeline for tracked teams")
    parser.add_argument("--team", type=str, help="Test specific team name")
    parser.add_argument("--clubs-only", action="store_true", help="Test clubs only (skip national teams)")
    args = parser.parse_args()
    
    print("=" * 70)
    print("RAG PIPELINE TEST - Wikidata + Ollama Alias Generation")
    print("=" * 70)
    
    # Check Ollama is reachable (external service via luv-dev network)
    ollama_url = "http://ollama-server:11434"
    try:
        response = requests.get(f"{ollama_url}/api/tags", timeout=5)
        models = response.json().get("models", [])
        model_names = [m.get("name") for m in models]
        print(f"✅ Ollama running, models: {model_names}")
    except Exception as e:
        print(f"❌ Ollama not reachable: {e}")
        print("   Run this inside the worker container:")
        print("   docker exec -it found-footy-dev-worker python /workspace/tests/test_rag_pipeline.py")
        sys.exit(1)
    
    # Filter teams
    teams_to_test = {}
    for team_id, data in TRACKED_TEAMS.items():
        if args.team and args.team.lower() not in data["name"].lower():
            continue
        if args.clubs_only and data["type"] == "national":
            continue
        teams_to_test[team_id] = data
    
    print(f"\n📊 Testing {len(teams_to_test)} teams...\n")
    
    results = []
    for team_id, data in teams_to_test.items():
        team_name, wikidata, aliases = await test_team(
            team_id, 
            data["name"], 
            data["type"],
            data.get("country")  # Pass country for better Wikidata search
        )
        results.append({
            "id": team_id,
            "name": team_name,
            "type": data["type"],
            "wikidata_sample": wikidata,
            "twitter_aliases": aliases
        })
        # Small delay to avoid rate limiting (SPARQL has rate limits)
        await asyncio.sleep(1.0)
    
    # Summary
    print("\n" + "=" * 70)
    print("SUMMARY")
    print("=" * 70)
    print(f"\n{'ID':<6} {'Team':<25} {'Type':<10} {'Twitter Aliases'}")
    print("-" * 70)
    for r in results:
        aliases_str = ", ".join(r["twitter_aliases"]) if r["twitter_aliases"] else "FAILED"
        print(f"{r['id']:<6} {r['name']:<25} {r['type']:<10} {aliases_str}")
    
    # Output as JSON for further processing
    print("\n" + "=" * 70)
    print("JSON OUTPUT (for caching)")
    print("=" * 70)
    print(json.dumps(results, indent=2))


if __name__ == "__main__":
    asyncio.run(main())
