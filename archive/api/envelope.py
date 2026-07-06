"""Typed SSE event envelopes.

This is the contract a future project copy-adapts from. See
`docs/api-contract.md` for the schema and rationale.

Envelope shape:
  {
    "type": "invalidate" | "heartbeat" | "health" | <app-specific>,
    "id":   "<monotonic counter, used by EventSource for Last-Event-ID resumption>",
    "ts":   <unix milliseconds>,
    "data": {<type-specific payload>}
  }

Constructor functions (`invalidate`, `heartbeat`, `health`) are the
public surface. Hand-building SSEEvent dicts elsewhere drifts the schema —
go through these.
"""

from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from typing import Any, Dict, List, Optional


def _now_ms() -> int:
    return int(datetime.now(timezone.utc).timestamp() * 1000)


@dataclass
class SSEEvent:
    """Base envelope. type/id/ts always present; `data` is type-specific."""

    type: str
    id: str
    ts: int
    data: Dict[str, Any] = field(default_factory=dict)

    def to_dict(self) -> Dict[str, Any]:
        return asdict(self)


# ─── Constructors ────────────────────────────────────────────────────────────


def invalidate(
    entity: str,
    ids: List[str],
    *,
    event_id: str,
    fields: Optional[List[str]] = None,
) -> SSEEvent:
    """Cache-invalidation signal.

    Tells the frontend: "<entity> with these <ids> changed; if you care
    about any of them, refetch via REST". Optional `fields` lets the
    frontend skip the refetch when only fields it doesn't display were
    touched (rarely worth using; safe to omit).
    """
    payload: Dict[str, Any] = {"entity": entity, "ids": [str(i) for i in ids]}
    if fields:
        payload["fields"] = fields
    return SSEEvent(type="invalidate", id=event_id, ts=_now_ms(), data=payload)


def heartbeat(*, event_id: str) -> SSEEvent:
    """Empty keep-alive — sent every SSE_HEARTBEAT_INTERVAL_S of idle.

    Lets the client detect dropped connections faster than waiting for
    a TCP timeout. EventSource auto-reconnects on close; the heartbeat
    just makes "close" happen sooner when something's wrong.
    """
    return SSEEvent(type="heartbeat", id=event_id, ts=_now_ms(), data={})


def health(
    status: str,
    *,
    event_id: str,
    details: Optional[Dict[str, Any]] = None,
) -> SSEEvent:
    """Backend health snapshot — sent on connection + when components flip."""
    payload: Dict[str, Any] = {"status": status}
    if details:
        payload["details"] = details
    return SSEEvent(type="health", id=event_id, ts=_now_ms(), data=payload)


def connected(*, event_id: str) -> SSEEvent:
    """Sent once per connection immediately after stream opens.

    Lets the client distinguish "connected for the first time, refresh
    your state" from "reconnected, you may already be up-to-date".
    """
    return SSEEvent(type="connected", id=event_id, ts=_now_ms(), data={})
