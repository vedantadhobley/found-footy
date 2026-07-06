"""GET /api/v1/events/{event_id} — find which date/fixture an event belongs to.

Mirrors the existing Express `/event/:eventId` endpoint. Used by the
"shared event link" flow: a URL like vedanta.systems/.../event/<event_id>
needs to know which fixture-date to render, which means looking up
across all 3 fixture collections by event id.

Returns a `status` field — Phase 6 add — that derives the user-visible
status from raw flags, so the frontend stops doing this math itself
(fixes the "extracting too long" UX issue: status flips to "watching"
as soon as the first S3 video lands, regardless of whether
_download_complete is true yet).
"""

from typing import Any, Dict, Optional

from fastapi import APIRouter, Depends
from pymongo.database import Database

from api.deps import get_db
from api.errors import NotFoundError

router = APIRouter()


def _derive_event_status(event: Dict[str, Any]) -> str:
    """Compute the user-visible status string from raw flags.

    Status ladder (most-resolved → least-resolved):
      "watching"   — at least one S3 video exists; user can play something
      "complete"   — all 10 download attempts ran, no videos found
                     (low-coverage match; nothing's coming)
      "extracting" — monitor said this is a real event, scraping in progress,
                     no videos in S3 yet
      "validating" — debouncing the API report (waiting for the 3-poll
                     stability + a known player name)

    The frontend reads this directly instead of recomputing from
    _monitor_complete / _download_complete / _s3_videos. Decouples the
    UI semantic from the workflow lifecycle (fixes the "extracting"
    status persisting until all 10 DLWFs finish even when a usable clip
    landed at attempt 2).
    """
    s3_videos = event.get("_s3_videos") or []
    if s3_videos:
        return "watching"

    monitor_complete = bool(event.get("_monitor_complete"))
    download_complete = bool(event.get("_download_complete"))

    if download_complete:
        # Pipeline ran to completion but produced 0 videos — low-profile
        # match with no Twitter coverage. Distinguish from "still working".
        return "complete"
    if monitor_complete:
        return "extracting"
    return "validating"


@router.get("/events/{event_id}")
def event_by_id(event_id: str, db: Database = Depends(get_db)) -> dict:
    """Find which fixture (across all 3 collections) holds this event id.

    Returns: {event_id, found, date, fixture_id, status} (status only
    when found). For shared-link routing the frontend only needs date
    + fixture_id, but `status` is included as a freebie.
    """
    for collection_name in ("fixtures_active", "fixtures_completed", "fixtures_staging"):
        doc = db[collection_name].find_one(
            {"events._event_id": event_id},
            {
                "_id": 1,
                "fixture.date": 1,
                "events.$": 1,
            },
        )
        if doc and doc.get("fixture", {}).get("date"):
            date = doc["fixture"]["date"][:10]  # YYYY-MM-DD
            event_doc = (doc.get("events") or [{}])[0]
            return {
                "event_id": event_id,
                "found": True,
                "date": date,
                "fixture_id": doc["_id"],
                "collection": collection_name,
                "status": _derive_event_status(event_doc),
            }

    return {"event_id": event_id, "found": False}
