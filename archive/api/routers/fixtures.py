"""GET /api/v1/fixtures, /fixtures/{id}, /dates.

Mirrors the read-shape the existing Express router serves at
/api/found-footy/{fixtures,dates}. Video URLs are rewritten from the
MongoDB-stored `/video/<bucket>/<key>` form to `/api/v1/videos/<bucket>/<key>`
at the API boundary so clients don't need to know the storage layout.

URL stability commitment (see docs/api-contract.md): the URL shape
returned here is part of the public contract. Don't reshape without
versioning.
"""

from datetime import datetime, timedelta, timezone
from typing import Any, Dict, List, Optional

from fastapi import APIRouter, Depends, Query
from pymongo.database import Database

from api.deps import get_db
from api.errors import BadRequestError, NotFoundError

router = APIRouter()


# Projection used across the fixture-listing endpoints. Keep it lean —
# the goal is full-list responses small enough to ship over a mobile
# connection during a CL Tuesday night.
_FIXTURE_PROJECTION = {
    "_id": 1,
    "fixture.id": 1,
    "fixture.date": 1,
    "fixture.status": 1,
    "league.id": 1,
    "league.name": 1,
    "league.country": 1,
    "league.round": 1,
    "teams.home.name": 1,
    "teams.home.winner": 1,
    "teams.away.name": 1,
    "teams.away.winner": 1,
    "goals.home": 1,
    "goals.away": 1,
    "score.penalty": 1,
    "_last_activity": 1,
    # Event fields
    "events._event_id": 1,
    "events.type": 1,
    "events.detail": 1,
    "events.time": 1,
    "events.team": 1,
    "events.player": 1,
    "events.assist": 1,
    "events._scoring_team": 1,
    "events._score_after": 1,
    "events._monitor_complete": 1,
    "events._download_complete": 1,
    "events._first_seen": 1,
    "events._s3_urls": 1,
    "events._s3_videos": 1,
    "events._telemetry": 1,
}


def _rewrite_video_url(url: Optional[str]) -> Optional[str]:
    """Map stored `/video/...` URLs to the public `/api/v1/videos/...` form."""
    if not url:
        return url
    if url.startswith("/video/"):
        return "/api/v1/videos/" + url[len("/video/"):]
    return url


def _transform_fixture(fixture: Dict[str, Any]) -> Dict[str, Any]:
    """Rewrite video URLs on a fixture document in-place-on-copy."""
    events = fixture.get("events") or []
    if not events:
        return fixture
    new_events = []
    for ev in events:
        ev_copy = dict(ev)
        if "_s3_urls" in ev_copy and ev_copy["_s3_urls"]:
            ev_copy["_s3_urls"] = [_rewrite_video_url(u) for u in ev_copy["_s3_urls"]]
        if "_s3_videos" in ev_copy and ev_copy["_s3_videos"]:
            new_videos = []
            for v in ev_copy["_s3_videos"]:
                v_copy = dict(v)
                if "url" in v_copy:
                    v_copy["url"] = _rewrite_video_url(v_copy["url"])
                new_videos.append(v_copy)
            ev_copy["_s3_videos"] = new_videos
        new_events.append(ev_copy)
    out = dict(fixture)
    out["events"] = new_events
    return out


def _transform_fixtures(fixtures: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
    return [_transform_fixture(f) for f in fixtures]


def _day_bounds(date_str: str) -> tuple[str, str]:
    """Return ISO-string [start, end) bounds for a single UTC day.

    The fixture.date field is stored as ISO strings (e.g.
    "2026-05-27T19:00:00+00:00"), so string comparison works.
    """
    try:
        day = datetime.strptime(date_str, "%Y-%m-%d").replace(tzinfo=timezone.utc)
    except ValueError as e:
        raise BadRequestError(f"date must be YYYY-MM-DD, got: {date_str!r}") from e
    end = day + timedelta(days=1)
    return day.isoformat().replace("+00:00", "Z"), end.isoformat().replace("+00:00", "Z")


# ─── GET /api/v1/dates ───────────────────────────────────────────────────────


@router.get("/dates")
def dates(db: Database = Depends(get_db)) -> dict:
    """List of YYYY-MM-DD dates with fixtures (most recent first, capped at 90)."""
    def _day_substr(coll: str, limit: Optional[int] = None) -> List[str]:
        pipeline = [
            {"$project": {"date": {"$substr": ["$fixture.date", 0, 10]}}},
            {"$group": {"_id": "$date"}},
            {"$sort": {"_id": -1}},
        ]
        if limit:
            pipeline.append({"$limit": limit})
        return [d["_id"] for d in db[coll].aggregate(pipeline) if d.get("_id")]

    all_dates = set()
    all_dates.update(_day_substr("fixtures_completed", limit=90))
    all_dates.update(_day_substr("fixtures_active"))
    all_dates.update(_day_substr("fixtures_staging"))
    return {"dates": sorted(all_dates, reverse=True)}


# ─── GET /api/v1/fixtures ────────────────────────────────────────────────────


@router.get("/fixtures")
def fixtures(
    date: Optional[str] = Query(
        None,
        description="YYYY-MM-DD — if provided, only fixtures for that day. Omit at your own risk on mobile.",
    ),
    db: Database = Depends(get_db),
) -> dict:
    """Return staging + active + completed fixtures.

    With `?date=`, returns only fixtures for that UTC day. Without it,
    returns every fixture in every collection — kept for back-compat
    with the existing Express endpoint but discouraged on mobile.
    """
    if date:
        start, end = _day_bounds(date)
        date_filter = {"fixture.date": {"$gte": start, "$lt": end}}
    else:
        date_filter = {}

    staging = list(
        db["fixtures_staging"]
        .find(date_filter, _FIXTURE_PROJECTION)
        .sort("fixture.date", 1)
    )
    active = list(
        db["fixtures_active"]
        .find(date_filter, _FIXTURE_PROJECTION)
        .sort([("_last_activity", -1), ("fixture.date", -1)])
    )
    completed = list(
        db["fixtures_completed"]
        .find(date_filter, _FIXTURE_PROJECTION)
        .sort("fixture.date", -1)
    )

    payload: Dict[str, Any] = {
        "staging": staging,  # no videos in staging — no URL rewrite needed
        "active": _transform_fixtures(active),
        "completed": _transform_fixtures(completed),
    }
    if date:
        payload["date"] = date
    return payload


# ─── GET /api/v1/fixtures/{fixture_id} ───────────────────────────────────────


@router.get("/fixtures/{fixture_id}")
def fixture_by_id(fixture_id: int, db: Database = Depends(get_db)) -> dict:
    """Return one fixture by its API-Football id, looking across all 3 collections."""
    for coll in ("fixtures_active", "fixtures_completed", "fixtures_staging"):
        doc = db[coll].find_one({"_id": fixture_id}, _FIXTURE_PROJECTION)
        if doc:
            return {"collection": coll, "fixture": _transform_fixture(doc)}
    raise NotFoundError("fixture", str(fixture_id))
