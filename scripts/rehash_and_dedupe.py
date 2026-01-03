#!/usr/bin/env python3
"""
Rehash and deduplicate videos that have empty perceptual hashes.

This script:
1. Finds all S3 videos with empty hashes
2. Downloads them from S3 temporarily
3. Generates perceptual hashes
4. Runs deduplication
5. Deletes duplicates from S3 and updates MongoDB
"""
import os
import sys
import tempfile
import subprocess
from pymongo import MongoClient
import boto3
from botocore.client import Config

# Import hash generation
sys.path.insert(0, '/app')
from src.activities.download import _generate_perceptual_hash, _perceptual_hashes_match

# MongoDB
uri = os.environ.get('MONGODB_URI')
client = MongoClient(uri)
db = client.get_default_database()

# S3
s3 = boto3.client(
    's3',
    endpoint_url=os.environ.get('S3_ENDPOINT', 'http://found-footy-prod-minio:9000'),
    aws_access_key_id=os.environ.get('S3_ACCESS_KEY', 'minioadmin'),
    aws_secret_access_key=os.environ.get('S3_SECRET_KEY', 'minioadmin'),
    config=Config(signature_version='s3v4'),
    region_name='us-east-1'
)
BUCKET = os.environ.get('S3_BUCKET', 'footy-videos')


def get_video_duration(file_path: str) -> float:
    """Get video duration using ffprobe"""
    try:
        cmd = [
            "ffprobe", "-v", "error",
            "-show_entries", "format=duration",
            "-of", "default=noprint_wrappers=1:nokey=1",
            file_path
        ]
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
        return float(result.stdout.strip())
    except:
        return 0


def rehash_event(fixture_id: int, event_id: str):
    """Rehash and deduplicate videos for a specific event"""
    
    # Find the event
    fixture = db.fixtures_active.find_one({'fixture.id': fixture_id})
    if not fixture:
        fixture = db.fixtures_completed.find_one({'fixture.id': fixture_id})
    if not fixture:
        print(f"Fixture {fixture_id} not found")
        return
    
    event = None
    event_idx = None
    for idx, e in enumerate(fixture.get('events', [])):
        if e.get('_event_id') == event_id:
            event = e
            event_idx = idx
            break
    
    if not event:
        print(f"Event {event_id} not found")
        return
    
    s3_videos = event.get('_s3_videos', [])
    print(f"\n=== Processing {event_id} ===")
    print(f"Total S3 videos: {len(s3_videos)}")
    
    # Find videos without hashes
    videos_need_hash = []
    videos_with_hash = []
    
    for i, vid in enumerate(s3_videos):
        h = vid.get('perceptual_hash', '')
        if h and h.startswith('dense:') and len(h) > 20:
            videos_with_hash.append((i, vid))
        else:
            videos_need_hash.append((i, vid))
    
    print(f"Videos with hash: {len(videos_with_hash)}")
    print(f"Videos need hash: {len(videos_need_hash)}")
    
    if not videos_need_hash:
        print("No videos need rehashing")
        return
    
    # Download and hash videos that need it
    with tempfile.TemporaryDirectory() as tmpdir:
        for i, vid in videos_need_hash:
            s3_key = vid.get('_s3_key', '') or vid.get('s3_key', '')
            if not s3_key:
                print(f"  Video {i}: No S3 key, skipping")
                continue
            
            # Download from S3
            local_path = os.path.join(tmpdir, f"video_{i}.mp4")
            print(f"  Video {i}: Downloading {s3_key}...")
            
            try:
                s3.download_file(BUCKET, s3_key, local_path)
            except Exception as e:
                print(f"  Video {i}: Download failed: {e}")
                continue
            
            # Get duration
            duration = get_video_duration(local_path)
            if duration <= 0:
                print(f"  Video {i}: Could not get duration, skipping")
                continue
            
            print(f"  Video {i}: Duration {duration:.1f}s, generating hash...")
            
            # Generate hash
            perceptual_hash = _generate_perceptual_hash(local_path, duration, heartbeat_fn=None)
            
            if perceptual_hash and perceptual_hash.startswith('dense:') and len(perceptual_hash) > 20:
                vid['perceptual_hash'] = perceptual_hash
                frame_count = len(perceptual_hash.split(':')[2].split(',')) if ':' in perceptual_hash else 0
                print(f"  Video {i}: Generated hash with {frame_count} frames")
            else:
                print(f"  Video {i}: Hash generation failed")
    
    # Now run deduplication
    print("\n--- Running deduplication ---")
    
    all_videos = s3_videos[:]  # Make a copy
    duplicates_to_remove = []
    
    # Compare all pairs
    for i in range(len(all_videos)):
        if i in [d[0] for d in duplicates_to_remove]:
            continue
        
        hash_i = all_videos[i].get('perceptual_hash', '')
        if not hash_i:
            continue
        
        for j in range(i + 1, len(all_videos)):
            if j in [d[0] for d in duplicates_to_remove]:
                continue
            
            hash_j = all_videos[j].get('perceptual_hash', '')
            if not hash_j:
                continue
            
            if _perceptual_hashes_match(hash_i, hash_j):
                # Determine which to keep (higher quality = larger file)
                size_i = all_videos[i].get('file_size', 0)
                size_j = all_videos[j].get('file_size', 0)
                
                if size_i >= size_j:
                    duplicates_to_remove.append((j, all_videos[j]))
                    print(f"  Duplicate: Video {j} matches Video {i}, removing {j} (smaller)")
                else:
                    duplicates_to_remove.append((i, all_videos[i]))
                    print(f"  Duplicate: Video {i} matches Video {j}, removing {i} (smaller)")
                    break  # i is being removed, stop comparing it
    
    if not duplicates_to_remove:
        print("No duplicates found")
        # Still update the hashes
        collection = 'fixtures_active' if db.fixtures_active.find_one({'fixture.id': fixture_id}) else 'fixtures_completed'
        db[collection].update_one(
            {'fixture.id': fixture_id},
            {'$set': {f'events.{event_idx}._s3_videos': all_videos}}
        )
        print("Updated hashes in MongoDB")
        return
    
    # Remove duplicates
    print(f"\n--- Removing {len(duplicates_to_remove)} duplicates ---")
    
    # Track which survivors get popularity bumps from which duplicates
    # Format: {survivor_idx: [duplicate_indices]}
    survivor_to_dupes = {}
    for dup_idx, dup_vid in duplicates_to_remove:
        # Find which video this duplicate matched
        for i in range(len(all_videos)):
            if i == dup_idx or i in [d[0] for d in duplicates_to_remove]:
                continue
            hash_i = all_videos[i].get('perceptual_hash', '')
            hash_dup = dup_vid.get('perceptual_hash', '')
            if hash_i and hash_dup and _perceptual_hashes_match(hash_i, hash_dup):
                if i not in survivor_to_dupes:
                    survivor_to_dupes[i] = []
                survivor_to_dupes[i].append(dup_idx)
                break
    
    # Bump popularity for survivors
    for survivor_idx, dupe_indices in survivor_to_dupes.items():
        old_pop = all_videos[survivor_idx].get('popularity', 1)
        new_pop = old_pop + len(dupe_indices)
        all_videos[survivor_idx]['popularity'] = new_pop
        print(f"  Video {survivor_idx}: popularity {old_pop} -> {new_pop} (+{len(dupe_indices)} dupes)")
    
    indices_to_remove = set(d[0] for d in duplicates_to_remove)
    new_videos = [v for i, v in enumerate(all_videos) if i not in indices_to_remove]
    
    # Recalculate ranks based on popularity
    new_videos.sort(key=lambda v: v.get('popularity', 1), reverse=True)
    for i, v in enumerate(new_videos):
        v['rank'] = i + 1
    
    # Delete from S3
    for idx, vid in duplicates_to_remove:
        s3_key = vid.get('_s3_key', '') or vid.get('s3_key', '')
        if s3_key:
            print(f"  Deleting from S3: {s3_key}")
            try:
                s3.delete_object(Bucket=BUCKET, Key=s3_key)
            except Exception as e:
                print(f"  Failed to delete {s3_key}: {e}")
    
    # Update MongoDB
    collection = 'fixtures_active' if db.fixtures_active.find_one({'fixture.id': fixture_id}) else 'fixtures_completed'
    db[collection].update_one(
        {'fixture.id': fixture_id},
        {'$set': {f'events.{event_idx}._s3_videos': new_videos}}
    )
    
    print(f"\nDone! Reduced from {len(all_videos)} to {len(new_videos)} videos")


if __name__ == '__main__':
    # Fix videos with empty hashes from older events
    # These are from Dec 31 and Jan 1
    rehash_event(1347273, '1347273_1501_137303_Goal_1')  # Gabon vs Ivory Coast
    rehash_event(1379153, '1379153_52_25927_Goal_1')     # Crystal Palace vs Fulham
