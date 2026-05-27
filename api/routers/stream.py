"""GET /api/v1/stream — SSE endpoint with typed envelope protocol.

See docs/api-contract.md for the event shapes the frontend expects.

Connection lifecycle:
  1. Client opens EventSource on /api/v1/stream
  2. Server immediately sends `connected` envelope
  3. Server interleaves `invalidate` events (broadcast from /internal/notify
     when the worker mutates data) with `heartbeat` events every
     SSE_HEARTBEAT_INTERVAL_S of idle.
  4. EventSource auto-reconnects on close — server uses monotonic
     `id:` field so clients can pass Last-Event-ID for replay (which
     this implementation does NOT honor today; messages dropped during
     reconnect are recovered via REST refetch instead).
"""

from fastapi import APIRouter, Depends
from sse_starlette.sse import EventSourceResponse

from api.deps import get_sse_manager
from api.envelope import connected
from api.sse import SSEManager, next_event_id

router = APIRouter()


@router.get("/stream")
async def stream(sse: SSEManager = Depends(get_sse_manager)):
    """Open an SSE stream. Returns immediately; events flow until close.

    Headers:
      Content-Type: text/event-stream
      Cache-Control: no-cache
      Connection: keep-alive
      X-Accel-Buffering: no   ← critical: tells nginx/Caddy not to buffer
    """
    conn_id, queue = sse.add_connection()
    # Seed the connection with a `connected` event so the client can
    # distinguish first-connect from reconnect (and refresh its state).
    await queue.put(connected(event_id=next_event_id()))

    return EventSourceResponse(
        sse.stream(conn_id, queue),
        # sse-starlette's `ping` is TCP-keepalive style (a `:ping` comment
        # line, not a typed envelope). It's separate from our heartbeat
        # envelope — both serve different layers (comments keep the TCP
        # connection alive past intermediate-proxy idle timeouts; envelopes
        # are application-level signals the client parses).
        #
        # NOTE: passing `ping=0` here triggers a tight-loop ping spam
        # (treated as "ping every 0 seconds") — confirmed by smoke test.
        # 15 seconds is the well-known default.
        ping=15,
        # Tell upstream proxies (Caddy / nginx) not to buffer.
        headers={
            "Cache-Control": "no-cache",
            "X-Accel-Buffering": "no",
        },
    )
