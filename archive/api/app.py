"""FastAPI application factory.

Composes the api/ package into a deployable ASGI app. All cross-cutting
concerns (CORS, lifespan, error handlers, structured logging) live here;
routers are pure HTTP handlers.

Runtime: `uvicorn api.app:app --host 0.0.0.0 --port 8080`
Container entry-point in docker-compose.{dev,prod}.yml.
"""

from contextlib import asynccontextmanager

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware

from api.settings import settings
from api.sse import SSEManager


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Wire process-scoped resources (SSE manager) onto app state."""
    app.state.sse_manager = SSEManager(
        heartbeat_interval_s=settings.SSE_HEARTBEAT_INTERVAL_S,
        max_queue_per_connection=settings.SSE_MAX_QUEUE_PER_CONNECTION,
    )
    yield
    # MongoDB client cleanup is handled by pymongo's atexit.


def create_app() -> FastAPI:
    app = FastAPI(
        title="found-footy API",
        description=(
            "Read API + SSE for the found-footy goal-video pipeline. "
            "See docs/api-contract.md for the URL stability + SSE envelope contract."
        ),
        version="1.0.0",
        lifespan=lifespan,
        # SSE breaks behind the FastAPI's auto-redirect on trailing-slash;
        # disable to keep the contract URL-stable.
        redirect_slashes=False,
    )

    app.add_middleware(
        CORSMiddleware,
        allow_origins=settings.CORS_ORIGINS,
        allow_methods=["GET", "POST", "OPTIONS"],
        allow_headers=["*"],
        expose_headers=["Content-Range", "Accept-Ranges", "Content-Length"],
    )

    # Register routers under /api/v1. The prefix is part of the URL
    # stability contract — if we ever ship /api/v2, we add it as a
    # parallel prefix; never break /api/v1.
    from api.routers import events, fixtures, health, internal, search, stream

    app.include_router(health.router, prefix="/api/v1", tags=["health"])
    app.include_router(fixtures.router, prefix="/api/v1", tags=["fixtures"])
    app.include_router(events.router, prefix="/api/v1", tags=["events"])
    app.include_router(search.router, prefix="/api/v1", tags=["search"])
    app.include_router(stream.router, prefix="/api/v1", tags=["stream"])
    app.include_router(internal.router, prefix="/api/v1", tags=["internal"])

    return app


app = create_app()
