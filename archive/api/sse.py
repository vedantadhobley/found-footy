"""SSE connection manager — per-connection queues + broadcast + heartbeats.

Each client connection gets its own bounded `asyncio.Queue`. A broadcast
puts the event into every queue concurrently; if a queue is full (a slow
client falling behind), that connection is dropped — EventSource will
auto-reconnect from the client side, and the catch-up happens via REST.

Heartbeats are interleaved at SSE_HEARTBEAT_INTERVAL_S of idle so a
silent broken connection is detected within seconds rather than minutes.

This pattern is the one project #2 will copy. Kept dependency-light
(stdlib asyncio + dataclasses; no celery/redis/whatever) so it drops
into any small project.
"""

import asyncio
import itertools
import json
from typing import AsyncIterator, Dict, Tuple

from api.envelope import SSEEvent, heartbeat


# Process-wide monotonic counter for SSE event ids. Used for
# Last-Event-ID resumption (`id:` field on each emitted event).
_event_id_counter = itertools.count(1)


def next_event_id() -> str:
    """Get the next process-wide event id (monotonic, opaque to clients)."""
    return str(next(_event_id_counter))


class SSEManager:
    """Connection registry + broadcast.

    Lives on app.state. Worker process notifications come in via the
    `/api/v1/internal/notify` endpoint and call `broadcast()`. Client
    connections call `add_connection()` to register and `stream()` to
    drain their queue.
    """

    def __init__(
        self,
        heartbeat_interval_s: int = 30,
        max_queue_per_connection: int = 100,
    ):
        self._connections: Dict[int, asyncio.Queue] = {}
        self._conn_id_seq = itertools.count(1)
        self._heartbeat_interval_s = heartbeat_interval_s
        self._max_queue_per_connection = max_queue_per_connection

    @property
    def connection_count(self) -> int:
        return len(self._connections)

    def add_connection(self) -> Tuple[int, asyncio.Queue]:
        """Register a new client connection. Returns (conn_id, queue)."""
        conn_id = next(self._conn_id_seq)
        queue: asyncio.Queue = asyncio.Queue(maxsize=self._max_queue_per_connection)
        self._connections[conn_id] = queue
        return conn_id, queue

    def remove_connection(self, conn_id: int) -> None:
        """Drop a connection — called on disconnect or queue-overflow."""
        self._connections.pop(conn_id, None)

    async def broadcast(self, event: SSEEvent) -> int:
        """Push `event` into every connection's queue.

        Returns the number of connections that received it. Connections
        whose queue was full at the time get dropped — the client side's
        EventSource will reconnect and refetch via REST (the design
        explicitly tolerates message loss for slow clients).
        """
        delivered = 0
        for conn_id, q in list(self._connections.items()):
            try:
                q.put_nowait(event)
                delivered += 1
            except asyncio.QueueFull:
                self.remove_connection(conn_id)
        return delivered

    async def stream(
        self, conn_id: int, queue: asyncio.Queue
    ) -> AsyncIterator[Dict]:
        """Generator the SSE response wraps.

        Yields dicts in the shape `sse-starlette.EventSourceResponse`
        expects (event/data/id keys). The `data` field is pre-serialized
        as a JSON string — sse-starlette's default str() on a dict
        produces Python repr (single quotes) which isn't valid JSON
        and would break `JSON.parse()` on the client.

        Interleaves heartbeats every `heartbeat_interval_s` of queue
        idleness.
        """
        try:
            while True:
                try:
                    event = await asyncio.wait_for(
                        queue.get(), timeout=self._heartbeat_interval_s
                    )
                except asyncio.TimeoutError:
                    hb = heartbeat(event_id=next_event_id())
                    yield {
                        "id": hb.id,
                        "event": hb.type,
                        "data": json.dumps(hb.to_dict()),
                    }
                    continue

                yield {
                    "id": event.id,
                    "event": event.type,
                    "data": json.dumps(event.to_dict()),
                }
        finally:
            self.remove_connection(conn_id)
