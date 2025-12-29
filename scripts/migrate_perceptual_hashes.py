#!/usr/bin/env python3
"""
Migration script to rehash all existing S3 videos with the new dense perceptual hash algorithm.

The new algorithm:
- Samples frames every 0.25 seconds (instead of fixed 1s, 2s, 3s)
- Applies histogram equalization for brightness normalization
- Stores as: "dense:0.25:0.25=abc123,0.50=def456,..."

This handles offset videos better since we do all-pairs comparison.

Usage:
    # Dry run (just show what would be updated)
    python scripts/migrate_perceptual_hashes.py --dry-run
    
    # Actually migrate
    python scripts/migrate_perceptual_hashes.py
    
    # Migrate specific fixture
    python scripts/migrate_perceptual_hashes.py --fixture-id 1234567
    
    # Resume from a specific video
    python scripts/migrate_perceptual_hashes.py --resume-from video_key.mp4
"""

import argparse
import asyncio
import os
import subprocess
import sys
import tempfile
from typing import Optional

# Add src to path
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from src.data.mongo_store import FootyMongoStore
from src.data.s3_store import FootyS3Store


def generate_dhash(frame_bytes: bytes) -> str:
    """Generate a 64-bit dHash from frame bytes using ffmpeg."""
    # Use ffmpeg to resize to 9x8 grayscale and apply histogram equalization
    cmd = [
        "ffmpeg", "-f", "image2pipe", "-i", "pipe:0",
        "-vf", "format=gray,histeq,scale=9:8",
        "-f", "rawvideo", "-pix_fmt", "gray", "pipe:1"
    ]
    result = subprocess.run(cmd, input=frame_bytes, capture_output=True)
    if result.returncode != 0:
        return ""
    
    pixels = list(result.stdout)
    if len(pixels) != 72:  # 9x8
        return ""
    
    # Generate difference hash (compare adjacent horizontal pixels)
    bits = []
    for row in range(8):
        for col in range(8):
            idx = row * 9 + col
            bits.append(1 if pixels[idx] > pixels[idx + 1] else 0)
    
    # Convert to hex
    hash_int = int("".join(str(b) for b in bits), 2)
    return f"{hash_int:016x}"


def generate_dense_hash(video_path: str, interval: float = 0.25) -> str:
    """
    Generate dense perceptual hash from video file.
    Samples every `interval` seconds with histogram equalization.
    """
    # Get video duration
    probe_cmd = [
        "ffprobe", "-v", "error",
        "-show_entries", "format=duration",
        "-of", "default=noprint_wrappers=1:nokey=1",
        video_path
    ]
    result = subprocess.run(probe_cmd, capture_output=True, text=True)
    if result.returncode != 0:
        return ""
    
    try:
        duration = float(result.stdout.strip())
    except ValueError:
        return ""
    
    # Generate timestamps
    timestamps = []
    ts = interval
    while ts < duration:
        timestamps.append(ts)
        ts += interval
    
    if not timestamps:
        # Very short video - just sample at 0.1s
        timestamps = [0.1]
    
    # Extract frames at each timestamp and compute hashes
    hashes = []
    for ts in timestamps:
        cmd = [
            "ffmpeg", "-ss", str(ts), "-i", video_path,
            "-vframes", "1", "-f", "image2pipe", "-vcodec", "png", "pipe:1"
        ]
        result = subprocess.run(cmd, capture_output=True)
        if result.returncode != 0 or not result.stdout:
            continue
        
        h = generate_dhash(result.stdout)
        if h:
            hashes.append(f"{ts:.2f}={h}")
    
    if not hashes:
        return ""
    
    return f"dense:{interval}:{','.join(hashes)}"


async def migrate_videos(
    dry_run: bool = True,
    fixture_id: Optional[int] = None,
    resume_from: Optional[str] = None,
):
    """Migrate all videos to use new dense perceptual hashes."""
    
    mongo = FootyMongoStore()
    s3 = FootyS3Store()
    
    print("🔍 Fetching fixtures with videos from MongoDB...")
    
    # Check both active and completed collections
    query = {"events._s3_videos.0": {"$exists": True}}
    if fixture_id:
        query["_id"] = fixture_id
    
    all_fixtures = []
    collections = [
        (mongo.fixtures_active, "active"),
        (mongo.fixtures_completed, "completed")
    ]
    
    for collection, name in collections:
        fixtures = list(collection.find(query))
        print(f"  📁 {name}: {len(fixtures)} fixtures with videos")
        all_fixtures.extend([(f, collection) for f in fixtures])
    
    print(f"📊 Total: {len(all_fixtures)} fixtures with videos")
    
    total_videos = 0
    migrated = 0
    skipped = 0
    errors = 0
    resuming = resume_from is not None
    
    for fixture, collection in all_fixtures:
        fix_id = fixture.get("_id", "unknown")
        events = fixture.get("events", [])
        
        fixture_modified = False
        
        for event in events:
            event_id = event.get("_event_id", "unknown")
            videos = event.get("_s3_videos", [])
            
            if not videos:
                continue
            
            print(f"\n📁 Fixture {fix_id} / Event: {event_id} ({len(videos)} videos)")
            
            for video in videos:
                s3_url = video.get("url", "")
                old_hash = video.get("perceptual_hash", "")
                
                # Extract S3 key from URL: /video/footy-videos/{key}
                if not s3_url.startswith("/video/footy-videos/"):
                    print(f"  ⚠️  Unexpected URL format: {s3_url}")
                    continue
                s3_key = s3_url.replace("/video/footy-videos/", "")
                
                total_videos += 1
                
                # Handle resume
                if resuming:
                    if s3_key == resume_from:
                        resuming = False
                        print(f"  ▶️  Resuming from: {s3_key}")
                    else:
                        skipped += 1
                        continue
                
                # Skip if already using new format
                if old_hash.startswith("dense:"):
                    print(f"  ⏭️  Already migrated: {s3_key}")
                    skipped += 1
                    continue
                
                print(f"  🔄 Processing: {s3_key}")
                
                if dry_run:
                    print(f"     [DRY RUN] Would rehash: {old_hash[:20] if old_hash else '(no hash)'}...")
                    migrated += 1
                    continue
                
                # Download video to temp file
                try:
                    with tempfile.NamedTemporaryFile(suffix=".mp4", delete=False) as tmp:
                        tmp_path = tmp.name
                    
                    # Download from S3
                    s3.s3_client.download_file(s3.bucket_name, s3_key, tmp_path)
                    
                    # Generate new hash
                    new_hash = generate_dense_hash(tmp_path)
                    
                    if new_hash:
                        video["perceptual_hash"] = new_hash
                        video["hash_version"] = "dense_0.25_histeq"
                        fixture_modified = True
                        migrated += 1
                        
                        hash_count = len(new_hash.split(","))
                        print(f"     ✅ New hash: {hash_count} frames")
                    else:
                        print(f"     ⚠️  Failed to generate hash")
                        errors += 1
                    
                except Exception as e:
                    print(f"     ❌ Error: {e}")
                    errors += 1
                finally:
                    if 'tmp_path' in locals() and os.path.exists(tmp_path):
                        os.remove(tmp_path)
        
        # Update MongoDB if modified
        if fixture_modified and not dry_run:
            collection.update_one(
                {"_id": fix_id},
                {"$set": {"events": events}}
            )
            print(f"  💾 Updated MongoDB for fixture {fix_id}")
    
    # Summary
    print("\n" + "=" * 60)
    print("📊 MIGRATION SUMMARY")
    print("=" * 60)
    print(f"  Total videos:  {total_videos}")
    print(f"  Migrated:      {migrated}")
    print(f"  Skipped:       {skipped}")
    print(f"  Errors:        {errors}")
    
    if dry_run:
        print("\n⚠️  DRY RUN - No changes were made")
        print("   Run without --dry-run to apply changes")


def main():
    parser = argparse.ArgumentParser(
        description="Migrate existing videos to new dense perceptual hash algorithm"
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Show what would be done without making changes"
    )
    parser.add_argument(
        "--fixture-id",
        type=int,
        help="Only migrate videos for a specific fixture"
    )
    parser.add_argument(
        "--resume-from",
        type=str,
        help="Resume migration from a specific S3 key"
    )
    
    args = parser.parse_args()
    
    print("🚀 Dense Perceptual Hash Migration")
    print("=" * 60)
    print(f"  Dry run:      {args.dry_run}")
    print(f"  Fixture ID:   {args.fixture_id or 'ALL'}")
    print(f"  Resume from:  {args.resume_from or 'N/A'}")
    print("=" * 60)
    
    asyncio.run(migrate_videos(
        dry_run=args.dry_run,
        fixture_id=args.fixture_id,
        resume_from=args.resume_from,
    ))


if __name__ == "__main__":
    main()
