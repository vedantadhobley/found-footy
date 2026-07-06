"""GET /api/v1/health — service liveness."""

from datetime import datetime, timezone

from fastapi import APIRouter, Depends
from pymongo.database import Database

from api.deps import get_db, get_sse_manager
from api.sse import SSEManager

router = APIRouter()


@router.get("/health")
def health(db: Database = Depends(get_db), sse: SSEManager = Depends(get_sse_manager)) -> dict:
    """Liveness probe.

    Returns:
      status: "ok" | "degraded"
      mongo:  "up" | "down"
      sse_connections: <int>
      timestamp: <ISO 8601>
    """
    mongo_status = "up"
    try:
        # Cheap ping — avoids loading any collections
        db.command("ping")
    except Exception:
        mongo_status = "down"

    overall = "ok" if mongo_status == "up" else "degraded"

    return {
        "status": overall,
        "mongo": mongo_status,
        "sse_connections": sse.connection_count,
        "timestamp": datetime.now(timezone.utc).isoformat(),
    }
