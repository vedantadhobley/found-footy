"""Centralized configuration for the api/ FastAPI service.

Env-driven with sane local-dev defaults. Mirrors the convention used by
src/utils/config.py for the worker side; deliberately kept independent
so the api/ package can be deployed without dragging the worker module
graph in.
"""

import os
from typing import List


class Settings:
    # Database
    MONGODB_URI: str = os.getenv("MONGODB_URI", "mongodb://localhost:27017")
    MONGODB_DB_NAME: str = os.getenv("MONGODB_DB_NAME", "found_footy")

    # S3 / MinIO (for video proxy in a later commit)
    S3_ENDPOINT: str = os.getenv("S3_ENDPOINT", "http://localhost:9000")
    S3_ACCESS_KEY: str = os.getenv("S3_ACCESS_KEY", "minioadmin")
    S3_SECRET_KEY: str = os.getenv("S3_SECRET_KEY", "minioadmin")
    S3_BUCKET: str = os.getenv("S3_BUCKET", "footy-videos")
    S3_USE_SSL: bool = os.getenv("S3_USE_SSL", "false").lower() == "true"

    # HTTP / CORS
    # Production should set CORS_ORIGINS explicitly to the vedanta.systems
    # origin; "*" is for dev convenience only.
    CORS_ORIGINS: List[str] = [
        o.strip() for o in os.getenv("CORS_ORIGINS", "*").split(",") if o.strip()
    ]

    # SSE
    SSE_HEARTBEAT_INTERVAL_S: int = int(os.getenv("SSE_HEARTBEAT_INTERVAL_S", "30"))
    SSE_MAX_QUEUE_PER_CONNECTION: int = int(os.getenv("SSE_MAX_QUEUE_PER_CONNECTION", "100"))

    # Internal endpoint guard — worker uses this to authenticate
    # /internal/notify calls. Empty string in dev means "no auth required"
    # (relies on docker network isolation); set explicitly for prod.
    INTERNAL_TOKEN: str = os.getenv("INTERNAL_TOKEN", "")

    LOG_LEVEL: str = os.getenv("LOG_LEVEL", "INFO")
    LOG_FORMAT: str = os.getenv("LOG_FORMAT", "json")


settings = Settings()
