#!/usr/bin/env python3
"""
Super simple Wikidata team lookup - like Google, not rocket science.
"""
import requests

# Copy team data directly to avoid import issues
TOP_UEFA = {
    541: {"name": "Real Madrid", "country": "Spain", "rank": 1},
    157: {"name": "Bayern München", "country": "Germany", "rank": 2},
    50: {"name": "Manchester City", "country": "England", "rank": 3},
    85: {"name": "Paris Saint Germain", "country": "France", "rank": 4},
    529: {"name": "Barcelona", "country": "Spain", "rank": 5},
    40: {"name": "Liverpool", "country": "England", "rank": 6},
    530: {"name": "Atletico Madrid", "country": "Spain", "rank": 7},
    42: {"name": "Arsenal", "country": "England", "rank": 8},
    33: {"name": "Manchester United", "country": "England", "rank": 9},
    165: {"name": "Borussia Dortmund", "country": "Germany", "rank": 10},
    49: {"name": "Chelsea", "country": "England", "rank": 11},
    496: {"name": "Juventus", "country": "Italy", "rank": 12},
    497: {"name": "AS Roma", "country": "Italy", "rank": 13},
    505: {"name": "Inter", "country": "Italy", "rank": 14},
    47: {"name": "Tottenham", "country": "England", "rank": 15},
}

# Simple search terms that work
SEARCH_OVERRIDES = {
    "Barcelona": "FC Barcelona",  # Avoid youth team
    "Bayern München": "FC Bayern Munich",
    "Paris Saint Germain": "Paris Saint-Germain FC", 
    "Atletico Madrid": "Atletico de Madrid",
    "Inter": "FC Internazionale Milano",  # Full name
    "Tottenham": "Tottenham Hotspur FC",
    "Borussia Dortmund": "Borussia Dortmund football",
    "AS Roma": "AS Roma football",
}

def search_wikidata(team_name: str) -> dict | None:
    """
    Simple Wikidata search - just like Google.
    Returns first football club result.
    """
    # Use override or default "{name} FC"
    search_term = SEARCH_OVERRIDES.get(team_name, f"{team_name} FC")
    
    url = "https://www.wikidata.org/w/api.php"
    params = {
        "action": "wbsearchentities",
        "search": search_term,
        "language": "en",
        "format": "json",
        "type": "item",
        "limit": 10
    }
    headers = {"User-Agent": "found-footy/1.0 (https://github.com/foundfooty)"}
    
    resp = requests.get(url, params=params, headers=headers)
    data = resp.json()
    
    # Find first result that's a football club
    for item in data.get("search", []):
        desc = (item.get("description") or "").lower()
        # Skip women's, youth, youth (under-*), basket, handball
        if any(x in desc for x in ["women", "youth", "under-", "basket", "handball", "category", "juvenil"]):
            continue
        # Accept if it mentions football
        if "football" in desc or "fútbol" in desc:
            return {
                "qid": item["id"],
                "label": item.get("label"),
                "description": item.get("description")
            }
    
    return None


def main():
    print("Testing simple Wikidata search for all TOP_UEFA teams\n")
    print("=" * 80)
    
    success = 0
    failed = []
    
    for team_id, team_info in TOP_UEFA.items():
        name = team_info["name"]
        result = search_wikidata(name)
        
        if result:
            print(f"✅ {name}")
            print(f"   {result['qid']}: {result['label']} - {result['description']}")
            success += 1
        else:
            print(f"❌ {name} - NOT FOUND")
            failed.append(name)
    
    print("\n" + "=" * 80)
    print(f"\nResults: {success}/{len(TOP_UEFA)} found")
    
    if failed:
        print(f"\nFailed teams: {', '.join(failed)}")


if __name__ == "__main__":
    main()
