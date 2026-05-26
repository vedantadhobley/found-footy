"""Download activities - Video download, validation, and hash generation for DownloadWorkflow"""
from temporalio import activity
from typing import Dict, List, Any, Optional
import os
import hashlib
import asyncio
import json
import re

from src.utils.footy_logging import log
from src.utils.orchestration_config import LLM_CONCURRENCY_PER_WORKER
from src.utils.errors import (
    LLMTimeoutError,
    LLMUnavailableError,
    LLMValidationError,
    TwitterRateLimitedError,
    VideoCDNTimeoutError,
    VideoDownloadError,
    VideoGeoRestrictedError,
    VideoMalformedURLError,
    VideoNotAvailableError,
)
from src.utils.config import (
    LLAMA_CHAT_URL,
    SHORT_EDGE_FILTER_ENABLED,
    MIN_SHORT_EDGE,
    ASPECT_RATIO_FILTER_ENABLED,
    MIN_ASPECT_RATIO,
    MIN_VIDEO_DURATION,
    MAX_VIDEO_DURATION,
)
# Twitter cookie cache (loaded once per worker)
_twitter_cookies_cache: Optional[Dict[str, str]] = None
_twitter_cookies_loaded = False

# Path to Twitter cookie backup (JSON format from twitter service)
TWITTER_COOKIE_BACKUP_PATH = os.getenv("TWITTER_COOKIE_BACKUP_PATH", "/config/twitter_cookies.json")

def _load_twitter_cookies() -> Dict[str, str]:
    """Load Twitter cookies from JSON backup file.
    
    Returns dict of cookie name -> value for use with httpx.
    Caches result to avoid repeated file reads.
    """
    global _twitter_cookies_cache, _twitter_cookies_loaded
    
    if _twitter_cookies_loaded:
        return _twitter_cookies_cache or {}
    
    _twitter_cookies_loaded = True
    
    if not os.path.exists(TWITTER_COOKIE_BACKUP_PATH):
        log.warning(activity.logger, "download", "cookies_missing",
                    "No cookie file found", path=TWITTER_COOKIE_BACKUP_PATH)
        return {}
    
    try:
        with open(TWITTER_COOKIE_BACKUP_PATH, 'r') as f:
            data = json.load(f)
        
        cookies = {}
        for cookie in data.get("cookies", []):
            name = cookie.get("name")
            value = cookie.get("value")
            if name and value:
                cookies[name] = value
        
        # Log which auth cookies we have (without values)
        auth_cookies = ["auth_token", "ct0", "twid", "guest_id"]
        found = [c for c in auth_cookies if c in cookies]
        log.info(activity.logger, "download", "cookies_loaded",
                 "Loaded Twitter cookies", count=len(cookies), auth_cookies=found)
        
        _twitter_cookies_cache = cookies
        return cookies
        
    except Exception as e:
        log.warning(activity.logger, "download", "cookies_load_failed",
                    "Failed to load cookies", error=str(e))
        return {}

# =============================================================================
# Workflow Tracking Activities
# =============================================================================

@activity.defn
async def register_download_workflow(
    fixture_id: int,
    event_id: str,
    workflow_id: str
) -> Dict[str, Any]:
    """
    Register a DownloadWorkflow as having started for this event.
    
    Called by DownloadWorkflow at the VERY START of its run.
    Uses $addToSet for idempotency - same workflow ID won't double-count.
    
    This is the key to the new tracking approach:
    - If workflow starts → registers → we know it ran
    - If workflow fails to start → doesn't register → count stays low → TwitterWorkflow retries
    - If workflow crashes and restarts → re-registers → no-op (idempotent)
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID
        workflow_id: The DownloadWorkflow ID (e.g., "download1-Everton-Barry-1379194_45_343684_Goal_1")
    
    Returns:
        Dict with success, count (current count after registration)
    """
    from src.data.mongo_store import FootyMongoStore, get_store
    
    store = get_store()
    
    try:
        # Add workflow ID to array (idempotent via $addToSet)
        success = store.add_download_workflow(fixture_id, event_id, workflow_id)
        
        # Get current count after registration
        count = store.get_download_workflow_count(fixture_id, event_id)
        
        log.info(activity.logger, "download", "workflow_registered",
                 "Registered download workflow",
                 event_id=event_id, workflow_id=workflow_id, count=count, success=success)
        
        return {
            "success": success,
            "count": count
        }
    except Exception as e:
        log.error(activity.logger, "download", "workflow_register_failed",
                  "Failed to register download workflow",
                  event_id=event_id, error=str(e), error_type=type(e).__name__)
        return {
            "success": False,
            "count": 0
        }

@activity.defn
async def check_and_mark_download_complete(
    fixture_id: int,
    event_id: str,
    required_count: int = 10
) -> Dict[str, Any]:
    """
    Check if _download_workflows count >= required_count and mark _download_complete if so.
    
    This is the new idempotent replacement for increment_twitter_count_and_check_complete.
    Called by UploadWorkflow after each batch and as a failsafe before idle timeout exit.
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID
        required_count: Number of download workflows required for completion (default 10)
    
    Returns:
        Dict with count, was_already_complete, marked_complete
    """
    from src.data.mongo_store import FootyMongoStore, get_store
    
    store = get_store()
    
    try:
        result = store.check_and_mark_download_complete(fixture_id, event_id, required_count)
        
        log.info(activity.logger, "download", "completion_check",
                 "Checked download completion",
                 event_id=event_id, count=result['count'], required=required_count,
                 was_complete=result['was_already_complete'], marked=result['marked_complete'])
        
        return result
    except Exception as e:
        log.error(activity.logger, "download", "completion_check_failed",
                  "Failed to check download completion",
                  event_id=event_id, error=str(e), error_type=type(e).__name__)
        return {
            "count": 0,
            "was_already_complete": False,
            "marked_complete": False
        }

@activity.defn
async def download_single_video(
    video_url: str, 
    video_index: int, 
    event_id: str, 
    temp_dir: str,
    source_tweet_url: str = ""
) -> Dict[str, Any]:
    """
    Download video from a tweet using Twitter's syndication API + direct CDN download.
    
    This approach:
    1. Extracts tweet_id from the URL
    2. Calls syndication API to get video CDN URLs (no auth required)
    3. Downloads best quality variant directly from video.twimg.com CDN
    
    Benefits over yt-dlp:
    - No rate limiting (CDN doesn't require auth)
    - Fully parallelizable (just HTTP GETs)
    - Better quality selection (we see all variants with bitrates)
    - Faster (no yt-dlp overhead)
    
    Args:
        video_url: URL to download (video page URL from Twitter)
        video_index: Index of this video in the list (used for filename prefix)
        event_id: Event ID for filename
        temp_dir: Temporary directory to download to
        source_tweet_url: Original tweet URL (for dedup checking)
    
    Returns:
        Dict with status and video info
    
    Raises:
        Exception: If download fails (for Temporal retry)
    """
    import httpx
    import re
    
    # Ensure temp directory exists
    os.makedirs(temp_dir, exist_ok=True)
    
    log.info(activity.logger, "download", "download_started",
             "Starting video download",
             event_id=event_id, video_idx=video_index, url=video_url[:60])
    
    # Extract tweet_id from URL.
    # Handles: https://x.com/user/status/123, https://x.com/i/status/123, https://twitter.com/...
    tweet_id_match = re.search(r'/status/(\d+)', video_url)
    if not tweet_id_match:
        err = VideoMalformedURLError(
            f"Could not extract tweet_id from URL: {video_url}",
            context={"event_id": event_id, "video_idx": video_index, "url": video_url,
                     "failure_mode": "no_status_match"},
        )
        log.error(activity.logger, "download", "tweet_id_extraction_failed",
                  "Could not extract tweet_id from URL",
                  **err.log_fields())
        raise err

    tweet_id = tweet_id_match.group(1)

    # Snowflake ID length sanity check.
    # Twitter snowflakes are time-based 64-bit IDs and have been ≥18 digits
    # since ~early 2020. Shorter IDs almost always indicate an upstream
    # truncation bug (we saw 13/14/17-digit IDs in the 2026-05-25
    # Paderborn-Wolfsburg post-mortem). Reject early with a typed error
    # so the failure shape is visible in Grafana via the
    # `failure_mode=truncated_snowflake` field rather than failing later
    # at the syndication API with a generic 404.
    #
    # Lower bound is conservative — a few legitimate ancient tweets may
    # still have 16-17 digit IDs, but none would surface as live goal
    # clips. Tighten/loosen here if a false-positive class emerges.
    MIN_SNOWFLAKE_LEN = 18
    if len(tweet_id) < MIN_SNOWFLAKE_LEN:
        err = VideoMalformedURLError(
            f"Tweet status ID too short ({len(tweet_id)} digits, expected ≥{MIN_SNOWFLAKE_LEN}): {tweet_id}",
            context={
                "event_id": event_id,
                "video_idx": video_index,
                "url": video_url,
                "tweet_id": tweet_id,
                "tweet_id_len": len(tweet_id),
                "min_expected_len": MIN_SNOWFLAKE_LEN,
                "failure_mode": "truncated_snowflake",
            },
        )
        log.warning(activity.logger, "download", "tweet_id_too_short",
                    "Rejecting URL with truncated snowflake ID",
                    **err.log_fields())
        raise err
    
    # Call syndication API to get video info
    syndication_url = f"https://cdn.syndication.twimg.com/tweet-result?id={tweet_id}&token=x"
    
    # Browser-like headers to avoid blocks
    api_headers = {
        "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
        "Accept": "application/json",
        "Referer": "https://twitter.com/",
    }
    
    try:
        # 5s timeout - if syndication API doesn't respond quickly, it's probably going to fail
        # Longer timeouts just delay the inevitable retry
        async with httpx.AsyncClient(timeout=5.0, headers=api_headers) as client:
            response = await client.get(syndication_url)

            if response.status_code != 200:
                err_ctx = {
                    "event_id": event_id,
                    "video_idx": video_index,
                    "tweet_id": tweet_id,
                    "status_code": response.status_code,
                }
                if response.status_code == 404:
                    err = VideoNotAvailableError(
                        f"Syndication 404 (tweet deleted or private): {tweet_id}",
                        context=err_ctx,
                    )
                elif response.status_code == 403:
                    err = VideoGeoRestrictedError(
                        f"Syndication 403 (likely geo-blocked): {tweet_id}",
                        context=err_ctx,
                    )
                elif response.status_code == 429:
                    err = TwitterRateLimitedError(
                        f"Syndication 429 (rate limited)",
                        context=err_ctx,
                    )
                else:
                    err = VideoDownloadError(
                        f"Syndication API returned {response.status_code}",
                        context=err_ctx,
                    )
                log.warning(activity.logger, "download", "syndication_api_error",
                            "Syndication API returned error",
                            **err.log_fields())
                raise err

            tweet_data = response.json()
    except httpx.TimeoutException as e:
        err = VideoCDNTimeoutError(
            "Syndication API timeout",
            context={"event_id": event_id, "video_idx": video_index, "tweet_id": tweet_id},
        )
        log.warning(activity.logger, "download", "syndication_timeout",
                    "Syndication API timeout", **err.log_fields())
        raise err from e
    except VideoDownloadError:
        # Already classified above; let it propagate
        raise
    except Exception as e:
        err = VideoDownloadError(
            f"Syndication API unexpected error: {str(e)[:100]}",
            context={
                "event_id": event_id,
                "video_idx": video_index,
                "tweet_id": tweet_id,
                "error_detail": str(e)[:200],
            },
        )
        log.warning(activity.logger, "download", "syndication_error",
                    "Syndication API error", **err.log_fields())
        raise err from e
    
    # Extract video variants from response
    # Prefer mediaDetails path as it includes bitrate info
    video_variants = []
    video_width = None
    video_height = None
    
    # Path 1 (preferred): mediaDetails[].video_info.variants - has bitrates
    if "mediaDetails" in tweet_data:
        for media in tweet_data.get("mediaDetails", []):
            # Get original video dimensions for pre-download filtering
            original_info = media.get("original_info", {})
            if original_info.get("width") and original_info.get("height"):
                video_width = original_info["width"]
                video_height = original_info["height"]
            
            if "video_info" in media and "variants" in media["video_info"]:
                video_variants.extend(media["video_info"]["variants"])
    
    # PRE-DOWNLOAD FILTERING: Check resolution/aspect ratio BEFORE downloading
    # This saves bandwidth and time by rejecting videos based on metadata
    if video_width and video_height:
        # Short edge filter
        if SHORT_EDGE_FILTER_ENABLED:
            short_edge = min(video_width, video_height)
            if short_edge < MIN_SHORT_EDGE:
                log.info(activity.logger, "download", "filtered_resolution",
                         "Pre-filtered: low resolution",
                         video_idx=video_index, short_edge=short_edge,
                         min_edge=MIN_SHORT_EDGE, width=video_width, height=video_height)
                return {"status": "filtered", "reason": "pre_filter_resolution", "source_url": source_tweet_url}
        
        # Aspect ratio filter (reject portrait/square videos)
        if ASPECT_RATIO_FILTER_ENABLED and video_height > 0:
            aspect_ratio = video_width / video_height
            if aspect_ratio < MIN_ASPECT_RATIO:
                log.info(activity.logger, "download", "filtered_aspect",
                         "Pre-filtered: portrait/square",
                         video_idx=video_index, aspect_ratio=round(aspect_ratio, 2),
                         min_ratio=MIN_ASPECT_RATIO, width=video_width, height=video_height)
                return {"status": "filtered", "reason": "pre_filter_aspect_ratio", "source_url": source_tweet_url}
    
    # Path 2 (fallback): video.variants - often missing bitrates
    if not video_variants and "video" in tweet_data and "variants" in tweet_data["video"]:
        video_variants = tweet_data["video"]["variants"]
    
    if not video_variants:
        log.warning(activity.logger, "download", "no_variants",
                    "No video variants in syndication response", video_idx=video_index)
        raise RuntimeError("No video variants found in syndication response")
    
    # Filter to mp4 only and sort by bitrate (highest first)
    mp4_variants = [
        v for v in video_variants 
        if v.get("type") == "video/mp4" or v.get("content_type") == "video/mp4"
    ]
    
    if not mp4_variants:
        log.warning(activity.logger, "download", "no_mp4_variants",
                    "No MP4 variants found", video_idx=video_index)
        raise RuntimeError("No MP4 variants found")
    
    # Sort by bitrate descending, pick best quality
    mp4_variants.sort(key=lambda v: v.get("bitrate", 0), reverse=True)
    best_variant = mp4_variants[0]
    cdn_url = best_variant.get("src") or best_variant.get("url")
    bitrate = best_variant.get("bitrate", 0)
    
    # Check if this is amplify_video (promoted content that may need auth)
    is_amplify = "amplify_video" in cdn_url if cdn_url else False
    
    log.info(activity.logger, "download", "variant_selected",
             "Selected best video variant",
             video_idx=video_index, variant_count=len(mp4_variants),
             bitrate_kbps=int(bitrate/1000), is_amplify=is_amplify)
    
    # Download from CDN
    output_path = os.path.join(temp_dir, f"{event_id}_{video_index}_01.mp4")
    
    # Load Twitter cookies for authenticated CDN requests
    twitter_cookies = _load_twitter_cookies()
    
    # Browser-like headers to avoid CDN 403 blocks
    cdn_headers = {
        "User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
        "Accept": "*/*",
        "Accept-Language": "en-US,en;q=0.9",
        "Accept-Encoding": "identity",
        "Referer": "https://twitter.com/",
        "Origin": "https://twitter.com",
    }
    
    # Add CSRF token header if we have cookies (required for some Twitter endpoints)
    if twitter_cookies.get("ct0"):
        cdn_headers["x-csrf-token"] = twitter_cookies["ct0"]
    
    # Build cookie string for httpx
    cookie_str = "; ".join(f"{k}={v}" for k, v in twitter_cookies.items()) if twitter_cookies else ""
    
    try:
        async with httpx.AsyncClient(timeout=60.0, headers=cdn_headers) as client:
            # Add cookies to the request
            request_cookies = twitter_cookies if twitter_cookies else None
            async with client.stream("GET", cdn_url, cookies=request_cookies) as response:
                if response.status_code != 200:
                    err_ctx = {
                        "event_id": event_id,
                        "video_idx": video_index,
                        "tweet_id": tweet_id,
                        "status_code": response.status_code,
                        "cdn_host": cdn_url.split("/")[2] if "//" in cdn_url else "?",
                        "is_amplify": is_amplify,
                    }
                    if response.status_code == 403:
                        err_ctx["has_auth"] = "auth_token" in twitter_cookies
                        err = VideoGeoRestrictedError(
                            f"CDN 403 (geo-blocked or auth required)",
                            context=err_ctx,
                        )
                        log.warning(activity.logger, "download", "cdn_auth_failed",
                                    "CDN returned 403", **err.log_fields())
                    elif response.status_code == 404:
                        err = VideoNotAvailableError(
                            f"CDN 404 (video removed)", context=err_ctx,
                        )
                        log.warning(activity.logger, "download", "cdn_error",
                                    "CDN returned 404", **err.log_fields())
                    else:
                        err = VideoDownloadError(
                            f"CDN returned {response.status_code}", context=err_ctx,
                        )
                        log.warning(activity.logger, "download", "cdn_error",
                                    "CDN returned error", **err.log_fields())
                    raise err

                with open(output_path, 'wb') as f:
                    async for chunk in response.aiter_bytes(chunk_size=8192):
                        f.write(chunk)
    except httpx.TimeoutException as e:
        err = VideoCDNTimeoutError(
            "CDN download timeout",
            context={"event_id": event_id, "video_idx": video_index, "tweet_id": tweet_id},
        )
        log.warning(activity.logger, "download", "cdn_timeout",
                    "CDN download timeout", **err.log_fields())
        raise err from e
    except VideoDownloadError:
        # Already classified above; let it propagate
        raise
    except Exception as e:
        err = VideoDownloadError(
            f"CDN unexpected error: {str(e)[:100]}",
            context={
                "event_id": event_id,
                "video_idx": video_index,
                "tweet_id": tweet_id,
                "error_detail": str(e)[:200],
            },
        )
        log.warning(activity.logger, "download", "cdn_download_error",
                    "CDN download error", **err.log_fields())
        raise err from e
    
    # Verify file exists and has content
    if not os.path.exists(output_path):
        log.error(activity.logger, "download", "file_missing",
                  "No file after download", video_idx=video_index)
        raise RuntimeError(f"No file after download | video_idx={video_index}")
    
    file_size = os.path.getsize(output_path)
    if file_size < 1000:  # Less than 1KB is likely an error
        log.error(activity.logger, "download", "file_too_small",
                  "Downloaded file too small",
                  video_idx=video_index, file_size=file_size)
        os.remove(output_path)
        raise RuntimeError(f"File too small ({file_size} bytes) | video_idx={video_index}")
    
    log.info(activity.logger, "download", "downloaded",
             "Video downloaded successfully",
             video_idx=video_index, file_size_mb=round(file_size / 1024 / 1024, 2))
    
    # Process the downloaded video (metadata, filters)
    result = _process_downloaded_video(
        output_path, 
        video_index, 
        0,  # sub_index 
        source_tweet_url,
        False  # is_multi_video - syndication API returns single best video
    )
    
    if result is None:
        return {"status": "filtered", "reason": "basic_filter", "source_url": source_tweet_url}
    
    return result

# ==============================================================================
# Helper functions for download activities
# ==============================================================================

def _process_downloaded_video(
    output_path: str,
    video_index: int,
    sub_index: int,
    source_tweet_url: str,
    is_multi_video: bool = False
) -> Optional[Dict[str, Any]]:
    """
    Process a single downloaded video file - get metadata and apply basic filters.
    
    NOTE: Does NOT generate perceptual hash here. Hash generation happens AFTER
    AI validation to avoid wasting compute on non-soccer videos.
    
    Args:
        output_path: Path to the downloaded video file
        video_index: Main video index
        sub_index: Sub-index within multi-video tweet (0 for single video)
        source_tweet_url: Original tweet URL
        is_multi_video: Whether this is from a multi-video tweet
        
    Returns:
        Dict with video info (no perceptual_hash yet) or None if filtered out
    """
    from temporalio import activity
    
    display_idx = f"{video_index}.{sub_index}" if is_multi_video else str(video_index)
    
    # Get video metadata (duration, resolution, bitrate)
    video_meta = _get_video_metadata(output_path)
    duration = video_meta["duration"]
    width = video_meta["width"]
    height = video_meta["height"]
    bitrate = video_meta["bitrate"]
    
    # Duration filter: typical goal clips
    # Must be strictly greater than MIN (e.g., exactly 3.00s fails)
    if duration <= MIN_VIDEO_DURATION:
        log.info(activity.logger, "download", "filtered_duration",
                 "Filtered: too short",
                 video_idx=display_idx, duration=round(duration, 1),
                 min_duration=MIN_VIDEO_DURATION)
        os.remove(output_path)
        return None
    
    if duration > MAX_VIDEO_DURATION:
        log.info(activity.logger, "download", "filtered_duration",
                 "Filtered: too long",
                 video_idx=display_idx, duration=round(duration, 1),
                 max_duration=MAX_VIDEO_DURATION)
        os.remove(output_path)
        return None
    
    # Short edge filter: reject low-resolution videos
    # 720p has short edge 720, but letterboxed HD has ~686px
    if SHORT_EDGE_FILTER_ENABLED and width and height:
        short_edge = min(width, height)
        if short_edge < MIN_SHORT_EDGE:
            log.info(activity.logger, "download", "filtered_resolution",
                     "Filtered: low resolution",
                     video_idx=display_idx, short_edge=short_edge,
                     min_edge=MIN_SHORT_EDGE, width=width, height=height)
            os.remove(output_path)
            return None
    
    # Aspect ratio filter: reject portrait/square videos (< 4:3)
    # Disabled by default to allow stadium phone recordings
    # Phone-TV recordings are filtered by AI vision instead
    if ASPECT_RATIO_FILTER_ENABLED and width and height and height > 0:
        aspect_ratio = width / height
        if aspect_ratio < MIN_ASPECT_RATIO:
            log.info(activity.logger, "download", "filtered_aspect",
                     "Filtered: aspect ratio",
                     video_idx=display_idx, aspect_ratio=round(aspect_ratio, 2),
                     min_ratio=MIN_ASPECT_RATIO)
            os.remove(output_path)
            return None
    
    # Calculate MD5 hash and size (fast)
    file_hash = _calculate_md5(output_path)
    file_size = os.path.getsize(output_path)
    
    quality_info = f"{width}x{height}" if width and height else "unknown res"
    if bitrate:
        quality_info += f"@{bitrate:.0f}kbps"
    
    log.info(activity.logger, "download", "video_ready",
             "Video ready for validation",
             video_idx=display_idx, file_size_mb=round(file_size / 1024 / 1024, 2),
             duration=round(duration, 1), width=width, height=height,
             bitrate_kbps=int(bitrate) if bitrate else 0)
    
    # Return without perceptual_hash - will be generated after AI validation
    return {
        "status": "success",
        "file_path": output_path,
        "file_hash": file_hash,
        "perceptual_hash": "",  # Placeholder - generated after AI validation
        "duration": duration,
        "file_size": file_size,
        "video_index": video_index,
        "sub_index": sub_index,
        "source_url": source_tweet_url,
        "width": width,
        "height": height,
        "bitrate": bitrate,
        "resolution_score": width * height if width and height else 0,
    }

def _calculate_md5(file_path: str) -> str:
    """Calculate MD5 hash of file for deduplication"""
    hash_md5 = hashlib.md5()
    with open(file_path, "rb") as f:
        for chunk in iter(lambda: f.read(4096), b""):
            hash_md5.update(chunk)
    return hash_md5.hexdigest()

def _get_video_metadata(file_path: str) -> dict:
    """
    Get video metadata (width, height, bitrate) using ffprobe.
    
    Args:
        file_path: Path to video file
        
    Returns:
        Dict with width, height, bitrate, duration
    """
    import subprocess
    import json
    
    result = {
        "width": 0,
        "height": 0,
        "bitrate": 0.0,
        "duration": 0.0,
    }
    
    try:
        cmd = [
            "ffprobe",
            "-v", "quiet",
            "-print_format", "json",
            "-show_format",
            "-show_streams",
            file_path
        ]
        proc = subprocess.run(cmd, capture_output=True, text=True, timeout=10)
        if proc.returncode == 0:
            data = json.loads(proc.stdout)
            
            # Get duration from format
            format_info = data.get("format", {})
            result["duration"] = float(format_info.get("duration", 0))
            
            # Get bitrate from format (in bits/s, convert to kbps)
            bit_rate = format_info.get("bit_rate")
            if bit_rate:
                result["bitrate"] = float(bit_rate) / 1000  # Convert to kbps
            
            # Find video stream for resolution
            for stream in data.get("streams", []):
                if stream.get("codec_type") == "video":
                    result["width"] = stream.get("width", 0)
                    result["height"] = stream.get("height", 0)
                    break
                    
    except Exception as e:
        log.warning(activity.logger, "download", "metadata_failed",
                    "Failed to get video metadata",
                    file_path=file_path, error=str(e))
    
    return result

@activity.defn
async def cleanup_download_temp(temp_dir: str) -> bool:
    """
    Clean up temporary directory.
    Called by DownloadWorkflow on failure to clean up any downloaded files.
    
    Args:
        temp_dir: Path to the temporary directory to delete
    
    Returns:
        True if successful
    """
    import shutil
    
    if temp_dir and os.path.exists(temp_dir):
        shutil.rmtree(temp_dir)
        log.info(activity.logger, "download", "temp_cleaned",
                 "Cleaned up temp directory", path=temp_dir)
        return True
    
    return False



# =============================================================================
# Back-compat re-exports (Phase 3 P3a, 2026-05-26)
#
# Vision + hashing code was extracted to dedicated modules. Callers using
# `from src.activities import download as download_activities` continue to
# access these symbols here via these re-exports — no caller-side changes.
#
# New code should import from the specific module:
#   from src.activities.vision import validate_video_is_soccer
#   from src.activities.hashing import generate_video_hash
# =============================================================================

from src.activities.vision import (
    parse_broadcast_clock,
    parse_clock_field,
    parse_added_field,
    parse_stoppage_clock_field,
    compute_absolute_minute,
    validate_timestamp,
    validate_video_is_soccer,
    _extract_frame_for_vision,
    _call_vision_model,
)
from src.activities.hashing import (
    generate_video_hash,
    _generate_perceptual_hash,
)

# Re-export queue_videos_for_upload from upload module so DownloadWorkflow can use it
from src.activities.upload import queue_videos_for_upload
