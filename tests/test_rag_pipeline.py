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

SYSTEM_PROMPT = """You are a football alias selector. Given a list of official aliases from Wikidata, derive the 3 best for Twitter search.

Rules:
- Return ONLY a JSON array of exactly 3 strings
- Choose short names (1-2 words) that fans use on Twitter
- Prefer: abbreviations (ATM, LFC), short names (Atleti, Barca), common nicknames
- You MAY simplify aliases: "El Atleti" → "Atleti", "FC Barcelona" → "Barcelona"
- All output must be DERIVED from the Wikidata list (substrings/simplifications OK)
- DO NOT invent aliases that aren't grounded in the Wikidata data
- No explanations, just the JSON array"""


def normalize_alias(alias: str) -> str:
    """Remove diacritics: Atlético → Atletico"""
    normalized = unicodedata.normalize('NFD', alias)
    return ''.join(c for c in normalized if unicodedata.category(c) != 'Mn')


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


async def fetch_wikidata_aliases(qid: str) -> List[str]:
    """Fetch ENGLISH aliases only from Wikidata entity."""
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
        
        # Deduplicate
        seen = set()
        unique = []
        for alias in aliases:
            if alias.lower() not in seen:
                seen.add(alias.lower())
                unique.append(alias)
        
        return unique
            
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
    """Call Ollama API via internal Docker network."""
    
    # Build the prompt
    prompt = f"""Team: {team_name}
Wikidata aliases: {json.dumps(wikidata_aliases[:20])}

Select the 3 best aliases for Twitter search. You can simplify (drop El/The/FC). Return ONLY a JSON array of 3 strings."""

    payload = {
        "model": "phi3:mini",
        "prompt": prompt,
        "system": SYSTEM_PROMPT,
        "stream": False,
        "options": {"temperature": 0.2, "num_predict": 50}
    }
    
    # Use internal Docker network URL
    ollama_url = "http://found-footy-dev-ollama:11434"
    
    try:
        response = requests.post(
            f"{ollama_url}/api/generate",
            json=payload,
            timeout=30
        )
        response.raise_for_status()
        data = response.json()
        text = data.get("response", "")
        
        # Parse JSON array from response
        start = text.find("[")
        end = text.rfind("]") + 1
        if start >= 0 and end > start:
            aliases = json.loads(text[start:end])
            if isinstance(aliases, list) and len(aliases) >= 3:
                return [str(a).strip() for a in aliases[:3]]
    except Exception as e:
        print(f"  ⚠️ Ollama API error: {e}")
    
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
    Heuristic selection when LLM fails.
    
    Rules:
    - Prefer English/Latin script aliases
    - Maximum 1 acronym (no periods like J.F.C.)
    - Prefer short nicknames and common names
    - Good variety (not all acronym variations)
    """
    # Filter to Latin script only, prefer English
    latin_aliases = [a for a in aliases if is_latin_script(a) and is_likely_english(a, team_name)]
    
    # Fallback if too aggressive filtering
    if len(latin_aliases) < 3:
        latin_aliases = [a for a in aliases if is_latin_script(a)]
    
    if not latin_aliases:
        return aliases[:3] if aliases else [team_name]
    
    # Categorize aliases
    clean_acronyms = []  # Like "LFC", "PSG", "ATM"
    short_names = []     # Like "Barca", "Bayern", "Spurs"  
    medium_names = []    # Like "Liverpool", "Real Madrid"
    other = []
    
    for alias in latin_aliases:
        # Skip aliases with periods (like "J.F.C.", "A.S.R")
        if '.' in alias:
            continue
        
        # Skip hashtags
        if alias.startswith('#'):
            continue
            
        words = alias.split()
        
        if is_clean_acronym(alias):
            clean_acronyms.append(alias)
        elif len(words) == 1 and len(alias) <= 10:
            # Single short word - likely a nickname
            short_names.append(alias)
        elif len(words) <= 2 and len(alias) <= 20:
            medium_names.append(alias)
        elif len(alias) <= 30:
            other.append(alias)
    
    # Build result: max 1 acronym, prefer variety
    result = []
    
    # Add best acronym (if any) - prefer shorter ones
    if clean_acronyms:
        # Sort by length (shorter first), then alphabetically
        clean_acronyms.sort(key=lambda x: (len(x), x))
        result.append(clean_acronyms[0])
    
    # Add short nicknames (most valuable for Twitter)
    seen_lower = {a.lower() for a in result}
    for name in short_names:
        if name.lower() not in seen_lower:
            result.append(name)
            seen_lower.add(name.lower())
            if len(result) >= 3:
                break
    
    # Fill with medium names if needed
    for name in medium_names:
        if len(result) >= 3:
            break
        if name.lower() not in seen_lower:
            result.append(name)
            seen_lower.add(name.lower())
    
    # Fill with other if still needed
    for name in other:
        if len(result) >= 3:
            break
        if name.lower() not in seen_lower:
            result.append(name)
            seen_lower.add(name.lower())
    
    # If we still don't have 3, add team name
    if len(result) < 3 and team_name and team_name.lower() not in seen_lower:
        result.append(team_name)
    
    return result[:3]


async def test_team(team_id: int, team_name: str, team_type: str, country: str = None) -> Tuple[str, List[str], List[str]]:
    """Test RAG pipeline for a single team."""
    print(f"\n{'='*60}")
    print(f"Team: {team_name} (ID: {team_id}, Type: {team_type}, Country: {country})")
    print(f"{'='*60}")
    
    # Search for Wikidata QID using SPARQL
    qid = await search_wikidata_qid(team_name, team_type, country)
    
    if not qid:
        print(f"  ❌ No Wikidata QID found")
        return team_name, [], [team_name]
    
    print(f"  📚 Wikidata QID: {qid}")
    
    # 2. Fetch aliases
    wikidata_aliases = await fetch_wikidata_aliases(qid)
    print(f"  📚 Wikidata aliases ({len(wikidata_aliases)}): {wikidata_aliases[:8]}...")
    
    if not wikidata_aliases:
        print(f"  ❌ No aliases found")
        return team_name, [], [team_name]
    
    # 3. Call LLM
    llm_result = await call_ollama_api(wikidata_aliases, team_name)
    
    if llm_result:
        # Validate against Wikidata
        wikidata_text = " ".join(wikidata_aliases).lower()
        valid = [a for a in llm_result if a.lower() in wikidata_text]
        
        if len(valid) >= 3:
            final = [normalize_alias(a) for a in valid[:3]]
            print(f"  ✅ LLM result: {llm_result}")
            print(f"  ✅ Normalized: {final}")
            return team_name, wikidata_aliases[:10], final
        else:
            print(f"  ⚠️ LLM returned ungrounded: {llm_result}")
    
    # 4. Fallback to heuristic
    selected = select_best_heuristic(wikidata_aliases, team_name)
    final = [normalize_alias(a) for a in selected]
    print(f"  📋 Heuristic fallback: {final}")
    return team_name, wikidata_aliases[:10], final


async def main():
    parser = argparse.ArgumentParser(description="Test RAG pipeline for tracked teams")
    parser.add_argument("--team", type=str, help="Test specific team name")
    parser.add_argument("--clubs-only", action="store_true", help="Test clubs only (skip national teams)")
    args = parser.parse_args()
    
    print("=" * 70)
    print("RAG PIPELINE TEST - Wikidata + Ollama Alias Generation")
    print("=" * 70)
    
    # Check Ollama is reachable
    ollama_url = "http://found-footy-dev-ollama:11434"
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
