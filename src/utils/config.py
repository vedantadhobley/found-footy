"""
Centralized configuration for Found Footy

All environment variables and defaults are defined here.
Import from this module instead of hardcoding values.

NOTE: Credentials are passed via docker-compose.yml environment variables.
This file only provides fallback defaults for local development.
"""

import os

# =============================================================================
# LLM Configuration (llama.cpp server on external luv network)
# =============================================================================

# Chat + Vision model (Qwen3-VL-8B)
# Container: llama-chat on luv-prod/luv-dev network
LLAMA_CHAT_URL = os.getenv("LLAMA_URL", "http://llama-chat:8080")

# Embedding model (Qwen3-Embedding-8B)
# Container: llama-embed on luv-prod/luv-dev network  
LLAMA_EMBED_URL = os.getenv("LLAMA_EMBED_URL", "http://llama-embed:8080")

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

# Aspect ratio filter: reject portrait/square videos (< 4:3)
# Filters videos taller than 4:3 (portrait orientation)
# Phone-TV recordings are additionally filtered by AI vision
ASPECT_RATIO_FILTER_ENABLED = True
MIN_ASPECT_RATIO = 1.32  # 4:3 = 1.333..., use 1.32 with tolerance

# Duration filters
MIN_VIDEO_DURATION = 3.0  # Seconds (must be > 3s)
MAX_VIDEO_DURATION = 60.0  # Seconds

# Perceptual hash configuration
HASH_SAMPLE_INTERVAL = 0.25  # Sample frame every 0.25 seconds
HASH_VERSION = "dense:0.25"  # Track algorithm version in MongoDB
MAX_HAMMING_DISTANCE = 10  # Max bit difference for duplicate detection
MIN_CONSECUTIVE_MATCHES = 3  # Min consecutive frames that must match
