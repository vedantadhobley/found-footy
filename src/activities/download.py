"""Download activities - Video download, validation, and hash generation for DownloadWorkflow"""
from temporalio import activity
from typing import Dict, List, Any, Optional
import os
import hashlib
import asyncio
import json
import re

from src.utils.footy_logging import log
from src.utils.config import (
    LLAMA_CHAT_URL,
    SHORT_EDGE_FILTER_ENABLED,
    MIN_SHORT_EDGE,
    ASPECT_RATIO_FILTER_ENABLED,
    MIN_ASPECT_RATIO,
    MIN_VIDEO_DURATION,
    MAX_VIDEO_DURATION,
)

# Global lock and timestamp to rate-limit downloads across all workers

# Semaphore to limit concurrent LLM requests per worker process.
# joi llama-chat runs --parallel 4 with 2 workers, so allow 2 concurrent per worker.
_LLM_SEMAPHORE = asyncio.Semaphore(2)
_download_lock = asyncio.Lock()
_last_download_time = 0

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
# Broadcast Clock Parsing
# =============================================================================

def parse_broadcast_clock(raw_clock: str | None) -> int | None:
    """
    Parse raw broadcast clock text into absolute match minute.
    
    Broadcast clocks appear in 5 distinct format categories:
    
    A) Running match clock (most common):
       "34:12" → 34, "84:28" → 84, "112:54" → 112
    
    B) Period indicator + time (relative or absolute):
       "2H 5:00" → 50 (relative: 45+5)
       "2H 67:00" → 67 (absolute: 67 >= 45)
       "ET 04:04" → 94 (relative: 90+4)
       "ET 102:53" → 102 (absolute: 102 >= 90)
    
    C) Compact stoppage (base + added minutes):
       "45+2" → 47, "45+2:30" → 47, "90+3" → 93
    
    D) Broadcast stoppage display (frozen base + allocated time + sub-clock):
       "45:00 +2 00:43" → 45 (base=45, sub-clock 0 mins elapsed)
       "90:00 +4 02:17" → 92 (base=90, sub-clock 2 mins elapsed)
       "90:00 +5 03:45" → 93 (base=90, sub-clock 3 mins elapsed)
       The main clock freezes at the period boundary (45:00, 90:00).
       +N is the total allocated stoppage time.
       MM:SS after that is a running sub-clock within the stoppage.
       Actual minute = base_minutes + sub-clock_minutes.
    
    E) Base time + stoppage (no space, colon in base):
       "90:00+3:15" → 93, "45:00+2" → 47
    
    Smart detection for relative vs absolute with period indicators:
    - ET + minutes >= 90 → already absolute (don't add 90)
    - ET + minutes <= 30 → relative to ET start (add 90)
    - 2H + minutes >= 45 → already absolute (don't add 45)
    - 2H + minutes < 45 → relative to 2H start (add 45)
    
    NOTE: With the structured prompt, Patterns D and E are handled by
    separate ADDED/STOPPAGE_CLOCK fields. This function is kept as a
    fallback for the combined CLOCK field parsing.
    
    Args:
        raw_clock: Raw text from AI vision model (e.g., "45:00 +2 00:43")
    
    Returns:
        Absolute match minute (0-120+), or None if unparseable
    """
    if not raw_clock or raw_clock.upper() in ("NONE", "HT", "FT", "HALF TIME", "FULL TIME"):
        return None
    
    text = raw_clock.upper().strip()
    
    # Detect period indicators (but don't apply offset yet)
    has_et = bool(re.search(r'\b(ET|AET|EXTRA\s*TIME)\b', text))
    has_2h = bool(re.search(r'\b(2H|2ND\s*HALF)\b', text))
    has_1h = bool(re.search(r'\b(1H|1ST\s*HALF)\b', text))
    
    # Remove indicators for time parsing
    clean_text = re.sub(r'\b(ET|AET|EXTRA\s*TIME|2H|2ND\s*HALF|1H|1ST\s*HALF)\b', '', text).strip()
    
    # =========================================================================
    # Pattern D: Broadcast stoppage display with running sub-clock
    # "45:00 +2 00:43" → base=45, sub-clock elapsed=0 min → 45
    # "90:00 +4 02:17" → base=90, sub-clock elapsed=2 min → 92
    # The main clock freezes at the period boundary. The sub-clock counts
    # how much of the allocated stoppage has elapsed.
    # Actual minute = base_minutes + sub_clock_minutes
    # =========================================================================
    broadcast_stoppage = re.match(
        r'(\d{1,3}):(\d{2})\s*\+\s*\d+\s+(\d{1,2}):(\d{2})',
        clean_text
    )
    if broadcast_stoppage:
        base_min = int(broadcast_stoppage.group(1))
        elapsed_added_min = int(broadcast_stoppage.group(3))
        return base_min + elapsed_added_min
    
    # =========================================================================
    # Pattern E: Base time with stoppage (no space, colon in base)
    # "90:00+3:15" → base=90, added=3 → 93
    # "45:00+2" → base=45, added=2 → 47
    # =========================================================================
    base_stoppage = re.match(
        r'(\d{1,3}):(\d{2})\+(\d+)(?::(\d{2}))?$',
        clean_text
    )
    if base_stoppage:
        base_min = int(base_stoppage.group(1))
        added_min = int(base_stoppage.group(3))
        return base_min + added_min
    
    # =========================================================================
    # Pattern C: Compact stoppage "45+2:30" or "90+3" or "45+2"
    # Base is stated directly (no colon before the +)
    # =========================================================================
    stoppage_match = re.match(r'(\d+)\s*\+\s*(\d+)', clean_text)
    if stoppage_match:
        base_min = int(stoppage_match.group(1))
        added_min = int(stoppage_match.group(2))
        return base_min + added_min
    
    # =========================================================================
    # Patterns A & B: Standard MM:SS or just MM (with optional period offset)
    # =========================================================================
    time_match = re.search(r'(\d{1,3}):(\d{2})', clean_text)
    if not time_match:
        # Try just minutes
        just_minutes = re.match(r'^(\d{1,3})$', clean_text.strip())
        if just_minutes:
            minutes = int(just_minutes.group(1))
        else:
            return None  # Can't parse
    else:
        minutes = int(time_match.group(1))
    
    # SMART OFFSET LOGIC: Determine if time is relative or absolute
    if has_et:
        if minutes >= 90:
            return minutes  # Already absolute (e.g., "ET 102:53")
        elif minutes <= 30:
            return 90 + minutes  # Relative to ET start (e.g., "ET 04:04")
        else:
            return minutes  # Ambiguous (31-89), assume absolute
    
    elif has_2h:
        if minutes >= 45:
            return minutes  # Already absolute (e.g., "2H 67:00")
        else:
            return 45 + minutes  # Relative to 2H start (e.g., "2H 5:00")
    
    elif has_1h:
        return minutes  # First half, no offset needed
    
    else:
        # No indicator — assume absolute (most common)
        return minutes


# =============================================================================
# Structured Clock Field Parsers
# =============================================================================

def parse_clock_field(raw_clock: str | None) -> int | None:
    """
    Parse the CLOCK field from structured prompt → absolute minute.
    
    Unlike parse_broadcast_clock(), this only handles the primary timer
    since ADDED and STOPPAGE_CLOCK are separate fields. Patterns D & E
    are eliminated — only A, B, C remain.
    
    Args:
        raw_clock: Raw CLOCK field text (e.g., "34:12", "2H 15:30", "45+2")
    
    Returns:
        Absolute match minute, or None if unparseable
    """
    if not raw_clock or raw_clock.upper() in ("NONE", "HT", "FT", "HALF TIME", "FULL TIME"):
        return None
    text = raw_clock.upper().strip()

    has_et = bool(re.search(r'\b(ET|AET|EXTRA\s*TIME)\b', text))
    has_2h = bool(re.search(r'\b(2H|2ND\s*HALF)\b', text))
    has_1h = bool(re.search(r'\b(1H|1ST\s*HALF)\b', text))
    clean = re.sub(r'\b(ET|AET|EXTRA\s*TIME|2H|2ND\s*HALF|1H|1ST\s*HALF)\b', '', text).strip()

    # Pattern C: compact stoppage "45+2", "90+3"
    sm = re.match(r'(\d+)\s*\+\s*(\d+)', clean)
    if sm:
        return int(sm.group(1)) + int(sm.group(2))

    # Patterns A & B: MM:SS or just MM
    tm = re.search(r'(\d{1,3}):(\d{2})', clean)
    if not tm:
        jm = re.match(r'^(\d{1,3})$', clean.strip())
        if jm:
            minutes = int(jm.group(1))
        else:
            return None
    else:
        minutes = int(tm.group(1))

    # Smart offset logic
    if has_et:
        return (90 + minutes) if minutes <= 30 else minutes
    elif has_2h:
        return (45 + minutes) if minutes < 45 else minutes
    elif has_1h:
        return minutes
    return minutes


def parse_added_field(raw_added: str | None) -> int | None:
    """
    Parse the ADDED field: "+4" → 4, "+6" → 6.
    
    Args:
        raw_added: Raw ADDED field text (e.g., "+4", "+6")
    
    Returns:
        Added time minutes, or None if not visible
    """
    if not raw_added or raw_added.upper().strip() in ("NONE", "N/A"):
        return None
    m = re.search(r'\+\s*(\d+)', raw_added)
    return int(m.group(1)) if m else None


def parse_stoppage_clock_field(raw_stoppage: str | None) -> int | None:
    """
    Parse the STOPPAGE_CLOCK field: "03:57" → 3.
    
    This is the sub-timer that counts elapsed time within stoppage.
    We only need the minutes component.
    
    Args:
        raw_stoppage: Raw STOPPAGE_CLOCK field text (e.g., "03:57", "02:17")
    
    Returns:
        Elapsed stoppage minutes, or None if not visible
    """
    if not raw_stoppage or raw_stoppage.upper().strip() in ("NONE", "N/A"):
        return None
    m = re.match(r'(\d{1,2}):(\d{2})', raw_stoppage.strip())
    return int(m.group(1)) if m else None


def compute_absolute_minute(clock_min: int | None, stoppage_min: int | None) -> int | None:
    """
    Compute absolute minute from structured clock fields.
    
    If stoppage_clock is present, the main clock is frozen at a period
    boundary and the real time is clock_min + stoppage_min.
    
    Args:
        clock_min: Parsed CLOCK field (absolute minute)
        stoppage_min: Parsed STOPPAGE_CLOCK field (elapsed stoppage minutes)
    
    Returns:
        Absolute match minute, or None if no clock data
    """
    if clock_min is None:
        return None
    if stoppage_min is not None:
        return clock_min + stoppage_min
    return clock_min


def validate_timestamp(
    frames: list[dict],
    api_elapsed: int,
    api_extra: int | None
) -> tuple[bool, int | None, str]:
    """
    Check if EITHER extracted clock matches the API-reported event time.
    
    Accepts structured frame data (dicts with raw_clock, raw_stoppage_clock)
    from the new 5-field prompt. Uses per-field parsers for clean extraction.
    
    Two-phase comparison:
    1. Direct match: parsed clock minute within ±1 of expected
    2. Stoppage-time OCR correction: if the vision model dropped the leading
       digit (e.g., read "92:36" as "02:36"), try rebasing with api_elapsed
    
    Args:
        frames: List of dicts with raw_clock, raw_added, raw_stoppage_clock keys
                (one per frame, typically 25% and 75%)
        api_elapsed: API elapsed minute (e.g., 90 for stoppage, 30 for regular)
        api_extra: API extra/stoppage minutes (e.g., 3), or None
    
    Returns:
        Tuple of:
        - clock_verified: True if at least one clock matches
        - extracted_minute: The matching minute (or best guess), None if no clock visible
        - timestamp_status: "verified", "unverified", or "rejected"
    """
    # Guard: if no API time available (e.g., in-flight replay with default=0),
    # we can't validate — default to unverified
    if not api_elapsed:
        return (False, None, "unverified")
    
    # API reports the minute AFTER the goal happened
    # Expected broadcast minute = elapsed + extra - 1
    expected = api_elapsed + (api_extra or 0) - 1
    
    # Parse all frames using structured field parsers
    computed_minutes = []
    for frame in frames:
        clock_min = parse_clock_field(frame.get("raw_clock"))
        stoppage_min = parse_stoppage_clock_field(frame.get("raw_stoppage_clock"))
        absolute = compute_absolute_minute(clock_min, stoppage_min)
        if absolute is not None:
            computed_minutes.append(absolute)
    
    if not computed_minutes:
        # No clock visible in any frame → unverified
        return (False, None, "unverified")
    
    # Phase 1: Direct match — computed minute within ±1 of expected
    for minute in computed_minutes:
        if abs(minute - expected) <= 1:
            return (True, minute, "verified")
    
    # Phase 2: Stoppage-time OCR correction
    #
    # During stoppage, api_elapsed is the period boundary (45, 90, 105, 120)
    # and the broadcast clock shows (api_elapsed + N):SS where N is minutes
    # into stoppage. Vision models sometimes drop the leading digit:
    #   Screen: "92:36 +8"  →  AI reads: "02:36 +8"  →  parser returns: 2
    #
    # Since api_elapsed IS the dropped base, we can recover:
    #   corrected = api_elapsed + parsed = 90 + 2 = 92
    #
    # The ±1 comparison against expected naturally constrains which parsed
    # values can match: only minutes ≈ api_extra (±1) will pass.
    if api_extra is not None:
        for minute in computed_minutes:
            corrected = api_elapsed + minute
            if abs(corrected - expected) <= 1:
                return (True, corrected, "verified")
    
    # Clock visible but wrong time → rejected
    # Return the closest one for logging purposes
    closest = min(computed_minutes, key=lambda m: abs(m - expected))
    return (False, closest, "rejected")


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
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
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
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
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
    
    # Extract tweet_id from URL
    # Handles: https://x.com/user/status/123, https://x.com/i/status/123, https://twitter.com/...
    tweet_id_match = re.search(r'/status/(\d+)', video_url)
    if not tweet_id_match:
        log.error(activity.logger, "download", "tweet_id_extraction_failed",
                  "Could not extract tweet_id from URL",
                  event_id=event_id, video_idx=video_index, url=video_url)
        raise RuntimeError(f"Could not extract tweet_id from URL: {video_url}")
    
    tweet_id = tweet_id_match.group(1)
    
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
                log.warning(activity.logger, "download", "syndication_api_error",
                            "Syndication API returned error",
                            video_idx=video_index, status_code=response.status_code)
                raise RuntimeError(f"Syndication API returned {response.status_code}")
            
            tweet_data = response.json()
    except httpx.TimeoutException:
        log.warning(activity.logger, "download", "syndication_timeout",
                    "Syndication API timeout", video_idx=video_index)
        raise RuntimeError("Syndication API timeout")
    except Exception as e:
        log.warning(activity.logger, "download", "syndication_error",
                    "Syndication API error",
                    video_idx=video_index, error=str(e)[:100])
        raise RuntimeError(f"Syndication API error: {str(e)[:100]}")
    
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
                    # Log more detail for auth failures
                    if response.status_code == 403:
                        has_auth = "auth_token" in twitter_cookies
                        log.warning(activity.logger, "download", "cdn_auth_failed",
                                    "CDN returned 403",
                                    video_idx=video_index, is_amplify=is_amplify, has_auth=has_auth)
                    else:
                        log.warning(activity.logger, "download", "cdn_error",
                                    "CDN returned error",
                                    video_idx=video_index, status_code=response.status_code)
                    raise RuntimeError(f"CDN returned {response.status_code}")
                
                with open(output_path, 'wb') as f:
                    async for chunk in response.aiter_bytes(chunk_size=8192):
                        f.write(chunk)
    except httpx.TimeoutException:
        log.warning(activity.logger, "download", "cdn_timeout",
                    "CDN download timeout", video_idx=video_index)
        raise RuntimeError("CDN download timeout")
    except Exception as e:
        log.warning(activity.logger, "download", "cdn_download_error",
                    "CDN download error",
                    video_idx=video_index, error=str(e)[:100])
        raise RuntimeError(f"CDN download error: {str(e)[:100]}")
    
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


@activity.defn
async def validate_video_is_soccer(
    file_path: str,
    event_id: str,
    event_minute: int = 0,
    event_extra: int | None = None,
) -> Dict[str, Any]:
    """
    AI validation to check if video is soccer AND not a phone-TV recording.
    Also extracts broadcast clock data for timestamp verification.
    
    Uses vision LLM to analyze frames and determine:
    1. Is this a soccer/football video?
    2. Is this someone filming a TV screen with their phone?
    3. What does the broadcast clock show? (structured: CLOCK/ADDED/STOPPAGE_CLOCK)
    
    Timestamp verification is folded into is_valid:
    - is_valid = is_soccer AND not screen_recording AND timestamp not rejected
    
    Args:
        file_path: Local path to video file
        event_id: Event ID for logging
        event_minute: API elapsed minute (e.g., 45, 90) — default 0 means no validation
        event_extra: API extra/stoppage minutes (e.g., 3), or None
    
    Returns:
        Dict with:
        - is_valid: True if soccer AND not screen AND timestamp not rejected
        - is_soccer, is_screen_recording, confidence, reason, checks_performed
        - clock_verified: True if extracted clock matches API time ±1
        - extracted_minute: Best extracted clock minute (None if no clock)
        - timestamp_status: "verified" / "unverified" / "rejected"
        - extracted_clocks: Raw structured data per frame
    
    Raises:
        FileNotFoundError: If video file doesn't exist
    """
    # Verify file exists before validation
    if not os.path.exists(file_path):
        log.error(activity.logger, "download", "validate_file_missing",
                  "Video file not found for validation",
                  event_id=event_id, file_path=file_path)
        raise FileNotFoundError(f"Video file not found for validation: {file_path}")
    
    # Get video duration for frame extraction
    duration = 0.0
    try:
        import subprocess
        probe_cmd = [
            "ffprobe", "-v", "error", "-show_entries",
            "format=duration", "-of", "csv=p=0", file_path
        ]
        result = subprocess.run(probe_cmd, capture_output=True, text=True, timeout=10)
        if result.returncode == 0 and result.stdout.strip():
            duration = float(result.stdout.strip())
    except Exception as e:
        log.warning(activity.logger, MODULE, "get_duration_failed",
                    "Failed to get duration", error=str(e))
        duration = 10.0  # Assume 10s if can't read
    
    if duration < 1.0:
        # Too short to analyze
        return {
            "is_valid": True,
            "confidence": 0.5,
            "reason": "Video too short to analyze",
            "detected_features": [],
            "checks_performed": 0,
        }
    
    # Combined vision prompt for soccer detection, phone-TV rejection, AND structured clock extraction
    prompt = """/no_think
Analyze this image and answer FIVE questions:

1. SOCCER: Is this from a soccer/football match broadcast or highlight?
   Answer YES if you see ANY of these:
   - Players on a soccer pitch/field (wide shots)
   - Match action, goals, tackles, or plays
   - Close-up replays or slow-motion of match moments
   - Player celebrations or reactions during a match
   - Goal close-ups showing the ball in the net
   - VAR review footage
   - Match broadcast graphics/scoreboards with match footage
   - Stadium/fan recording of live match action
   
   Answer NO if you see:
   - People talking in a studio, podcast, or TV show (even with soccer graphics)
   - News anchors or pundits at a desk discussing soccer
   - Press conferences or interviews (not match footage)
   - Just logos/graphics with NO match footage at all
   - Advertisements or promotional content
   - Other sports (basketball, tennis, etc.)

2. SCREEN: Is this someone filming a TV/monitor screen with their phone/camera?
   This is ONLY for detecting amateur phone recordings of TV screens, NOT professional broadcasts.
   
   Answer YES ONLY if you see MULTIPLE of these PHYSICAL artifacts:
   - Moiré patterns (rainbow/wavy interference lines on the display)
   - Visible TV bezel/frame edges around the picture (thick black borders with TV hardware visible)
   - Screen glare or reflections from room lighting
   - Tilted/angled perspective showing someone filmed a screen from the side
   - Visible room, furniture, walls, or surroundings around the TV
   - Severe pixelation from screen refresh rate mismatch
   
   IMPORTANT - These are NOT screen recordings, answer NO:
   - Professional broadcast overlays, scoreboards, or graphics
   - VAR boxes, replay borders, or picture-in-picture
   - Watermarks or channel logos
   - Black letterbox bars (16:9 vs 4:3 aspect ratio difference)
   - Clean professional broadcast footage with any overlays
   
   When in doubt, answer NO. Only reject obvious phone-filming-TV scenarios.

3. CLOCK: What does the PRIMARY match timer show?
   Report the main clock display (e.g., "34:12", "90:00", "2H 15:30"). Copy exactly.
   If no clock visible, answer NONE.

4. ADDED: Is there an ADDITIONAL TIME indicator (like "+3", "+5") shown near the clock?
   If yes, report exactly what you see (e.g., "+4", "+6").
   If none visible, answer NONE.

5. STOPPAGE_CLOCK: Is there a SEPARATE smaller clock counting time within added/stoppage time?
   Some broadcasts freeze the main clock (e.g., at 90:00) and show a small running
   sub-timer (e.g., "03:57") for the elapsed stoppage time.
   If you see this separate sub-timer, report it (e.g., "03:57").
   If there is no separate sub-timer, answer NONE.

Answer format (exactly):
SOCCER: YES or NO
SCREEN: YES or NO
CLOCK: <exact text from main timer> or NONE
ADDED: <exact indicator like +4> or NONE
STOPPAGE_CLOCK: <exact sub-timer text> or NONE"""

    log.info(activity.logger, "download", "validate_started",
             "Starting AI vision validation",
             event_id=event_id, duration=round(duration, 1),
             file=os.path.basename(file_path))
    
    # =========================================================================
    # Smart 2-3 check strategy:
    # 1. Check 25% and 75% first
    # 2. If both agree → use that result (2 checks)
    # 3. If they disagree → check 50% as tiebreaker (3 checks)
    # =========================================================================
    
    def parse_response(resp) -> dict:
        """Parse vision model response into structured dict.
        
        Returns dict with: is_soccer, is_screen, raw_clock, raw_added, raw_stoppage_clock
        """
        result = {"is_soccer": False, "is_screen": False, "raw_clock": None, "raw_added": None, "raw_stoppage_clock": None}
        
        if not resp:
            return result
        
        # Handle llama.cpp OpenAI format
        if "choices" in resp:
            content = resp.get("choices", [{}])[0].get("message", {}).get("content", "")
            content_upper = content.upper()
            
            # Check for skip indicator (vision not available)
            if "SKIP" in content_upper:
                result["is_soccer"] = True  # Fail open when vision is unavailable
                return result
            
            # Parse SOCCER answer
            is_soccer = "SOCCER:YES" in content_upper or "SOCCER: YES" in content_upper
            if "SOCCER:" not in content_upper:
                is_soccer = "YES" in content_upper and "SOCCER" in content_upper
            result["is_soccer"] = is_soccer
            
            # Parse SCREEN answer
            is_screen = "SCREEN:YES" in content_upper or "SCREEN: YES" in content_upper
            if "SCREEN:" not in content_upper:
                is_screen = "MOIRE" in content_upper or "BEZEL" in content_upper or "TV FRAME" in content_upper
            result["is_screen"] = is_screen
            
            # Parse CLOCK field
            clock_match = re.search(r'CLOCK:\s*(.+?)(?:\n|$)', content, re.IGNORECASE)
            if clock_match:
                clock_value = clock_match.group(1).strip()
                if clock_value.upper() not in ("NONE", "N/A", "NOT VISIBLE", "NO CLOCK"):
                    result["raw_clock"] = clock_value
            
            # Parse ADDED field
            added_match = re.search(r'ADDED:\s*(.+?)(?:\n|$)', content, re.IGNORECASE)
            if added_match:
                added_value = added_match.group(1).strip()
                if added_value.upper() not in ("NONE", "N/A", "NOT VISIBLE"):
                    result["raw_added"] = added_value
            
            # Parse STOPPAGE_CLOCK field
            stoppage_match = re.search(r'STOPPAGE_CLOCK:\s*(.+?)(?:\n|$)', content, re.IGNORECASE)
            if stoppage_match:
                stoppage_value = stoppage_match.group(1).strip()
                if stoppage_value.upper() not in ("NONE", "N/A", "NOT VISIBLE"):
                    result["raw_stoppage_clock"] = stoppage_value
            
            return result
        
        # Legacy Ollama format
        text = resp.get("response", "").strip()
        text_upper = text.upper()
        result["is_soccer"] = "SOCCER:YES" in text_upper or ("YES" in text_upper and "SOCCER" in text_upper)
        result["is_screen"] = "SCREEN:YES" in text_upper
        
        # Parse CLOCK from legacy format
        clock_match = re.search(r'CLOCK:\s*(.+?)(?:\n|$)', text, re.IGNORECASE)
        if clock_match:
            clock_value = clock_match.group(1).strip()
            if clock_value.upper() not in ("NONE", "N/A", "NOT VISIBLE", "NO CLOCK"):
                result["raw_clock"] = clock_value
        
        return result
    
    # Extract frames at 25% and 75%
    t_25 = duration * 0.25
    t_75 = duration * 0.75
    
    frame_25 = _extract_frame_for_vision(file_path, t_25)
    frame_75 = _extract_frame_for_vision(file_path, t_75)
    
    if not frame_25 and not frame_75:
        log.error(activity.logger, "download", "frame_extraction_failed",
                  "Failed to extract ANY frames",
                  event_id=event_id, file=file_path)
        raise RuntimeError("Failed to extract frames for validation")
    
    # =========================================================================
    # Smart 2-3 check strategy with heartbeats:
    # 1. Check 25% and 75% first (heartbeat after each)
    # 2. If both agree → use that result (2 checks)
    # 3. If they disagree → check 50% as tiebreaker (3 checks, heartbeat after)
    # =========================================================================
    checks_performed = 0
    frame_25_data, frame_75_data, frame_50_data = None, None, None
    soccer_25, screen_25 = None, None
    soccer_75, screen_75 = None, None
    
    if frame_25:
        activity.heartbeat(f"AI vision check 1/2 (25% frame)...")
        response_25 = await _call_vision_model(frame_25, prompt)
        checks_performed += 1
        parsed_25 = parse_response(response_25)
        soccer_25 = parsed_25["is_soccer"]
        screen_25 = parsed_25["is_screen"]
        frame_25_data = parsed_25
        log.info(activity.logger, "download", "vision_check",
                 "Vision check completed",
                 check_pct=25, is_soccer=soccer_25, is_screen=screen_25,
                 clock=parsed_25["raw_clock"], added=parsed_25["raw_added"],
                 stoppage=parsed_25["raw_stoppage_clock"])
    
    # Heartbeat before second check
    activity.heartbeat(f"AI vision check 2/2 (75% frame)...")
    
    # Check 75%
    if frame_75:
        response_75 = await _call_vision_model(frame_75, prompt)
        checks_performed += 1
        parsed_75 = parse_response(response_75)
        soccer_75 = parsed_75["is_soccer"]
        screen_75 = parsed_75["is_screen"]
        frame_75_data = parsed_75
        log.info(activity.logger, "download", "vision_check",
                 "Vision check completed",
                 check_pct=75, is_soccer=soccer_75, is_screen=screen_75,
                 clock=parsed_75["raw_clock"], added=parsed_75["raw_added"],
                 stoppage=parsed_75["raw_stoppage_clock"])
    
    # =========================================================================
    # Determine BOTH soccer and screen results using same tiebreaker logic
    # For screen detection: we need BOTH frames to agree, or 2/3 majority
    # This prevents false positives from broadcast overlays/graphics
    # =========================================================================
    
    # Check if we need tiebreaker (disagreement on either soccer OR screen)
    soccer_disagree = soccer_25 is not None and soccer_75 is not None and soccer_25 != soccer_75
    screen_disagree = screen_25 is not None and screen_75 is not None and screen_25 != screen_75
    need_tiebreaker = soccer_disagree or screen_disagree
    
    soccer_50, screen_50 = None, None
    
    if need_tiebreaker:
        log.info(activity.logger, "download", "vision_tiebreaker",
                 "Disagreement detected, checking 50% tiebreaker")
        activity.heartbeat("AI vision tiebreaker (50% frame)...")
        
        t_50 = duration * 0.50
        frame_50 = _extract_frame_for_vision(file_path, t_50)
        
        if frame_50:
            response_50 = await _call_vision_model(frame_50, prompt)
            checks_performed += 1
            parsed_50 = parse_response(response_50)
            soccer_50 = parsed_50["is_soccer"]
            screen_50 = parsed_50["is_screen"]
            frame_50_data = parsed_50  # Extracted but NOT used for timestamp validation
            log.info(activity.logger, "download", "vision_check",
                     "Vision tiebreaker completed",
                     check_pct=50, is_soccer=soccer_50, is_screen=screen_50,
                     clock=parsed_50["raw_clock"])
    
    # Determine soccer result
    if soccer_25 is None and soccer_75 is not None:
        is_soccer = soccer_75
        confidence = 0.7
        soccer_reason = "Single frame check (25% failed)"
    elif soccer_75 is None and soccer_25 is not None:
        is_soccer = soccer_25
        confidence = 0.7
        soccer_reason = "Single frame check (75% failed)"
    elif soccer_25 == soccer_75:
        is_soccer = soccer_25
        confidence = 0.95 if is_soccer else 0.90
        soccer_reason = f"Both checks agree: {'soccer' if is_soccer else 'not soccer'}"
    else:
        # Disagreement - use tiebreaker (2/3 majority)
        if soccer_50 is not None:
            yes_votes = sum([soccer_25 or False, soccer_50, soccer_75 or False])
            is_soccer = yes_votes >= 2
            confidence = 0.85
            soccer_reason = f"Tiebreaker decided: {yes_votes}/3 votes for soccer"
        else:
            is_soccer = soccer_25 if soccer_25 is not None else False
            confidence = 0.6
            soccer_reason = "Tiebreaker failed, using first check"
    
    # Determine screen recording result (same tiebreaker logic - need agreement or 2/3 majority)
    if screen_25 is None and screen_75 is not None:
        is_screen_recording = screen_75
    elif screen_75 is None and screen_25 is not None:
        is_screen_recording = screen_25
    elif screen_25 == screen_75:
        # Both agree
        is_screen_recording = screen_25 or False
    else:
        # Disagreement - use tiebreaker (2/3 majority to REJECT)
        if screen_50 is not None:
            screen_votes = sum([screen_25 or False, screen_50, screen_75 or False])
            is_screen_recording = screen_votes >= 2  # Need 2/3 to reject
        else:
            # Tiebreaker failed, default to NOT rejecting (benefit of the doubt)
            is_screen_recording = False
    
    # Final validation: must be soccer AND not a screen recording
    is_valid = is_soccer and not is_screen_recording
    
    # Collect structured clock data from validation frames (25% and 75% only)
    # 50% frame is extracted for tiebreaker but NOT used for timestamp validation
    validation_frames = []
    if frame_25_data:
        validation_frames.append(frame_25_data)
    if frame_75_data:
        validation_frames.append(frame_75_data)
    
    # Timestamp validation — only if we have API minute data
    clock_verified = False
    extracted_minute = None
    timestamp_status = "unverified"
    
    if event_minute and validation_frames:
        clock_verified, extracted_minute, timestamp_status = validate_timestamp(
            validation_frames, event_minute, event_extra
        )
        
        # Fold timestamp rejection into is_valid
        if timestamp_status == "rejected":
            is_valid = False
    
    # Build extracted_clocks for logging/debugging
    extracted_clocks = []
    for frame_data in [frame_25_data, frame_75_data]:
        if frame_data:
            extracted_clocks.append({
                "clock": frame_data.get("raw_clock"),
                "added": frame_data.get("raw_added"),
                "stoppage_clock": frame_data.get("raw_stoppage_clock"),
            })
    if frame_50_data:
        extracted_clocks.append({
            "clock": frame_50_data.get("raw_clock"),
            "added": frame_50_data.get("raw_added"),
            "stoppage_clock": frame_50_data.get("raw_stoppage_clock"),
        })
    
    if is_screen_recording:
        reason = "Rejected: phone recording of TV/screen detected"
        log.info(activity.logger, "download", "validate_rejected",
                 "Video rejected: phone-TV recording",
                 rejection_type="screen_recording",
                 event_id=event_id, checks=checks_performed, is_screen=True,
                 clocks=extracted_clocks)
    elif not is_soccer:
        reason = soccer_reason
        log.info(activity.logger, "download", "validate_rejected",
                 "Video rejected: not soccer",
                 rejection_type="not_soccer",
                 event_id=event_id, checks=checks_performed, reason=soccer_reason,
                 clocks=extracted_clocks)
    elif timestamp_status == "rejected":
        reason = f"Rejected: wrong game minute (expected ~{event_minute + (event_extra or 0) - 1}, got {extracted_minute})"
        log.info(activity.logger, "download", "validate_rejected",
                 "Video rejected: wrong timestamp",
                 rejection_type="wrong_timestamp",
                 event_id=event_id, checks=checks_performed,
                 expected=event_minute + (event_extra or 0) - 1,
                 extracted=extracted_minute, clocks=extracted_clocks)
    else:
        reason = soccer_reason
        log.info(activity.logger, "download", "validate_passed",
                 "Video passed validation",
                 event_id=event_id, checks=checks_performed,
                 confidence=round(confidence, 2),
                 clock_verified=clock_verified,
                 extracted_minute=extracted_minute,
                 timestamp_status=timestamp_status,
                 clocks=extracted_clocks)
    
    return {
        "is_valid": is_valid,
        "confidence": confidence,
        "reason": reason,
        "is_soccer": is_soccer,
        "is_screen_recording": is_screen_recording,
        "detected_features": ["soccer_field"] if is_soccer else [],
        "checks_performed": checks_performed,
        # Structured clock extraction data
        "clock_verified": clock_verified,
        "extracted_minute": extracted_minute,
        "timestamp_status": timestamp_status,
        "extracted_clocks": extracted_clocks,
    }


@activity.defn
async def generate_video_hash(file_path: str, duration: float) -> Dict[str, Any]:
    """
    Generate perceptual hash for a video file.
    
    Called AFTER AI validation to avoid wasting compute on non-soccer videos.
    Dense sampling at 0.25s intervals with histogram equalization.
    
    Uses heartbeats every ~10 frames to signal progress to Temporal.
    This allows long videos to process without timeout as long as
    they're making progress (not hung).
    
    Args:
        file_path: Path to video file
        duration: Video duration in seconds
        
    Returns:
        Dict with perceptual_hash string
    """
    if not os.path.exists(file_path):
        log.error(activity.logger, "download", "hash_file_missing",
                  "File not found for hash generation", file_path=file_path)
        return {"perceptual_hash": "", "error": "file_not_found"}
    
    log.info(activity.logger, "download", "hash_started",
             "Starting hash generation",
             file=os.path.basename(file_path), duration=round(duration, 1))
    
    # Pass heartbeat function to signal progress during long hash generation
    perceptual_hash = _generate_perceptual_hash(file_path, duration, heartbeat_fn=activity.heartbeat)
    
    # Log hash info and validate result
    if perceptual_hash.startswith("dense:"):
        parts = perceptual_hash.split(":", 2)
        if len(parts) >= 3 and parts[2]:
            frame_count = len(parts[2].split(","))
            if frame_count >= 3:
                log.info(activity.logger, "download", "hash_generated",
                         "Hash generated successfully",
                         frame_count=frame_count, interval=0.25)
            else:
                log.warning(activity.logger, "download", "hash_low_frames",
                            "Low frame count in hash",
                            frame_count=frame_count, duration=duration,
                            file=os.path.basename(file_path))
        else:
            log.error(activity.logger, "download", "hash_no_frames",
                      "No frames extracted for hash",
                      file=os.path.basename(file_path), duration=duration)
            return {"perceptual_hash": "", "error": "no_frames_extracted"}
    else:
        log.error(activity.logger, "download", "hash_invalid_format",
                  "Invalid hash format",
                  file=os.path.basename(file_path), hash_prefix=perceptual_hash[:50])
        return {"perceptual_hash": "", "error": "invalid_hash_format"}
    
    return {"perceptual_hash": perceptual_hash}


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


def _extract_frame_for_vision(file_path: str, timestamp: float) -> Optional[str]:
    """
    Extract a single frame from video and return as base64.
    
    Args:
        file_path: Path to video file
        timestamp: Timestamp in seconds to extract frame
        
    Returns:
        Base64-encoded PNG image or None if extraction fails
    """
    import subprocess
    import base64
    
    try:
        cmd = [
            "ffmpeg",
            "-ss", str(timestamp),
            "-i", file_path,
            "-vframes", "1",
            "-f", "image2pipe",
            "-vcodec", "png",
            "-"
        ]
        result = subprocess.run(
            cmd,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=10
        )
        
        if result.returncode != 0 or not result.stdout:
            log.warning(activity.logger, "download", "frame_extraction_failed",
                        "Frame extraction failed",
                        timestamp=timestamp, returncode=result.returncode)
            return None
        
        return base64.b64encode(result.stdout).decode('utf-8')
    except subprocess.TimeoutExpired:
        log.warning(activity.logger, "download", "frame_extraction_timeout",
                    "Frame extraction timed out", timestamp=timestamp)
        return None
    except Exception as e:
        log.warning(activity.logger, "download", "frame_extraction_error",
                    "Frame extraction error", timestamp=timestamp, error=str(e))
        return None


async def _call_vision_model(image_base64: str, prompt: str) -> Optional[Dict[str, Any]]:
    """
    Call vision LLM with an image using llama.cpp OpenAI-compatible API.
    
    Uses a semaphore to limit concurrent requests per worker (llama.cpp is
    single-slot for inference). Retries with backoff on transient failures.
    
    Args:
        image_base64: Base64-encoded image
        prompt: Question to ask about the image
        
    Returns:
        Dict with response content or None if failed
    """
    import httpx
    
    llama_url = LLAMA_CHAT_URL
    max_retries = 3
    
    payload = {
        "messages": [
            {
                "role": "user",
                "content": [
                    {"type": "text", "text": prompt},
                    {
                        "type": "image_url",
                        "image_url": {
                            "url": f"data:image/jpeg;base64,{image_base64}"
                        }
                    }
                ]
            }
        ],
        "max_tokens": 100,
        "temperature": 0.1
    }
    
    for attempt in range(1, max_retries + 1):
        try:
            async with _LLM_SEMAPHORE:
                log.debug(activity.logger, "download", "vision_call",
                          "Calling vision model", url=llama_url, attempt=attempt)
                
                async with httpx.AsyncClient(timeout=60.0) as client:
                    response = await client.post(
                        f"{llama_url}/v1/chat/completions",
                        json=payload,
                    )
                    
                    if response.status_code == 200:
                        return response.json()
                    else:
                        log.warning(activity.logger, "download", "vision_http_error",
                                    "Vision model returned error",
                                    status_code=response.status_code, attempt=attempt)
                        if attempt < max_retries:
                            await asyncio.sleep(2 * attempt)
                            continue
                        return None
        except httpx.ConnectError as e:
            log.error(activity.logger, "download", "vision_connect_failed",
                      "Cannot connect to LLM",
                      url=llama_url, error=str(e), error_type="ConnectError")
            raise
        except (httpx.TimeoutException, httpx.ReadError) as e:
            error_type = type(e).__name__
            if attempt < max_retries:
                wait = 3 * attempt
                log.warning(activity.logger, "download", "vision_retry",
                            f"Vision model {error_type}, retrying in {wait}s",
                            attempt=attempt, max_retries=max_retries, error_type=error_type)
                await asyncio.sleep(wait)
                continue
            else:
                log.warning(activity.logger, "download", "vision_timeout",
                            f"Vision model {error_type} after {max_retries} attempts",
                            error=str(e), error_type=error_type)
                raise
        except Exception as e:
            log.error(activity.logger, "download", "vision_error",
                      "Vision model error",
                      error=str(e), error_type=type(e).__name__)
            raise
    
    return None


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


def _generate_perceptual_hash(file_path: str, duration: float, heartbeat_fn=None) -> str:
    """
    Generate perceptual hash using dense sampling for offset-tolerant matching.
    
    Uses difference hash (dHash) algorithm with histogram equalization:
    1. Sample frames every 0.25s throughout the video
    2. Apply histogram equalization to normalize contrast/brightness
    3. Resize to 9x8, convert to grayscale
    4. Compare adjacent pixels to create 64-bit hash per frame
    
    Dense sampling (every 0.25s) solves the offset problem:
    - Different clips of same goal often start at different times
    - All-pairs comparison finds matching frames regardless of offset
    - Histogram equalization handles color grading differences
    
    Format: "dense:<interval>:<ts1>=<hash1>,<ts2>=<hash2>,..."
    Example: "dense:0.25:0.25=a3f8b2e1c9d4f5a2,0.50=1e3b8c7d9f2a4b1c,..."
    
    Args:
        file_path: Path to video file
        duration: Video duration in seconds
        heartbeat_fn: Optional function to call periodically to signal activity is alive
        
    Returns:
        Dense hash string with all frame hashes
    """
    import subprocess
    from PIL import Image, ImageOps
    import io
    
    interval = 0.25  # Sample every 0.25 seconds
    total_frames = int((duration - 0.3) / interval)
    
    # CRITICAL: Send heartbeat IMMEDIATELY before any processing starts
    # This prevents timeout when multiple videos compete for resources
    if heartbeat_fn:
        heartbeat_fn(f"Starting hash generation for {total_frames} frames")
    
    def extract_frame_hash_normalized(timestamp: float) -> str:
        """Extract frame at timestamp, normalize, and compute dHash"""
        try:
            cmd = [
                "ffmpeg",
                "-ss", str(timestamp),
                "-i", file_path,
                "-vframes", "1",
                "-f", "image2pipe",
                "-vcodec", "png",
                "-"
            ]
            result = subprocess.run(
                cmd,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
                timeout=10
            )
            
            if result.returncode != 0 or not result.stdout:
                return ""
            
            # Load image, convert to grayscale, apply histogram equalization
            img = Image.open(io.BytesIO(result.stdout))
            img = img.convert('L')  # Grayscale
            img = ImageOps.equalize(img)  # Normalize contrast/brightness
            img = img.resize((9, 8), Image.Resampling.LANCZOS)
            
            pixels = list(img.getdata())
            
            # Compute difference hash
            hash_bits = []
            for row in range(8):
                for col in range(8):
                    left = pixels[row * 9 + col]
                    right = pixels[row * 9 + col + 1]
                    hash_bits.append('1' if left < right else '0')
            
            hash_int = int(''.join(hash_bits), 2)
            return format(hash_int, '016x')
            
        except Exception as e:
            log.warning(activity.logger, "download", "hash_frame_failed",
                        "Failed to extract frame for hash",
                        timestamp=timestamp, error=str(e))
            return ""
    
    # Extract hashes at 0.25s intervals
    hashes = []
    t = interval
    frame_count = 0
    
    while t < duration - 0.3:  # Stop before last 0.3s to avoid end-of-file issues
        # Send heartbeat BEFORE each ffmpeg call to prevent timeout during resource contention
        # When multiple videos are processed in parallel, ffmpeg calls can be very slow
        if heartbeat_fn:
            heartbeat_fn(f"Processing frame {frame_count + 1}/{total_frames}")
        
        frame_hash = extract_frame_hash_normalized(t)
        if frame_hash:
            hashes.append(f"{t:.2f}={frame_hash}")
        t += interval
        frame_count += 1
    
    if not hashes:
        # Fallback: try at least one frame at 1s
        if heartbeat_fn:
            heartbeat_fn("Fallback: trying single frame at 1s")
        frame_hash = extract_frame_hash_normalized(1.0)
        if frame_hash:
            hashes.append(f"1.00={frame_hash}")
    
    # Format: "dense:<interval>:<hash_list>"
    hash_list = ",".join(hashes)
    return f"dense:{interval}:{hash_list}"


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


# Re-export queue_videos_for_upload from upload module so DownloadWorkflow can use it
from src.activities.upload import queue_videos_for_upload
