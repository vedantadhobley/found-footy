"""FastAPI dependencies.

Centralized so each router doesn't reach into pymongo / app state
directly. Easier to mock in tests and easier to swap the underlying
implementation later.
"""

from typing import Optional

from fastapi import Header, Request
from pymongo import MongoClient
from pymongo.database import Database

from api.errors import UnauthorizedError
from api.settings import settings
from api.sse import SSEManager


# Process-wide MongoDB client. PyMongo's MongoClient is thread-safe and
# manages its own connection pool; one instance per process is correct.
_mongo_client: Optional[MongoClient] = None


def get_mongo_client() -> MongoClient:
    global _mongo_client
    if _mongo_client is None:
        _mongo_client = MongoClient(settings.MONGODB_URI)
    return _mongo_client


def get_db() -> Database:
    """Yields the `found_footy` database from the shared client."""
    return get_mongo_client()[settings.MONGODB_DB_NAME]


def get_sse_manager(request: Request) -> SSEManager:
    """The SSE manager lives on app.state, set up by the lifespan handler."""
    return request.app.state.sse_manager


def require_internal_token(x_internal_token: Optional[str] = Header(None)) -> bool:
    """Guard for /api/v1/internal/* routes.

    In dev (`INTERNAL_TOKEN=""`) this is a no-op — we rely on docker
    network isolation. In prod, set `INTERNAL_TOKEN` to a strong shared
    secret and pass it via `X-Internal-Token` header from the worker.
    """
    if not settings.INTERNAL_TOKEN:
        return True
    if x_internal_token != settings.INTERNAL_TOKEN:
        raise UnauthorizedError("invalid or missing X-Internal-Token")
    return True
