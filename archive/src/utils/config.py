"""
Centralized configuration for Found Footy

All environment variables and defaults are defined here.
Import from this module instead of hardcoding values.

NOTE: Credentials are passed via docker-compose.yml environment variables.
This file only provides fallback defaults for local development.
"""

import os

# =============================================================================
# LLM Configuration
# Set LLAMA_URL and LLAMA_EMBED_URL in .env or docker-compose environment.
# Defaults to localhost for local development / testing.
# =============================================================================

# Chat + Vision model
LLAMA_CHAT_URL = os.getenv("LLAMA_URL", "http://localhost:8080")

# Embedding model (not currently used by found-footy)
# LLAMA_EMBED_URL = os.getenv("LLAMA_EMBED_URL", "http://localhost:8081")

# =============================================================================
# Database Configuration
# Credentials passed via MONGODB_URI env var from docker-compose.yml
# =============================================================================

MONGODB_URI = os.getenv("MONGODB_URI") or os.getenv("MONGODB_URL", "mongodb://localhost:27017/")

# =============================================================================
# MinIO S3-Compatible Storage Configuration
# Credentials passed via S3_* env vars from docker-compose.yml
# =============================================================================

S3_ENDPOINT_URL = os.getenv("S3_ENDPOINT_URL", "http://localhost:9000")
S3_ACCESS_KEY = os.getenv("S3_ACCESS_KEY", "")  # Required - no default
S3_SECRET_KEY = os.getenv("S3_SECRET_KEY", "")  # Required - no default
S3_BUCKET_NAME = os.getenv("S3_BUCKET_NAME", "footy-videos")
# MinIO doesn't use regions, but boto3 requires one - use dummy value
S3_REGION = "us-east-1"

# =============================================================================
# External APIs
# =============================================================================

API_FOOTBALL_BASE_URL = os.getenv("API_FOOTBALL_BASE_URL", "https://v3.football.api-sports.io")

# =============================================================================
# Video Processing Configuration
# =============================================================================

# Short edge filter: reject low-resolution videos
# 720p HD has short edge of 720px, but letterboxed HD (1280x686) has less
# Using 680px as threshold to allow letterboxed HD content
SHORT_EDGE_FILTER_ENABLED = True
MIN_SHORT_EDGE = 600  # Pixels - allows letterboxed 720p content

# Aspect ratio filter: standard broadcast 16:9 only (target = 1.7778).
# Bounds picked from prod S3 distribution as of 2026-06-30: 81% of accepted
# videos sit in the 1.78-1.79 bucket; the 1.77-1.80 band covers ~84% on its
# own; widening to 1.75-1.82 absorbs encoder-side padding/cropping artifacts
# (e.g. 1280x722 = 1.7729, 1280x705 = 1.8156) without admitting letterboxed
# 16:10 broadcasts (~1.60-1.72) or cinema-cropped clips (>=1.85).
# Phone-TV recordings are additionally filtered by AI vision downstream.
ASPECT_RATIO_FILTER_ENABLED = True
MIN_ASPECT_RATIO = 1.75
MAX_ASPECT_RATIO = 1.82

# Duration filters
MIN_VIDEO_DURATION = 3.0  # Seconds (must be > 3s)
MAX_VIDEO_DURATION = 90.0  # Seconds

# Perceptual hash configuration
HASH_SAMPLE_INTERVAL = 0.25  # Sample frame every 0.25 seconds
HASH_VERSION = "dense:0.25"  # Track algorithm version in MongoDB
MAX_HAMMING_DISTANCE = 10  # Max bit difference for duplicate detection
MIN_CONSECUTIVE_MATCHES = 3  # Min consecutive frames that must match (0.75s at 0.25s interval)
