#!/usr/bin/env python3
"""
Cleanup script to remove duplicate videos from existing events.

Uses the new dense perceptual hash algorithm to identify duplicates,
then keeps the longest + largest videos (same logic as deduplicate_videos).

For each duplicate cluster:
- Keep the video with longest duration
- Keep the video with largest file size  
- If same video is both → keep 1
- If different videos → keep BOTH

Usage:
    # Dry run (just show what would be deleted)
    python scripts/cleanup_duplicate_videos.py --dry-run
    
    # Actually cleanup
    python scripts/cleanup_duplicate_videos.py
    
    # Cleanup specific fixture
    python scripts/cleanup_duplicate_videos.py --fixture-id 1379139
"""

import argparse
import asyncio
import os
import sys
from typing import Dict, List, Set, Tuple

# Add src to path
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from src.data.mongo_store import FootyMongoStore
from src.data.s3_store import FootyS3Store
from src.activities.download import _perceptual_hashes_match


def find_duplicate_clusters(videos: List[Dict]) -> List[List[int]]:
    """
    Group videos into duplicate clusters using union-find approach.
    Returns list of clusters, where each cluster is a list of video indices.
    """
    n = len(videos)
    parent = list(range(n))
    
    def find(x):
        if parent[x] != x:
            parent[x] = find(parent[x])
        return parent[x]
    
    def union(x, y):
        px, py = find(x), find(y)
        if px != py:
            parent[px] = py
    
    # Find all duplicate pairs and union them
    for i in range(n):
        for j in range(i + 1, n):
            h1 = videos[i].get('perceptual_hash', '')
            h2 = videos[j].get('perceptual_hash', '')
            if h1 and h2 and _perceptual_hashes_match(h1, h2):
                union(i, j)
    
    # Group by root
    clusters_dict: Dict[int, List[int]] = {}
    for i in range(n):
        root = find(i)
        if root not in clusters_dict:
            clusters_dict[root] = []
        clusters_dict[root].append(i)
    
    # Only return clusters with 2+ videos (actual duplicates)
    return [c for c in clusters_dict.values() if len(c) > 1]


def get_video_duration(video: Dict) -> float:
    """
    Get video duration from perceptual hash (since duration isn't stored separately).
    
    For dense format, duration is the last timestamp in the hash.
    """
    from src.activities.download import _get_duration_from_hash
    
    perceptual_hash = video.get('perceptual_hash', '')
    return _get_duration_from_hash(perceptual_hash)


def select_keepers(videos: List[Dict], cluster: List[int]) -> Tuple[Set[int], Set[int]]:
    """
    Select which videos to keep from a duplicate cluster.
    Returns (keepers, to_delete) as sets of indices.
    """
    # Find longest duration (extracted from perceptual hash)
    longest_idx = max(cluster, key=lambda i: get_video_duration(videos[i]))
    
    # Find largest file size
    largest_idx = max(cluster, key=lambda i: videos[i].get('file_size', 0))
    
    keepers = {longest_idx, largest_idx}  # Could be 1 or 2 videos
    to_delete = set(cluster) - keepers
    
    return keepers, to_delete


async def cleanup_duplicates(
    dry_run: bool = True,
    fixture_id: int = None,
):
    """Remove duplicate videos from events."""
    
    mongo = FootyMongoStore()
    s3 = FootyS3Store()
    
    print("🔍 Scanning for duplicate videos...")
    
    # Check both collections
    query = {"events._s3_videos.1": {"$exists": True}}
    if fixture_id:
        query["_id"] = fixture_id
    
    collections = [
        (mongo.fixtures_active, "active"),
        (mongo.fixtures_completed, "completed")
    ]
    
    total_deleted = 0
    total_bytes_saved = 0
    events_cleaned = 0
    
    for collection, coll_name in collections:
        fixtures = list(collection.find(query))
        if not fixtures:
            continue
        
        print(f"\n📁 Processing {len(fixtures)} fixtures from {coll_name}...")
        
        for fixture in fixtures:
            fix_id = fixture.get("_id", "unknown")
            events = fixture.get("events", [])
            fixture_modified = False
            
            for event in events:
                videos = event.get("_s3_videos", [])
                if len(videos) < 2:
                    continue
                
                event_id = event.get("_event_id", "unknown")
                
                # Find duplicate clusters
                clusters = find_duplicate_clusters(videos)
                if not clusters:
                    continue
                
                print(f"\n  📹 {event_id}: {len(videos)} videos, {len(clusters)} dup cluster(s)")
                
                # Collect all videos to delete
                all_to_delete: Set[int] = set()
                
                for cluster in clusters:
                    keepers, to_delete = select_keepers(videos, cluster)
                    
                    # Log cluster info
                    keeper_info = []
                    for idx in keepers:
                        v = videos[idx]
                        duration = get_video_duration(v)
                        keeper_info.append(f"V{idx+1} ({v.get('file_size', 0):,}b, {duration:.1f}s)")
                    
                    delete_info = []
                    for idx in to_delete:
                        v = videos[idx]
                        duration = get_video_duration(v)
                        delete_info.append(f"V{idx+1} ({v.get('file_size', 0):,}b, {duration:.1f}s)")
                    
                    print(f"    Cluster: keep [{', '.join(keeper_info)}], delete [{', '.join(delete_info)}]")
                    all_to_delete.update(to_delete)
                
                if not all_to_delete:
                    continue
                
                events_cleaned += 1
                
                # Calculate bytes saved
                bytes_saved = sum(videos[i].get('file_size', 0) for i in all_to_delete)
                total_bytes_saved += bytes_saved
                total_deleted += len(all_to_delete)
                
                if dry_run:
                    print(f"    [DRY RUN] Would delete {len(all_to_delete)} videos, save {bytes_saved:,} bytes")
                    continue
                
                # Actually delete from S3 and update MongoDB
                for idx in sorted(all_to_delete, reverse=True):
                    video = videos[idx]
                    s3_url = video.get('url', '')
                    
                    # Extract S3 key from URL
                    if s3_url.startswith("/video/footy-videos/"):
                        s3_key = s3_url.replace("/video/footy-videos/", "")
                        try:
                            s3.s3_client.delete_object(Bucket=s3.bucket_name, Key=s3_key)
                            print(f"    🗑️  Deleted S3: {s3_key}")
                        except Exception as e:
                            print(f"    ⚠️  S3 delete failed: {e}")
                
                # Remove deleted videos from list (reverse order to preserve indices)
                for idx in sorted(all_to_delete, reverse=True):
                    videos.pop(idx)
                
                # Recalculate ranks for remaining videos
                videos_sorted = sorted(
                    videos,
                    key=lambda v: (v.get("popularity", 1), v.get("file_size", 0)),
                    reverse=True
                )
                for rank, video in enumerate(videos_sorted, start=1):
                    video["rank"] = rank
                
                event["_s3_videos"] = videos_sorted
                fixture_modified = True
                print(f"    ✅ Removed {len(all_to_delete)} videos, {len(videos_sorted)} remaining")
            
            # Update MongoDB if modified
            if fixture_modified and not dry_run:
                collection.update_one(
                    {"_id": fix_id},
                    {"$set": {"events": events}}
                )
                print(f"  💾 Updated fixture {fix_id}")
    
    # Summary
    print("\n" + "=" * 60)
    print("📊 CLEANUP SUMMARY")
    print("=" * 60)
    print(f"  Events cleaned:    {events_cleaned}")
    print(f"  Videos deleted:    {total_deleted}")
    print(f"  Space saved:       {total_bytes_saved:,} bytes ({total_bytes_saved/1024/1024:.1f} MB)")
    
    if dry_run:
        print("\n⚠️  DRY RUN - No changes were made")
        print("   Run without --dry-run to apply changes")


def main():
    parser = argparse.ArgumentParser(
        description="Remove duplicate videos from events, keeping longest + largest"
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Show what would be done without making changes"
    )
    parser.add_argument(
        "--fixture-id",
        type=int,
        help="Only cleanup videos for a specific fixture"
    )
    
    args = parser.parse_args()
    
    print("🧹 Duplicate Video Cleanup")
    print("=" * 60)
    print(f"  Dry run:      {args.dry_run}")
    print(f"  Fixture ID:   {args.fixture_id or 'ALL'}")
    print("=" * 60)
    
    asyncio.run(cleanup_duplicates(
        dry_run=args.dry_run,
        fixture_id=args.fixture_id,
    ))


if __name__ == "__main__":
    main()
