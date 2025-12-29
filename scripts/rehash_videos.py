#!/usr/bin/env python3
"""
Rehash all existing videos with new 1s/2s/3s perceptual hash algorithm.

This script:
1. Downloads each video from S3 to a temp file
2. Recalculates hash using fixed timestamps (1s, 2s, 3s) instead of percentages
3. Updates MongoDB with new hash
4. Re-runs deduplication within each event to merge duplicates
5. Updates popularity scores for merged videos
6. Deletes duplicate videos from S3

Run inside the worker container:
    python3 scripts/rehash_videos.py
"""
import os
import sys
import tempfile
import subprocess
from PIL import Image
import io
import boto3
from collections import defaultdict

# Add src to path
sys.path.insert(0, '/app')

from src.data.mongo_store import FootyMongoStore


def extract_frame_hash(file_path: str, timestamp: float) -> str:
    """Extract frame at timestamp and compute dHash"""
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
        
        if result.returncode != 0:
            return "0" * 16
        
        img = Image.open(io.BytesIO(result.stdout))
        img = img.convert('L')
        img = img.resize((9, 8), Image.Resampling.LANCZOS)
        
        pixels = list(img.getdata())
        
        hash_bits = []
        for row in range(8):
            for col in range(8):
                left = pixels[row * 9 + col]
                right = pixels[row * 9 + col + 1]
                hash_bits.append('1' if left < right else '0')
        
        hash_int = int(''.join(hash_bits), 2)
        return format(hash_int, '016x')
        
    except Exception as e:
        print(f"    ⚠️ Failed to extract frame at {timestamp}s: {e}")
        return "0" * 16


def generate_new_hash(file_path: str) -> str:
    """Generate perceptual hash using 1s, 2s, 3s fixed timestamps"""
    hash_1s = extract_frame_hash(file_path, 1.0)
    hash_2s = extract_frame_hash(file_path, 2.0)
    hash_3s = extract_frame_hash(file_path, 3.0)
    return f"{hash_1s}:{hash_2s}:{hash_3s}"


def hamming_distance(hex_a: str, hex_b: str) -> int:
    """Calculate hamming distance between two hex hashes"""
    try:
        int_a = int(hex_a, 16)
        int_b = int(hex_b, 16)
        return bin(int_a ^ int_b).count('1')
    except (ValueError, TypeError):
        return 64


def hashes_match(hash_a: str, hash_b: str, max_per_frame: int = 8, max_total: int = 12) -> bool:
    """Check if two hashes match (new format only: h1:h2:h3)"""
    try:
        parts_a = hash_a.split(':')
        parts_b = hash_b.split(':')
        
        if len(parts_a) != 3 or len(parts_b) != 3:
            return False
        
        total = 0
        for i in range(3):
            dist = hamming_distance(parts_a[i], parts_b[i])
            if dist > max_per_frame:
                return False
            total += dist
        
        return total <= max_total
    except:
        return False


def main():
    store = FootyMongoStore()
    
    # Setup S3 client
    s3 = boto3.client('s3',
        endpoint_url=os.getenv('S3_ENDPOINT_URL'),
        aws_access_key_id=os.getenv('S3_ACCESS_KEY'),
        aws_secret_access_key=os.getenv('S3_SECRET_KEY'),
        region_name='us-east-1'
    )
    bucket = 'footy-videos'
    
    # Process completed fixtures
    total_rehashed = 0
    total_duplicates_merged = 0
    total_deleted_from_s3 = 0
    
    fixtures = list(store.db['fixtures_completed'].find({'events._s3_videos': {'$exists': True, '$ne': []}}))
    print(f"📊 Processing {len(fixtures)} fixtures with videos...")
    
    for doc in fixtures:
        fixture_id = doc['_id']
        home = doc.get('teams', {}).get('home', {}).get('name', 'Unknown')
        away = doc.get('teams', {}).get('away', {}).get('name', 'Unknown')
        
        for evt in doc.get('events', []):
            event_id = evt.get('_event_id', '')
            videos = evt.get('_s3_videos', [])
            
            if not videos:
                continue
            
            print(f"\n🎯 {fixture_id} {home} vs {away} - {event_id} ({len(videos)} videos)")
            
            # Step 1: Rehash all videos
            rehashed_videos = []
            for video in videos:
                s3_url = video.get('url', '')
                if not s3_url.startswith('/video/footy-videos/'):
                    print(f"    ⚠️ Skipping weird URL: {s3_url}")
                    continue
                
                s3_key = s3_url.replace('/video/footy-videos/', '')
                old_hash = video.get('perceptual_hash', '')
                
                # Download to temp file
                with tempfile.NamedTemporaryFile(suffix='.mp4', delete=False) as tmp:
                    tmp_path = tmp.name
                
                try:
                    s3.download_file(bucket, s3_key, tmp_path)
                    new_hash = generate_new_hash(tmp_path)
                    
                    print(f"    ✓ {s3_key.split('/')[-1]}: {old_hash[:20]}... → {new_hash[:20]}...")
                    
                    video_copy = video.copy()
                    video_copy['perceptual_hash'] = new_hash
                    video_copy['_s3_key'] = s3_key  # Track for potential deletion
                    rehashed_videos.append(video_copy)
                    total_rehashed += 1
                    
                except Exception as e:
                    print(f"    ❌ Failed to process {s3_key}: {e}")
                    # Keep original
                    rehashed_videos.append(video)
                finally:
                    if os.path.exists(tmp_path):
                        os.remove(tmp_path)
            
            # Step 2: Deduplicate within event
            if len(rehashed_videos) <= 1:
                # Update MongoDB with new hash
                store.db['fixtures_completed'].update_one(
                    {'_id': fixture_id, 'events._event_id': event_id},
                    {'$set': {'events.$._s3_videos': rehashed_videos}}
                )
                continue
            
            # Group by hash
            hash_groups = defaultdict(list)
            for video in rehashed_videos:
                phash = video.get('perceptual_hash', '')
                
                # Find matching group
                matched_group = None
                for group_hash in hash_groups.keys():
                    if hashes_match(phash, group_hash):
                        matched_group = group_hash
                        break
                
                if matched_group:
                    hash_groups[matched_group].append(video)
                else:
                    hash_groups[phash].append(video)
            
            # Pick best from each group (largest file_size), sum popularity
            deduped_videos = []
            for group_hash, group_videos in hash_groups.items():
                if len(group_videos) == 1:
                    video = group_videos[0]
                    video.pop('_s3_key', None)  # Remove temp key
                    deduped_videos.append(video)
                else:
                    # Multiple duplicates - keep best, merge popularity
                    group_videos.sort(key=lambda v: v.get('file_size', 0), reverse=True)
                    best = group_videos[0]
                    
                    # Sum popularity from all duplicates
                    total_popularity = sum(v.get('popularity', 1) for v in group_videos)
                    best['popularity'] = total_popularity
                    
                    print(f"    🔄 Merged {len(group_videos)} duplicates → keeping {best.get('_s3_key', '').split('/')[-1]} (pop={total_popularity})")
                    
                    # Delete the losers from S3
                    for loser in group_videos[1:]:
                        loser_key = loser.get('_s3_key', '')
                        if loser_key:
                            try:
                                s3.delete_object(Bucket=bucket, Key=loser_key)
                                print(f"    🗑️ Deleted duplicate from S3: {loser_key.split('/')[-1]}")
                                total_deleted_from_s3 += 1
                            except Exception as e:
                                print(f"    ⚠️ Failed to delete {loser_key}: {e}")
                    
                    total_duplicates_merged += len(group_videos) - 1
                    best.pop('_s3_key', None)
                    deduped_videos.append(best)
            
            # Step 3: Recalculate ranks
            deduped_videos.sort(key=lambda v: v.get('popularity', 1), reverse=True)
            for i, video in enumerate(deduped_videos):
                video['rank'] = i + 1
            
            # Step 4: Update MongoDB
            store.db['fixtures_completed'].update_one(
                {'_id': fixture_id, 'events._event_id': event_id},
                {'$set': {'events.$._s3_videos': deduped_videos}}
            )
            
            if len(deduped_videos) < len(videos):
                print(f"    📉 Reduced: {len(videos)} → {len(deduped_videos)} videos")
    
    print(f"\n" + "="*60)
    print(f"✅ COMPLETE")
    print(f"   Rehashed: {total_rehashed} videos")
    print(f"   Duplicates merged: {total_duplicates_merged}")
    print(f"   Deleted from S3: {total_deleted_from_s3}")


if __name__ == '__main__':
    main()
