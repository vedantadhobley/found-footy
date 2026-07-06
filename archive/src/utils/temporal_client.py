"""
Process-level singleton for the Temporal client used INSIDE activities.

Activities sometimes need to call back into the Temporal client (e.g.
queue_videos_for_upload uses signal-with-start to drive UploadWorkflow).
Each Client.connect() opens a fresh gRPC connection to the Temporal server.
Doing that on every activity invocation is wasteful — measured ~100+ extra
connects per CL night.

This singleton initializes the client lazily on first use and reuses the same
connection for the lifetime of the worker process. PyMongo-style pattern,
adapted for asyncio.

Not for use inside @workflow.run — workflows must use workflow.execute_activity
and friends, which already have access to the embedded client.

Not for use inside the worker bootstrap (src/worker.py) or the scaler service
(src/scaler/scaler_service.py) — those run in different processes and have
their own one-shot Client.connect at startup.
"""
import asyncio
import os
from typing import Optional

from temporalio.client import Client

_client: Optional[Client] = None
_lock = asyncio.Lock()


async def get_client() -> Client:
    """Return the process-wide Temporal client, opening it on first call.

    Safe to call concurrently from multiple activities; the lock guards
    first-time initialization.
    """
    global _client
    if _client is not None:
        return _client
    async with _lock:
        if _client is None:
            temporal_host = os.getenv("TEMPORAL_HOST", "localhost:7233")
            _client = await Client.connect(temporal_host)
    return _client
