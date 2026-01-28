"""Download activities - Video download, validation, and hash generation for DownloadWorkflow"""
from temporalio import activity
from typing import Dict, List, Any, Optional
import os
import hashlib
import asyncio
import json

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
        activity.logger.warning(f"⚠️ [COOKIES] No cookie file at {TWITTER_COOKIE_BACKUP_PATH}")
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
        activity.logger.info(f"🍪 [COOKIES] Loaded {len(cookies)} cookies, auth: {found}")
        
        _twitter_cookies_cache = cookies
        return cookies
        
    except Exception as e:
        activity.logger.warning(f"⚠️ [COOKIES] Failed to load: {e}")
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
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    try:
        # Add workflow ID to array (idempotent via $addToSet)
        success = store.add_download_workflow(fixture_id, event_id, workflow_id)
        
        # Get current count after registration
        count = store.get_download_workflow_count(fixture_id, event_id)
        
        activity.logger.info(
            f"📊 [DOWNLOAD] register_download_workflow | event={event_id} | "
            f"workflow={workflow_id} | count={count} | success={success}"
        )
        
        return {
            "success": success,
            "count": count
        }
    except Exception as e:
        activity.logger.error(
            f"❌ [DOWNLOAD] register_download_workflow failed | event={event_id} | error={e}"
        )
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
        
        activity.logger.info(
            f"📊 [DOWNLOAD] check_and_mark_download_complete | event={event_id} | "
            f"count={result['count']}/{required_count} | "
            f"was_complete={result['was_already_complete']} | "
            f"marked={result['marked_complete']}"
        )
        
        return result
    except Exception as e:
        activity.logger.error(
            f"❌ [DOWNLOAD] check_and_mark_download_complete failed | event={event_id} | error={e}"
        )
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
    
    activity.logger.info(
        f"📥 [DOWNLOAD] Starting download | event={event_id} | video_idx={video_index} | "
        f"url={video_url[:60]}..."
    )
    
    # Extract tweet_id from URL
    # Handles: https://x.com/user/status/123, https://x.com/i/status/123, https://twitter.com/...
    tweet_id_match = re.search(r'/status/(\d+)', video_url)
    if not tweet_id_match:
        msg = f"[DOWNLOAD] Could not extract tweet_id from URL: {video_url}"
        activity.logger.error(f"❌ {msg}")
        raise RuntimeError(msg)
    
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
                msg = f"Syndication API returned {response.status_code}"
                activity.logger.warning(f"⚠️ [DOWNLOAD] {msg} | video_idx={video_index}")
                raise RuntimeError(msg)
            
            tweet_data = response.json()
    except httpx.TimeoutException:
        msg = "Syndication API timeout"
        activity.logger.warning(f"⚠️ [DOWNLOAD] {msg} | video_idx={video_index}")
        raise RuntimeError(msg)
    except Exception as e:
        msg = f"Syndication API error: {str(e)[:100]}"
        activity.logger.warning(f"⚠️ [DOWNLOAD] {msg} | video_idx={video_index}")
        raise RuntimeError(msg)
    
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
                activity.logger.warning(
                    f"📏 [DOWNLOAD] Pre-filtered: low resolution | video={video_index} | "
                    f"short_edge={short_edge}px | min={MIN_SHORT_EDGE}px | res={video_width}x{video_height}"
                )
                return {"status": "filtered", "reason": "pre_filter_resolution", "source_url": source_tweet_url}
        
        # Aspect ratio filter (reject portrait/square videos)
        if ASPECT_RATIO_FILTER_ENABLED and video_height > 0:
            aspect_ratio = video_width / video_height
            if aspect_ratio < MIN_ASPECT_RATIO:
                activity.logger.warning(
                    f"📐 [DOWNLOAD] Pre-filtered: portrait/square | video={video_index} | "
                    f"ratio={aspect_ratio:.2f} | min={MIN_ASPECT_RATIO} | res={video_width}x{video_height}"
                )
                return {"status": "filtered", "reason": "pre_filter_aspect_ratio", "source_url": source_tweet_url}
    
    # Path 2 (fallback): video.variants - often missing bitrates
    if not video_variants and "video" in tweet_data and "variants" in tweet_data["video"]:
        video_variants = tweet_data["video"]["variants"]
    
    if not video_variants:
        msg = f"No video variants found in syndication response"
        activity.logger.warning(f"⚠️ [DOWNLOAD] {msg} | video_idx={video_index}")
        raise RuntimeError(msg)
    
    # Filter to mp4 only and sort by bitrate (highest first)
    mp4_variants = [
        v for v in video_variants 
        if v.get("type") == "video/mp4" or v.get("content_type") == "video/mp4"
    ]
    
    if not mp4_variants:
        msg = f"No MP4 variants found"
        activity.logger.warning(f"⚠️ [DOWNLOAD] {msg} | video_idx={video_index}")
        raise RuntimeError(msg)
    
    # Sort by bitrate descending, pick best quality
    mp4_variants.sort(key=lambda v: v.get("bitrate", 0), reverse=True)
    best_variant = mp4_variants[0]
    cdn_url = best_variant.get("src") or best_variant.get("url")
    bitrate = best_variant.get("bitrate", 0)
    
    # Check if this is amplify_video (promoted content that may need auth)
    is_amplify = "amplify_video" in cdn_url if cdn_url else False
    
    activity.logger.info(
        f"🎬 [DOWNLOAD] Found {len(mp4_variants)} variants, using best: {bitrate/1000:.0f}kbps | "
        f"video_idx={video_index} | amplify={is_amplify}"
    )
    
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
                    msg = f"CDN returned {response.status_code}"
                    # Log more detail for auth failures
                    if response.status_code == 403:
                        has_auth = "auth_token" in twitter_cookies
                        activity.logger.warning(
                            f"⚠️ [DOWNLOAD] {msg} | video_idx={video_index} | amplify={is_amplify} | "
                            f"has_auth_cookie={has_auth}"
                        )
                    else:
                        activity.logger.warning(f"⚠️ [DOWNLOAD] {msg} | video_idx={video_index}")
                    raise RuntimeError(msg)
                
                with open(output_path, 'wb') as f:
                    async for chunk in response.aiter_bytes(chunk_size=8192):
                        f.write(chunk)
    except httpx.TimeoutException:
        msg = "CDN download timeout"
        activity.logger.warning(f"⚠️ [DOWNLOAD] {msg} | video_idx={video_index}")
        raise RuntimeError(msg)
    except Exception as e:
        msg = f"CDN download error: {str(e)[:100]}"
        activity.logger.warning(f"⚠️ [DOWNLOAD] {msg} | video_idx={video_index}")
        raise RuntimeError(msg)
    
    # Verify file exists and has content
    if not os.path.exists(output_path):
        msg = f"[DOWNLOAD] No file after download | video_idx={video_index}"
        activity.logger.error(f"❌ {msg}")
        raise RuntimeError(msg)
    
    file_size = os.path.getsize(output_path)
    if file_size < 1000:  # Less than 1KB is likely an error
        msg = f"[DOWNLOAD] File too small ({file_size} bytes) | video_idx={video_index}"
        activity.logger.error(f"❌ {msg}")
        os.remove(output_path)
        raise RuntimeError(msg)
    
    activity.logger.info(
        f"✅ [DOWNLOAD] Downloaded {file_size / 1024 / 1024:.2f}MB | video_idx={video_index}"
    )
    
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
async def validate_video_is_soccer(file_path: str, event_id: str) -> Dict[str, Any]:
    """
    AI validation to check if video is soccer AND not a phone-TV recording.
    
    Uses vision LLM to analyze frames and determine:
    1. Is this a soccer/football video?
    2. Is this someone filming a TV screen with their phone?
    
    We WANT:
    - Direct broadcast recordings
    - Stadium/in-person phone recordings (fans filming live)
    
    We REJECT:
    - Non-soccer content (ads, interviews, other sports)
    - Phone recordings of TV screens (moiré patterns, bezels, glare)
    
    Detection criteria for SOCCER:
    - Soccer/football field (green pitch, white lines)
    - Players in match uniforms
    - Goal posts/nets
    - Match action/gameplay
    
    Detection criteria for SCREEN RECORDING (reject):
    - TV bezel/frame visible around edges
    - Moiré patterns (wavy interference from filming display)
    - Screen glare or reflections
    - Room/furniture visible around TV
    - Curved or tilted screen perspective
    
    Args:
        file_path: Local path to video file
        event_id: Event ID for logging
    
    Returns:
        Dict with:
        - is_valid: True if soccer AND not screen recording
        - is_soccer: True if soccer content detected
        - is_screen_recording: True if phone-TV recording detected
        - confidence, reason, checks_performed
    
    Raises:
        FileNotFoundError: If video file doesn't exist
    """
    # Verify file exists before validation
    if not os.path.exists(file_path):
        msg = f"❌ FATAL: Video file not found for validation: {file_path}"
        activity.logger.error(msg)
        raise FileNotFoundError(msg)
    
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
        activity.logger.warning(f"⚠️ Failed to get duration: {e}")
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
    
    # Combined vision prompt for soccer detection AND phone-TV rejection
    prompt = """/no_think
Analyze this image and answer TWO questions:

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

Answer format (exactly):
SOCCER: YES or NO
SCREEN: YES or NO"""

    activity.logger.info(
        f"🔍 [VALIDATE] Starting AI vision validation | event={event_id} | "
        f"duration={duration:.1f}s | file={os.path.basename(file_path)}"
    )
    
    # =========================================================================
    # Smart 2-3 check strategy:
    # 1. Check 25% and 75% first
    # 2. If both agree → use that result (2 checks)
    # 3. If they disagree → check 50% as tiebreaker (3 checks)
    # =========================================================================
    
    def parse_response(resp) -> tuple[bool, bool]:
        """Parse vision model response, returns (is_soccer, is_screen_recording)"""
        if not resp:
            return (False, False)
        
        # Handle llama.cpp OpenAI format
        if "choices" in resp:
            content = resp.get("choices", [{}])[0].get("message", {}).get("content", "").upper()
            # Check for skip indicator (vision not available)
            if "SKIP" in content:
                return (True, False)  # Fail open when vision is unavailable
            
            # Parse the two answers
            is_soccer = "SOCCER:YES" in content or "SOCCER: YES" in content
            is_screen = "SCREEN:YES" in content or "SCREEN: YES" in content
            
            # Fallback: if format not matched, look for keywords
            if "SOCCER:" not in content:
                is_soccer = "YES" in content and "SOCCER" in content
            if "SCREEN:" not in content:
                is_screen = "MOIRE" in content or "BEZEL" in content or "TV FRAME" in content
            
            return (is_soccer, is_screen)
        
        # Legacy Ollama format
        text = resp.get("response", "").strip().upper()
        is_soccer = "SOCCER:YES" in text or ("YES" in text and "SOCCER" in text)
        is_screen = "SCREEN:YES" in text
        return (is_soccer, is_screen)
    
    # Extract frames at 25% and 75%
    t_25 = duration * 0.25
    t_75 = duration * 0.75
    
    frame_25 = _extract_frame_for_vision(file_path, t_25)
    frame_75 = _extract_frame_for_vision(file_path, t_75)
    
    if not frame_25 and not frame_75:
        msg = f"❌ [VALIDATE] Failed to extract ANY frames | event={event_id} | file={file_path}"
        activity.logger.error(msg)
        raise RuntimeError(msg)  # Let retry policy handle - don't fail-open
    
    # =========================================================================
    # Smart 2-3 check strategy with heartbeats:
    # 1. Check 25% and 75% first (heartbeat after each)
    # 2. If both agree → use that result (2 checks)
    # 3. If they disagree → check 50% as tiebreaker (3 checks, heartbeat after)
    # =========================================================================
    checks_performed = 0
    soccer_25, screen_25 = None, None
    soccer_75, screen_75 = None, None
    
    if frame_25:
        activity.heartbeat(f"AI vision check 1/2 (25% frame)...")
        response_25 = await _call_vision_model(frame_25, prompt)
        checks_performed += 1
        soccer_25, screen_25 = parse_response(response_25)
        activity.logger.info(
            f"   📸 [VALIDATE] 25% check | SOCCER={'YES' if soccer_25 else 'NO'} | "
            f"SCREEN={'YES' if screen_25 else 'NO'}"
        )
    
    # Heartbeat before second check
    activity.heartbeat(f"AI vision check 2/2 (75% frame)...")
    
    # Check 75%
    if frame_75:
        response_75 = await _call_vision_model(frame_75, prompt)
        checks_performed += 1
        soccer_75, screen_75 = parse_response(response_75)
        activity.logger.info(
            f"   📸 [VALIDATE] 75% check | SOCCER={'YES' if soccer_75 else 'NO'} | "
            f"SCREEN={'YES' if screen_75 else 'NO'}"
        )
    
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
        activity.logger.info(f"   ⚖️ [VALIDATE] Disagreement detected, checking 50% tiebreaker...")
        activity.heartbeat("AI vision tiebreaker (50% frame)...")
        
        t_50 = duration * 0.50
        frame_50 = _extract_frame_for_vision(file_path, t_50)
        
        if frame_50:
            response_50 = await _call_vision_model(frame_50, prompt)
            checks_performed += 1
            soccer_50, screen_50 = parse_response(response_50)
            activity.logger.info(
                f"   📸 [VALIDATE] 50% tiebreaker | SOCCER={'YES' if soccer_50 else 'NO'} | "
                f"SCREEN={'YES' if screen_50 else 'NO'}"
            )
    
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
        activity.logger.info(
            f"   ✓ [VALIDATE] Both frames agree | is_soccer={'YES' if is_soccer else 'NO'}"
        )
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
        activity.logger.info(
            f"   ✓ [VALIDATE] Both frames agree | is_screen={'YES' if is_screen_recording else 'NO'}"
        )
    else:
        # Disagreement - use tiebreaker (2/3 majority to REJECT)
        if screen_50 is not None:
            screen_votes = sum([screen_25 or False, screen_50, screen_75 or False])
            is_screen_recording = screen_votes >= 2  # Need 2/3 to reject
            activity.logger.info(
                f"   ⚖️ [VALIDATE] Screen tiebreaker: {screen_votes}/3 votes for screen recording"
            )
        else:
            # Tiebreaker failed, default to NOT rejecting (benefit of the doubt)
            is_screen_recording = False
            activity.logger.info(
                f"   ⚖️ [VALIDATE] Screen disagreement, no tiebreaker - defaulting to NOT screen"
            )
    
    # Final validation: must be soccer AND not a screen recording
    is_valid = is_soccer and not is_screen_recording
    
    if is_screen_recording:
        reason = "Rejected: phone recording of TV/screen detected"
        activity.logger.warning(
            f"📺 [VALIDATE] REJECTED phone-TV recording | event={event_id} | "
            f"checks={checks_performed}"
        )
    elif not is_soccer:
        reason = soccer_reason
        activity.logger.warning(
            f"❌ [VALIDATE] REJECTED not soccer | event={event_id} | "
            f"checks={checks_performed} | reason={soccer_reason}"
        )
    else:
        reason = soccer_reason
        activity.logger.info(
            f"✅ [VALIDATE] PASSED validation | event={event_id} | "
            f"checks={checks_performed} | confidence={confidence:.0%}"
        )
    
    return {
        "is_valid": is_valid,
        "confidence": confidence,
        "reason": reason,
        "is_soccer": is_soccer,
        "is_screen_recording": is_screen_recording,
        "detected_features": ["soccer_field"] if is_soccer else [],
        "checks_performed": checks_performed,
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
        activity.logger.error(
            f"❌ [HASH] File not found | path={file_path}"
        )
        return {"perceptual_hash": "", "error": "file_not_found"}
    
    activity.logger.info(
        f"🔐 [HASH] Starting hash generation | file={os.path.basename(file_path)} | "
        f"duration={duration:.1f}s"
    )
    
    # Pass heartbeat function to signal progress during long hash generation
    perceptual_hash = _generate_perceptual_hash(file_path, duration, heartbeat_fn=activity.heartbeat)
    
    # Log hash info and validate result
    if perceptual_hash.startswith("dense:"):
        parts = perceptual_hash.split(":", 2)
        if len(parts) >= 3 and parts[2]:
            frame_count = len(parts[2].split(","))
            if frame_count >= 3:
                activity.logger.info(
                    f"✅ [HASH] Generated hash | frames={frame_count} | interval=0.25s"
                )
            else:
                activity.logger.warning(
                    f"⚠️ [HASH] Low frame count | frames={frame_count} | "
                    f"duration={duration}s | file={os.path.basename(file_path)}"
                )
        else:
            activity.logger.error(
                f"❌ [HASH] No frames extracted | file={os.path.basename(file_path)} | "
                f"duration={duration}s"
            )
            return {"perceptual_hash": "", "error": "no_frames_extracted"}
    else:
        activity.logger.error(
            f"❌ [HASH] Invalid format | file={os.path.basename(file_path)} | "
            f"hash_prefix={perceptual_hash[:50]}"
        )
        return {"perceptual_hash": "", "error": "invalid_hash_format"}
    
    return {"perceptual_hash": perceptual_hash}


@activity.defn
async def increment_twitter_count(
    fixture_id: int,
    event_id: str,
    total_attempts: int = 10
) -> dict:
    """
    DEPRECATED: Use check_and_mark_download_complete instead.
    
    This activity uses counter-based tracking which has race condition issues.
    The new workflow-ID-based tracking (register_download_workflow + check_and_mark_download_complete)
    is idempotent and handles retries correctly.
    
    Keeping this for backward compatibility during transition.
    
    Increment _twitter_count and check if we should mark _download_complete.
    
    Called by:
    - DownloadWorkflow when a download completes
    - TwitterWorkflow when a search finds no videos (no download triggered)
    
    This solves the race condition where fixture could move to fixtures_completed
    while downloads are still running. By having downloads set _download_complete,
    we ensure completion only happens after all work is done.
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID
        total_attempts: Total attempts expected (default 10)
    
    Returns:
        Dict with success, new_count, marked_complete
    """
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    activity.logger.info(
        f"📊 [DOWNLOAD] DEPRECATED: Incrementing twitter count | event={event_id}"
    )
    
    result = store.increment_twitter_count_and_check_complete(
        fixture_id, event_id, total_attempts
    )
    
    if result["success"]:
        if result["marked_complete"]:
            activity.logger.info(
                f"✅ [DOWNLOAD] Twitter count={result['new_count']}, marked _download_complete=true | "
                f"event={event_id}"
            )
        else:
            activity.logger.info(
                f"📊 [DOWNLOAD] Twitter count={result['new_count']}/{total_attempts} | event={event_id}"
            )
    else:
        activity.logger.error(
            f"❌ [DOWNLOAD] Failed to increment twitter count | event={event_id}"
        )
    
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
        activity.logger.warning(
            f"⏱️ [DOWNLOAD] Filtered: too short | video={display_idx} | "
            f"duration={duration:.1f}s | min={MIN_VIDEO_DURATION}s"
        )
        os.remove(output_path)
        return None
    
    if duration > MAX_VIDEO_DURATION:
        activity.logger.warning(
            f"⏱️ [DOWNLOAD] Filtered: too long | video={display_idx} | "
            f"duration={duration:.1f}s | max={MAX_VIDEO_DURATION}s"
        )
        os.remove(output_path)
        return None
    
    # Short edge filter: reject low-resolution videos
    # 720p has short edge 720, but letterboxed HD has ~686px
    if SHORT_EDGE_FILTER_ENABLED and width and height:
        short_edge = min(width, height)
        if short_edge < MIN_SHORT_EDGE:
            activity.logger.warning(
                f"📏 [DOWNLOAD] Filtered: low resolution | video={display_idx} | "
                f"short_edge={short_edge}px | min={MIN_SHORT_EDGE}px | res={width}x{height}"
            )
            os.remove(output_path)
            return None
    
    # Aspect ratio filter: reject portrait/square videos (< 4:3)
    # Disabled by default to allow stadium phone recordings
    # Phone-TV recordings are filtered by AI vision instead
    if ASPECT_RATIO_FILTER_ENABLED and width and height and height > 0:
        aspect_ratio = width / height
        if aspect_ratio < MIN_ASPECT_RATIO:
            activity.logger.warning(
                f"📐 [DOWNLOAD] Filtered: aspect ratio | video={display_idx} | "
                f"ratio={aspect_ratio:.2f} | min={MIN_ASPECT_RATIO}"
            )
            os.remove(output_path)
            return None
    
    # Calculate MD5 hash and size (fast)
    file_hash = _calculate_md5(output_path)
    file_size = os.path.getsize(output_path)
    
    quality_info = f"{width}x{height}" if width and height else "unknown res"
    if bitrate:
        quality_info += f"@{bitrate:.0f}kbps"
    
    activity.logger.info(
        f"✅ [DOWNLOAD] Video ready for validation | video={display_idx} | "
        f"size={file_size / 1024 / 1024:.2f}MB | duration={duration:.1f}s | quality={quality_info}"
    )
    
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
            activity.logger.warning(
                f"⚠️ Frame extraction failed at {timestamp}s: "
                f"returncode={result.returncode}, stderr={result.stderr.decode()[:200] if result.stderr else 'none'}"
            )
            return None
        
        return base64.b64encode(result.stdout).decode('utf-8')
    except subprocess.TimeoutExpired:
        activity.logger.warning(f"⚠️ Frame extraction timed out at {timestamp}s")
        return None
    except Exception as e:
        activity.logger.warning(f"⚠️ Frame extraction error at {timestamp}s: {e}")
        return None


async def _call_vision_model(image_base64: str, prompt: str) -> Optional[Dict[str, Any]]:
    """
    Call vision LLM with an image using llama.cpp OpenAI-compatible API.
    
    Args:
        image_base64: Base64-encoded image
        prompt: Question to ask about the image
        
    Returns:
        Dict with response content or None if failed
    """
    import httpx
    
    llama_url = LLAMA_CHAT_URL
    
    activity.logger.debug(f"🔍 Calling vision model at {llama_url}")
    
    try:
        async with httpx.AsyncClient(timeout=30.0) as client:
            # OpenAI-compatible multimodal format for llama.cpp
            response = await client.post(
                f"{llama_url}/v1/chat/completions",
                json={
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
            )
            
            if response.status_code == 200:
                return response.json()
            else:
                activity.logger.warning(
                    f"⚠️ Vision model returned status {response.status_code}: {response.text[:200]}"
                )
                return None
    except httpx.ConnectError as e:
        activity.logger.error(f"❌ Cannot connect to LLM at {llama_url}: {e}")
        raise  # Propagate connection errors - should retry
    except httpx.TimeoutException as e:
        activity.logger.warning(f"⚠️ Vision model request timed out: {e}")
        raise  # Propagate timeouts - should retry
    except Exception as e:
        activity.logger.error(f"❌ Vision model error: {type(e).__name__}: {e}")
        raise  # Propagate all errors - let retry policy handle


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
        activity.logger.warning(f"⚠️ Failed to get video metadata for {file_path}: {e}")
    
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
            activity.logger.warning(f"⚠️ Failed to extract frame at {timestamp}s: {e}")
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
        activity.logger.info(f"🧹 [DOWNLOAD] Cleaned up temp dir | path={temp_dir}")
        return True
    
    return False


# Re-export queue_videos_for_upload from upload module so DownloadWorkflow can use it
from src.activities.upload import queue_videos_for_upload
