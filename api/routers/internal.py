"""POST /api/v1/internal/notify — worker pushes invalidation events to the API.

The worker (specifically `monitor.notify_frontend_refresh` activity)
calls this endpoint after notable Mongo mutations, and the API
broadcasts a typed `invalidate` SSE event to all connected clients.

Auth: requires X-Internal-Token header matching INTERNAL_TOKEN env var.
In dev the token is empty and any caller can hit this — relies on
docker network isolation (only worker/twitter containers can reach
found-footy-{env}-api:8080).
"""

from typing import List, Optional

from fastapi import APIRouter, Depends
from pydantic import BaseModel, Field

from api.deps import get_sse_manager, require_internal_token
from api.envelope import invalidate
from api.sse import SSEManager, next_event_id

router = APIRouter()


class NotifyRequest(BaseModel):
    """Worker → API: 'these entities changed, tell clients to refetch'."""

    entity: str = Field(..., description="Resource name: fixtures | events | videos | health")
    ids: List[str] = Field(default_factory=list, description="Affected ids (stringified).")
    fields: Optional[List[str]] = Field(
        None,
        description=(
            "Optional list of changed field paths. Frontend may skip the "
            "refetch if it doesn't render any of these fields."
        ),
    )


class NotifyResponse(BaseModel):
    delivered_to: int
    event_id: str


@router.post(
    "/internal/notify",
    response_model=NotifyResponse,
    dependencies=[Depends(require_internal_token)],
)
async def notify(req: NotifyRequest, sse: SSEManager = Depends(get_sse_manager)) -> NotifyResponse:
    """Broadcast an `invalidate` envelope to every connected SSE client.

    Returns the delivery count so the worker can log impact.
    """
    eid = next_event_id()
    event = invalidate(req.entity, req.ids, event_id=eid, fields=req.fields)
    delivered = await sse.broadcast(event)
    return NotifyResponse(delivered_to=delivered, event_id=eid)
