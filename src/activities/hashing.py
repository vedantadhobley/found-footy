"""
Hashing activities — perceptual hash generation for video deduplication.

Extracted from download.py in Phase 3 (P3a, 2026-05-26). Currently uses
dHash (difference hash) with histogram equalization on dense (0.25s)
frame sampling.

Phase 5 of the roadmap may replace this with Qwen3-VL image embeddings +
cosine similarity. If that lands, the entire module is deletable —
which is why the split was worth doing now.

NOTE on the log `module` field — we keep `MODULE = "download"` inside
these functions so existing Grafana dashboards continue to filter the
same logical pipeline stage. The filename is documentation-of-
organization; the MODULE constant is documentation-of-identity.

Module split is back-compat: src/activities/download.py re-exports both
public symbols (generate_video_hash, _generate_perceptual_hash) so
callers using `from src.activities import download as download_activities`
continue to work unchanged.
"""

from typing import Any, Dict
import os

from temporalio import activity

from src.utils.footy_logging import log

# Log identity kept as "download" for dashboard continuity.
MODULE = "download"


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
            log.warning(activity.logger, MODULE, "hash_frame_failed",
                        "Failed to extract frame for hash",
                        timestamp=timestamp, error=str(e))
            return ""

    # Extract hashes at 0.25s intervals
    hashes = []
    t = interval
    frame_count = 0

    while t < duration - 0.3:  # Stop before last 0.3s to avoid end-of-file issues
        # Send heartbeat BEFORE each ffmpeg call to prevent timeout during resource contention
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
        log.error(activity.logger, MODULE, "hash_file_missing",
                  "File not found for hash generation", file_path=file_path)
        return {"perceptual_hash": "", "error": "file_not_found"}

    log.info(activity.logger, MODULE, "hash_started",
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
                log.info(activity.logger, MODULE, "hash_generated",
                         "Hash generated successfully",
                         frame_count=frame_count, interval=0.25)
            else:
                log.warning(activity.logger, MODULE, "hash_low_frames",
                            "Low frame count in hash",
                            frame_count=frame_count, duration=duration,
                            file=os.path.basename(file_path))
        else:
            log.error(activity.logger, MODULE, "hash_no_frames",
                      "No frames extracted for hash",
                      file=os.path.basename(file_path), duration=duration)
            return {"perceptual_hash": "", "error": "no_frames_extracted"}
    else:
        log.error(activity.logger, MODULE, "hash_invalid_format",
                  "Invalid hash format",
                  file=os.path.basename(file_path), hash_prefix=perceptual_hash[:50])
        return {"perceptual_hash": "", "error": "invalid_hash_format"}

    return {"perceptual_hash": perceptual_hash}
