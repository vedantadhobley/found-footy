#!/usr/bin/env python3
"""
Batch test for structured three-field clock extraction (CLOCK / ADDED / STOPPAGE_CLOCK).

Queries MongoDB for a mix of stoppage time and regular time goals, downloads
videos from S3, extracts frames, and validates the structured prompt output.

Usage (run inside a prod worker container):
    python scripts/test_structured_extraction.py
"""
import asyncio
import base64
import json
import os
import re
import subprocess
import sys
import tempfile
import time

import httpx
from pymongo import MongoClient
import boto3

# ---------------------------------------------------------------------------
# Connection config (same env vars the workers use)
# ---------------------------------------------------------------------------
MONGODB_URI = os.getenv("MONGODB_URI", "mongodb://localhost:27017/")
S3_ENDPOINT = os.getenv("S3_ENDPOINT_URL", "http://localhost:9000")
S3_ACCESS = os.getenv("S3_ACCESS_KEY", "")
S3_SECRET = os.getenv("S3_SECRET_KEY", "")
S3_BUCKET = os.getenv("S3_BUCKET_NAME", "footy-videos")
LLAMA_URL = os.getenv("LLAMA_URL", "http://localhost:8080")

# ---------------------------------------------------------------------------
# Structured vision prompt — three distinct clock fields
# ---------------------------------------------------------------------------
STRUCTURED_PROMPT = """/no_think
Analyze this image and answer FIVE questions:

1. SOCCER: Is this from a soccer/football match broadcast or highlight?
   Answer YES if you see match action, goals, replays, celebrations, VAR, or broadcast graphics with match footage.
   Answer NO for studio shows, podcasts, press conferences, ads, or other sports.

2. SCREEN: Is this someone filming a TV/monitor screen with their phone/camera?
   Answer YES ONLY if you see MULTIPLE physical artifacts: moiré patterns, visible TV bezel, screen glare, tilted perspective, room surroundings.
   Answer NO for professional broadcasts (overlays, scoreboards, watermarks, letterbox bars are NOT screen recordings).

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


# ---------------------------------------------------------------------------
# Parsing helpers — structured field parsers
# ---------------------------------------------------------------------------
def parse_structured_response(resp: dict | None) -> dict:
    """Parse the 5-field vision model response into a structured dict."""
    result = {
        "is_soccer": False,
        "is_screen": False,
        "raw_clock": None,
        "raw_added": None,
        "raw_stoppage_clock": None,
        "raw_content": "",
    }
    if not resp or "choices" not in resp:
        return result

    content = resp["choices"][0].get("message", {}).get("content", "")
    result["raw_content"] = content
    cu = content.upper()

    # SOCCER
    result["is_soccer"] = "SOCCER:YES" in cu.replace(" ", "") or "SOCCER: YES" in cu

    # SCREEN
    result["is_screen"] = "SCREEN:YES" in cu.replace(" ", "") or "SCREEN: YES" in cu

    # CLOCK
    m = re.search(r'CLOCK:\s*(.+?)(?:\n|$)', content, re.IGNORECASE)
    if m:
        v = m.group(1).strip()
        if v.upper() not in ("NONE", "N/A", "NOT VISIBLE"):
            result["raw_clock"] = v

    # ADDED
    m = re.search(r'ADDED:\s*(.+?)(?:\n|$)', content, re.IGNORECASE)
    if m:
        v = m.group(1).strip()
        if v.upper() not in ("NONE", "N/A", "NOT VISIBLE"):
            result["raw_added"] = v

    # STOPPAGE_CLOCK
    m = re.search(r'STOPPAGE_CLOCK:\s*(.+?)(?:\n|$)', content, re.IGNORECASE)
    if m:
        v = m.group(1).strip()
        if v.upper() not in ("NONE", "N/A", "NOT VISIBLE"):
            result["raw_stoppage_clock"] = v

    return result


def parse_clock_field(raw_clock: str | None) -> int | None:
    """Parse CLOCK field → absolute minute."""
    if not raw_clock or raw_clock.upper() in ("NONE", "HT", "FT", "HALF TIME", "FULL TIME"):
        return None
    text = raw_clock.upper().strip()

    has_et = bool(re.search(r'\b(ET|AET|EXTRA\s*TIME)\b', text))
    has_2h = bool(re.search(r'\b(2H|2ND\s*HALF)\b', text))
    has_1h = bool(re.search(r'\b(1H|1ST\s*HALF)\b', text))
    clean = re.sub(r'\b(ET|AET|EXTRA\s*TIME|2H|2ND\s*HALF|1H|1ST\s*HALF)\b', '', text).strip()

    # Pattern C fallback
    sm = re.match(r'(\d+)\s*\+\s*(\d+)', clean)
    if sm:
        return int(sm.group(1)) + int(sm.group(2))

    tm = re.search(r'(\d{1,3}):(\d{2})', clean)
    if not tm:
        jm = re.match(r'^(\d{1,3})$', clean.strip())
        if jm:
            minutes = int(jm.group(1))
        else:
            return None
    else:
        minutes = int(tm.group(1))

    if has_et:
        return (90 + minutes) if minutes <= 30 else minutes
    elif has_2h:
        return (45 + minutes) if minutes < 45 else minutes
    elif has_1h:
        return minutes
    return minutes


def parse_added_field(raw_added: str | None) -> int | None:
    """Parse ADDED field: '+4' → 4."""
    if not raw_added or raw_added.upper().strip() == "NONE":
        return None
    m = re.search(r'\+\s*(\d+)', raw_added)
    return int(m.group(1)) if m else None


def parse_stoppage_clock_field(raw_stoppage: str | None) -> int | None:
    """Parse STOPPAGE_CLOCK field: '03:57' → 3."""
    if not raw_stoppage or raw_stoppage.upper().strip() == "NONE":
        return None
    m = re.match(r'(\d{1,2}):(\d{2})', raw_stoppage.strip())
    return int(m.group(1)) if m else None


def compute_absolute_minute(clock_min: int | None, stoppage_min: int | None) -> int | None:
    """Compute absolute minute from structured fields."""
    if clock_min is None:
        return None
    if stoppage_min is not None:
        return clock_min + stoppage_min
    return clock_min


def validate_timestamp(
    frames: list[dict],
    api_elapsed: int,
    api_extra: int | None,
) -> tuple[bool, int | None, str]:
    """Validate extracted timestamps against API time. Returns (verified, minute, bucket)."""
    if not api_elapsed:
        return (False, None, "C")

    expected = api_elapsed + (api_extra or 0) - 1
    computed = []
    for f in frames:
        cm = parse_clock_field(f.get("raw_clock"))
        sm = parse_stoppage_clock_field(f.get("raw_stoppage_clock"))
        am = compute_absolute_minute(cm, sm)
        if am is not None:
            computed.append(am)

    if not computed:
        return (False, None, "C")

    # Phase 1: direct match
    for m in computed:
        if abs(m - expected) <= 1:
            return (True, m, "A")

    # Phase 2: OCR correction
    if api_extra is not None:
        for m in computed:
            corrected = api_elapsed + m
            if abs(corrected - expected) <= 1:
                return (True, corrected, "A")

    closest = min(computed, key=lambda x: abs(x - expected))
    return (False, closest, "B")


# ---------------------------------------------------------------------------
# Video helpers
# ---------------------------------------------------------------------------
def download_from_s3(s3_key: str, local_path: str) -> bool:
    """Download a video from S3/MinIO."""
    try:
        s3 = boto3.client(
            "s3",
            endpoint_url=S3_ENDPOINT,
            aws_access_key_id=S3_ACCESS,
            aws_secret_access_key=S3_SECRET,
            region_name="us-east-1",
        )
        s3.download_file(S3_BUCKET, s3_key, local_path)
        return True
    except Exception as e:
        print(f"    S3 download error: {e}")
        return False


def get_video_duration(path: str) -> float:
    r = subprocess.run(
        ["ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path],
        capture_output=True, text=True, timeout=10,
    )
    return float(r.stdout.strip()) if r.returncode == 0 and r.stdout.strip() else 0.0


def extract_frame(path: str, ts: float) -> bytes | None:
    with tempfile.NamedTemporaryFile(suffix=".jpg", delete=False) as tmp:
        tmp_path = tmp.name
    try:
        subprocess.run(
            ["ffmpeg", "-y", "-ss", str(ts), "-i", path, "-vframes", "1", "-q:v", "2", tmp_path],
            capture_output=True, timeout=30,
        )
        if os.path.exists(tmp_path) and os.path.getsize(tmp_path) > 0:
            with open(tmp_path, "rb") as f:
                return f.read()
        return None
    finally:
        if os.path.exists(tmp_path):
            os.unlink(tmp_path)


async def call_vision(frame_bytes: bytes) -> dict | None:
    b64 = base64.b64encode(frame_bytes).decode()
    payload = {
        "model": "qwen3-vl",
        "messages": [
            {
                "role": "user",
                "content": [
                    {"type": "image_url", "image_url": {"url": f"data:image/jpeg;base64,{b64}"}},
                    {"type": "text", "text": STRUCTURED_PROMPT},
                ],
            }
        ],
        "max_tokens": 200,
        "temperature": 0.1,
    }
    try:
        async with httpx.AsyncClient(timeout=60.0) as client:
            r = await client.post(f"{LLAMA_URL}/v1/chat/completions", json=payload)
            r.raise_for_status()
            return r.json()
    except Exception as e:
        print(f"    Vision error: {e}")
        return None


# ---------------------------------------------------------------------------
# Find test goals from MongoDB
# ---------------------------------------------------------------------------
def find_test_goals(db) -> list[dict]:
    """
    Find a mix of stoppage-time and regular-time goals that have S3 videos.
    Returns list of {event_id, player, team, elapsed, extra, s3_key, fixture_id}.
    """
    goals = []

    # Search across all fixture collections
    for col_name in ["fixtures_completed", "fixtures_active"]:
        fixtures_col = db[col_name]
        for fixture in fixtures_col.find(
            {"events": {"$exists": True, "$ne": []}},
        ).sort("_id", -1).limit(100):
            fixture_id = fixture.get("fixture_id") or fixture.get("_id")
            events = fixture.get("events", [])

            for event in events:
                if event.get("type") != "Goal":
                    continue

                time_obj = event.get("time", {})
                elapsed = time_obj.get("elapsed")
                extra = time_obj.get("extra")
                if not elapsed:
                    continue

                s3_videos = event.get("_s3_videos", [])
                if not s3_videos:
                    continue

                # Pick first video with an S3 key
                s3_key = None
                for v in s3_videos:
                    k = v.get("_s3_key")
                    if k:
                        s3_key = k
                        break
                if not s3_key:
                    continue

                player = event.get("player", {}).get("name", "Unknown")
                team = event.get("team", {}).get("name", "Unknown")

                goals.append({
                    "event_id": event.get("_event_id", ""),
                    "player": player,
                    "team": team,
                    "elapsed": elapsed,
                    "extra": extra,
                    "s3_key": s3_key,
                    "fixture_id": fixture_id,
                })

    # Separate into stoppage and regular
    stoppage = [g for g in goals if g["extra"] is not None and g["extra"] > 0]
    regular = [g for g in goals if g["extra"] is None or g["extra"] == 0]

    # Pick up to 5 of each, preferring variety
    selected = []
    seen_elapsed = set()

    for g in stoppage:
        key = (g["elapsed"], g["extra"])
        if key not in seen_elapsed and len([s for s in selected if s["extra"]]) < 5:
            selected.append(g)
            seen_elapsed.add(key)

    seen_elapsed.clear()
    for g in regular:
        if g["elapsed"] not in seen_elapsed and len([s for s in selected if not s.get("extra")]) < 5:
            selected.append(g)
            seen_elapsed.add(g["elapsed"])

    return selected


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
async def main():
    print("=" * 80)
    print("STRUCTURED CLOCK EXTRACTION — BATCH TEST")
    print("=" * 80)
    print(f"LLM endpoint:  {LLAMA_URL}")
    print(f"MongoDB:       {MONGODB_URI[:40]}...")
    print(f"S3 endpoint:   {S3_ENDPOINT}")
    print()

    # Connect to MongoDB
    client = MongoClient(MONGODB_URI)
    db = client["found_footy"]

    # Find test goals
    print("Finding test goals...")
    goals = find_test_goals(db)
    stoppage_count = len([g for g in goals if g.get("extra")])
    regular_count = len(goals) - stoppage_count
    print(f"Found {len(goals)} goals ({stoppage_count} stoppage, {regular_count} regular)")
    print()

    if not goals:
        print("No goals found with S3 videos!")
        return

    results = []

    for i, goal in enumerate(goals):
        elapsed = goal["elapsed"]
        extra = goal["extra"]
        expected = elapsed + (extra or 0) - 1
        time_str = f"{elapsed}+{extra}" if extra else str(elapsed)

        print(f"{'─' * 80}")
        print(f"[{i+1}/{len(goals)}] {goal['player']} ({goal['team']}) — {time_str}'")
        print(f"  S3 key: {goal['s3_key']}")
        print(f"  Expected clock minute: {expected} (±1)")

        # Download video
        with tempfile.NamedTemporaryFile(suffix=".mp4", delete=False) as tmp:
            local_path = tmp.name

        try:
            if not download_from_s3(goal["s3_key"], local_path):
                print("  ❌ Failed to download from S3")
                results.append({"goal": goal, "status": "download_failed"})
                continue

            duration = get_video_duration(local_path)
            if duration <= 0:
                print("  ❌ Could not read video duration")
                results.append({"goal": goal, "status": "duration_error"})
                continue

            print(f"  Duration: {duration:.1f}s")

            # Extract and test at 25% and 75%
            frames_data = []
            for pct, label in [(0.25, "25%"), (0.75, "75%")]:
                ts = duration * pct
                print(f"\n  Frame at {label} ({ts:.1f}s):")

                frame_bytes = extract_frame(local_path, ts)
                if not frame_bytes:
                    print(f"    ❌ Failed to extract frame")
                    frames_data.append({"raw_clock": None, "raw_added": None, "raw_stoppage_clock": None})
                    continue

                resp = await call_vision(frame_bytes)
                parsed = parse_structured_response(resp)

                print(f"    SOCCER: {'YES' if parsed['is_soccer'] else 'NO'}")
                print(f"    SCREEN: {'YES' if parsed['is_screen'] else 'NO'}")
                print(f"    CLOCK:          {parsed['raw_clock'] or 'NONE'}")
                print(f"    ADDED:          {parsed['raw_added'] or 'NONE'}")
                print(f"    STOPPAGE_CLOCK: {parsed['raw_stoppage_clock'] or 'NONE'}")

                # Parse each field
                clock_min = parse_clock_field(parsed["raw_clock"])
                added_val = parse_added_field(parsed["raw_added"])
                stoppage_min = parse_stoppage_clock_field(parsed["raw_stoppage_clock"])
                absolute = compute_absolute_minute(clock_min, stoppage_min)

                print(f"    → clock_min={clock_min}, added={added_val}, stoppage_min={stoppage_min}")
                print(f"    → absolute_minute={absolute}")

                frames_data.append(parsed)

            # Validate
            verified, ext_minute, bucket = validate_timestamp(frames_data, elapsed, extra)

            status_icon = {"A": "✅", "B": "❌", "C": "⚠️"}[bucket]
            print(f"\n  VALIDATION: {status_icon} Bucket {bucket}")
            print(f"    API time: {time_str}' → expected={expected}")
            print(f"    Extracted minute: {ext_minute}")
            print(f"    Verified: {verified}")

            if bucket == "A":
                verdict = "PASS"
            elif bucket == "B":
                verdict = "FAIL (wrong minute)"
            else:
                verdict = "NO CLOCK"

            results.append({
                "goal": goal,
                "status": verdict,
                "bucket": bucket,
                "expected": expected,
                "extracted": ext_minute,
                "frames": [
                    {
                        "clock": f.get("raw_clock"),
                        "added": f.get("raw_added"),
                        "stoppage": f.get("raw_stoppage_clock"),
                    }
                    for f in frames_data
                ],
            })

        finally:
            if os.path.exists(local_path):
                os.unlink(local_path)

    # -----------------------------------------------------------------------
    # Summary
    # -----------------------------------------------------------------------
    print(f"\n{'=' * 80}")
    print("RESULTS SUMMARY")
    print(f"{'=' * 80}\n")

    # Table header
    print(f"{'Player':<22} {'Team':<18} {'API':<8} {'Exp':<5} {'Got':<5} {'Bkt':<4} {'25% CLOCK':<14} {'ADDED':<8} {'STOP':<10} {'75% CLOCK':<14} {'ADDED':<8} {'STOP':<10} {'Result':<12}")
    print("─" * 160)

    pass_count = 0
    fail_count = 0
    no_clock = 0

    for r in results:
        g = r["goal"]
        time_str = f"{g['elapsed']}+{g['extra']}" if g.get("extra") else str(g["elapsed"])
        frames = r.get("frames", [{}, {}])
        f25 = frames[0] if len(frames) > 0 else {}
        f75 = frames[1] if len(frames) > 1 else {}

        status = r["status"]
        if "PASS" in status:
            pass_count += 1
            icon = "✅ PASS"
        elif "FAIL" in status:
            fail_count += 1
            icon = "❌ FAIL"
        elif "NO CLOCK" in status:
            no_clock += 1
            icon = "⚠️  NO CLK"
        else:
            icon = "?? " + status

        print(
            f"{g['player']:<22} "
            f"{g['team']:<18} "
            f"{time_str:<8} "
            f"{r.get('expected', '?'):<5} "
            f"{str(r.get('extracted', '?')):<5} "
            f"{r.get('bucket', '?'):<4} "
            f"{str(f25.get('clock', '-')):<14} "
            f"{str(f25.get('added', '-')):<8} "
            f"{str(f25.get('stoppage', '-')):<10} "
            f"{str(f75.get('clock', '-')):<14} "
            f"{str(f75.get('added', '-')):<8} "
            f"{str(f75.get('stoppage', '-')):<10} "
            f"{icon}"
        )

    print("─" * 160)
    total = len(results)
    print(f"\nTotal: {total}  |  ✅ Pass: {pass_count}  |  ❌ Fail: {fail_count}  |  ⚠️  No clock: {no_clock}")

    if fail_count == 0 and total > 0:
        print("\n🎉 ALL GOALS VALIDATED SUCCESSFULLY")
    elif fail_count > 0:
        print(f"\n⚠️  {fail_count} GOAL(S) FAILED VALIDATION — review above")

    client.close()


if __name__ == "__main__":
    asyncio.run(main())
