#!/usr/bin/env python3
"""
Test script for clock extraction from video frames.

Usage:
    python scripts/test_clock_extraction.py <video_path> [--api-minute MINUTE] [--api-extra EXTRA]

Examples:
    # Just test clock OCR (no validation)
    python scripts/test_clock_extraction.py /path/to/video.mp4
    
    # Test with API time validation (e.g., 95th minute goal)
    python scripts/test_clock_extraction.py /path/to/video.mp4 --api-minute 95
    
    # Test extra time goal (95' = 90 + 5)
    python scripts/test_clock_extraction.py /path/to/video.mp4 --api-minute 90 --api-extra 5

This script:
1. Extracts frames at 25%, 50%, 75% of video
2. Sends each frame to the vision LLM with the new CLOCK prompt
3. Parses the raw clock text response
4. Uses parse_broadcast_clock() to convert to absolute minute
5. Optionally validates against API time using validate_timestamp()
"""
import argparse
import asyncio
import base64
import subprocess
import sys
import os
import tempfile
import httpx

# Add project root to path
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from src.activities.download import parse_broadcast_clock, validate_timestamp
from src.utils.config import LLAMA_CHAT_URL


def extract_frame(video_path: str, timestamp_seconds: float) -> bytes | None:
    """Extract a frame from video at given timestamp, return as JPEG bytes."""
    with tempfile.NamedTemporaryFile(suffix=".jpg", delete=False) as tmp:
        tmp_path = tmp.name
    
    try:
        cmd = [
            "ffmpeg", "-y",
            "-ss", str(timestamp_seconds),
            "-i", video_path,
            "-vframes", "1",
            "-q:v", "2",  # High quality JPEG
            tmp_path
        ]
        result = subprocess.run(cmd, capture_output=True, timeout=30)
        
        if result.returncode != 0 or not os.path.exists(tmp_path):
            return None
        
        with open(tmp_path, "rb") as f:
            return f.read()
    finally:
        if os.path.exists(tmp_path):
            os.unlink(tmp_path)


def get_video_duration(video_path: str) -> float:
    """Get video duration in seconds."""
    cmd = [
        "ffprobe", "-v", "error",
        "-show_entries", "format=duration",
        "-of", "csv=p=0",
        video_path
    ]
    result = subprocess.run(cmd, capture_output=True, text=True, timeout=10)
    if result.returncode == 0 and result.stdout.strip():
        return float(result.stdout.strip())
    return 0.0


async def call_vision_model(frame_bytes: bytes, prompt: str) -> dict | None:
    """Call the vision model with a frame."""
    base64_image = base64.b64encode(frame_bytes).decode('utf-8')
    
    payload = {
        "model": "qwen3-vl",
        "messages": [
            {
                "role": "user",
                "content": [
                    {
                        "type": "image_url",
                        "image_url": {
                            "url": f"data:image/jpeg;base64,{base64_image}"
                        }
                    },
                    {
                        "type": "text",
                        "text": prompt
                    }
                ]
            }
        ],
        "max_tokens": 200,
        "temperature": 0.1
    }
    
    try:
        async with httpx.AsyncClient(timeout=60.0) as client:
            response = await client.post(LLAMA_CHAT_URL, json=payload)
            response.raise_for_status()
            return response.json()
    except Exception as e:
        print(f"  Error calling vision model: {e}")
        return None


def parse_response(resp: dict | None) -> tuple[bool, bool, str | None]:
    """Parse vision model response, returns (is_soccer, is_screen, raw_clock)"""
    import re
    
    if not resp:
        return (False, False, None)
    
    if "choices" not in resp:
        return (False, False, None)
    
    content = resp.get("choices", [{}])[0].get("message", {}).get("content", "")
    content_upper = content.upper()
    
    # Parse SOCCER
    is_soccer = "SOCCER:YES" in content_upper or "SOCCER: YES" in content_upper
    if "SOCCER:" not in content_upper:
        is_soccer = "YES" in content_upper and "SOCCER" in content_upper
    
    # Parse SCREEN
    is_screen = "SCREEN:YES" in content_upper or "SCREEN: YES" in content_upper
    if "SCREEN:" not in content_upper:
        is_screen = "MOIRE" in content_upper or "BEZEL" in content_upper
    
    # Parse CLOCK
    raw_clock = None
    clock_match = re.search(r'CLOCK:\s*(.+?)(?:\n|$)', content, re.IGNORECASE)
    if clock_match:
        clock_value = clock_match.group(1).strip()
        if clock_value.upper() not in ("NONE", "N/A", "NOT VISIBLE", "NO CLOCK"):
            raw_clock = clock_value
    
    return (is_soccer, is_screen, raw_clock)


# The vision prompt (same as in download.py)
VISION_PROMPT = """/no_think
Analyze this image and answer THREE questions:

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

3. CLOCK: What does the game clock/match timer show on the broadcast scoreboard?
   
   Report EXACTLY what you see on screen, including any period/half indicators.
   
   Examples of what you might see:
   - "34:12" (just the time)
   - "2H 15:30" (half indicator + time)  
   - "ET 04:04" (extra time indicator + time)
   - "45+2:30" (stoppage time format)
   - "90:00+3" (alternative stoppage format)
   - "105:47" (extra time shown as absolute)
   
   Copy the clock display EXACTLY as shown, including any text like "ET", "2H", "1H", "AET", etc.
   If no game clock/timer is visible (e.g., close-up replay, celebration), answer NONE.

Answer format (exactly):
SOCCER: YES or NO
SCREEN: YES or NO
CLOCK: <exact text from screen> or NONE"""


async def main():
    parser = argparse.ArgumentParser(description="Test clock extraction from video frames")
    parser.add_argument("video_path", help="Path to video file")
    parser.add_argument("--api-minute", type=int, default=None,
                       help="API elapsed minute for validation (e.g., 95)")
    parser.add_argument("--api-extra", type=int, default=None,
                       help="API extra time for validation (e.g., 5 for 90+5)")
    args = parser.parse_args()
    
    if not os.path.exists(args.video_path):
        print(f"Error: Video file not found: {args.video_path}")
        sys.exit(1)
    
    print(f"\n{'='*60}")
    print(f"Testing clock extraction: {os.path.basename(args.video_path)}")
    print(f"{'='*60}")
    
    duration = get_video_duration(args.video_path)
    if duration <= 0:
        print(f"Error: Could not get video duration")
        sys.exit(1)
    
    print(f"Video duration: {duration:.1f}s")
    print(f"LLM endpoint: {LLAMA_CHAT_URL}")
    
    if args.api_minute is not None:
        expected = args.api_minute + (args.api_extra or 0) - 1
        print(f"API time: {args.api_minute}" + (f"+{args.api_extra}" if args.api_extra else ""))
        print(f"Expected clock minute: {expected} (±1)")
    
    print()
    
    # Test frames at 25%, 50%, 75%
    frame_positions = [
        (0.25, "25%"),
        (0.50, "50%"),
        (0.75, "75%"),
    ]
    
    extracted_clocks = []
    
    for position, label in frame_positions:
        timestamp = duration * position
        print(f"Frame at {label} ({timestamp:.1f}s):")
        
        frame_bytes = extract_frame(args.video_path, timestamp)
        if not frame_bytes:
            print(f"  ❌ Failed to extract frame")
            extracted_clocks.append(None)
            continue
        
        print(f"  📷 Extracted frame ({len(frame_bytes)} bytes)")
        print(f"  🤖 Calling vision model...")
        
        response = await call_vision_model(frame_bytes, VISION_PROMPT)
        is_soccer, is_screen, raw_clock = parse_response(response)
        
        print(f"  SOCCER: {'✅ YES' if is_soccer else '❌ NO'}")
        print(f"  SCREEN: {'⚠️ YES (phone recording)' if is_screen else '✅ NO'}")
        
        if raw_clock:
            parsed_minute = parse_broadcast_clock(raw_clock)
            print(f"  CLOCK: '{raw_clock}' → parsed as minute {parsed_minute}")
            extracted_clocks.append(raw_clock)
        else:
            print(f"  CLOCK: NONE (no clock visible)")
            extracted_clocks.append(None)
        
        # Show raw response for debugging
        if response and "choices" in response:
            content = response["choices"][0].get("message", {}).get("content", "")
            print(f"  Raw response:\n    {content.replace(chr(10), chr(10) + '    ')}")
        
        print()
    
    # Summary
    print(f"{'='*60}")
    print("SUMMARY")
    print(f"{'='*60}")
    
    print(f"Extracted clocks: {extracted_clocks}")
    
    # Parse all clocks
    parsed_minutes = []
    for raw in extracted_clocks:
        parsed = parse_broadcast_clock(raw)
        if parsed is not None:
            parsed_minutes.append(parsed)
    
    print(f"Parsed minutes: {parsed_minutes}")
    
    # Validate against API time if provided
    if args.api_minute is not None:
        clock_verified, extracted_minute, bucket = validate_timestamp(
            extracted_clocks,
            args.api_minute,
            args.api_extra
        )
        
        print()
        print(f"Timestamp validation:")
        print(f"  API time: {args.api_minute}" + (f"+{args.api_extra}" if args.api_extra else ""))
        print(f"  Expected: {args.api_minute + (args.api_extra or 0) - 1} (±1)")
        print(f"  Clock verified: {'✅ YES' if clock_verified else '❌ NO'}")
        print(f"  Extracted minute: {extracted_minute}")
        print(f"  Bucket: {bucket}")
        
        if bucket == "A":
            print(f"  → ✅ PASS: Clock matches API time")
        elif bucket == "B":
            print(f"  → ❌ REJECT: Clock visible but wrong minute")
        else:
            print(f"  → ⚠️ BUCKET C: No clock visible, cannot verify")
    
    print()


if __name__ == "__main__":
    asyncio.run(main())
