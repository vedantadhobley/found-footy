"""GET /api/v1/search?q=<query> — full-text search across fixtures.

Mirrors the existing Express `/search` endpoint: regex match on team
names + player names + assister names across all 3 collections, then
group results by date.
"""

import re
from collections import defaultdict
from typing import Any, Dict, List

from fastapi import APIRouter, Depends, Query
from pymongo.database import Database

from api.deps import get_db
from api.routers.fixtures import _FIXTURE_PROJECTION, _transform_fixtures

router = APIRouter()


# Search returns a SUBSET of fields — same projection as /fixtures.
_SEARCH_PROJECTION = _FIXTURE_PROJECTION


@router.get("/search")
def search(
    q: str = Query("", description="Search term — matched against team / player / assist names."),
    db: Database = Depends(get_db),
) -> dict:
    """Regex search across all 3 fixture collections.

    Empty / too-short queries return empty results without hitting Mongo.
    Returns results grouped by YYYY-MM-DD date, most-recent-day first,
    plus per-fixture `_search` metadata pointing at which events matched.
    """
    query = (q or "").strip()
    if len(query) < 2:
        return {"results": [], "query": query, "total_fixtures": 0}

    # Escape regex special chars so a query like "F.C." doesn't blow up
    escaped = re.escape(query)
    regex = {"$regex": escaped, "$options": "i"}

    full_filter = {
        "$or": [
            {"teams.home.name": regex},
            {"teams.away.name": regex},
            {"events.player.name": regex},
            {"events.assist.name": regex},
        ]
    }
    # Staging has no events yet — match on team names only
    staging_filter = {
        "$or": [
            {"teams.home.name": regex},
            {"teams.away.name": regex},
        ]
    }

    completed = list(
        db["fixtures_completed"]
        .find(full_filter, _SEARCH_PROJECTION)
        .sort("fixture.date", -1)
        .limit(100)
    )
    active = list(
        db["fixtures_active"]
        .find(full_filter, _SEARCH_PROJECTION)
        .sort("fixture.date", -1)
        .limit(50)
    )
    staging = list(
        db["fixtures_staging"]
        .find(staging_filter, _SEARCH_PROJECTION)
        .sort("fixture.date", 1)
        .limit(50)
    )

    # Merge + dedupe by _id (staging > active > completed if duplicated,
    # which is unlikely but possible during transitions)
    seen: set[str] = set()
    fixtures: List[Dict[str, Any]] = []
    for f in [*staging, *active, *completed]:
        fid = str(f.get("_id"))
        if fid not in seen:
            seen.add(fid)
            fixtures.append(f)

    fixtures.sort(key=lambda f: (f.get("fixture") or {}).get("date") or "", reverse=True)

    # Annotate each fixture with which events matched the query
    re_pattern = re.compile(escaped, re.IGNORECASE)
    transformed = _transform_fixtures(fixtures)
    annotated: List[Dict[str, Any]] = []
    for fixture in transformed:
        teams = fixture.get("teams") or {}
        team_match = bool(
            re_pattern.search((teams.get("home") or {}).get("name") or "")
            or re_pattern.search((teams.get("away") or {}).get("name") or "")
        )
        matched_event_ids: List[str] = []
        for event in fixture.get("events") or []:
            player_name = (event.get("player") or {}).get("name") or ""
            assist_name = (event.get("assist") or {}).get("name") or ""
            if re_pattern.search(player_name) or re_pattern.search(assist_name):
                if event.get("_event_id"):
                    matched_event_ids.append(event["_event_id"])
        fixture["_search"] = {
            "team_match": team_match,
            "matched_event_ids": matched_event_ids,
            "match_count": (
                len(fixture.get("events") or []) if team_match else len(matched_event_ids)
            ),
        }
        annotated.append(fixture)

    # Group by date string (YYYY-MM-DD)
    grouped: Dict[str, List[Dict[str, Any]]] = defaultdict(list)
    for fixture in annotated:
        date_str = ((fixture.get("fixture") or {}).get("date") or "unknown")[:10]
        grouped[date_str].append(fixture)

    results = [
        {"date": date, "fixtures": grouped[date]}
        for date in sorted(grouped.keys(), reverse=True)
    ]
    return {"results": results, "query": query, "total_fixtures": len(annotated)}
