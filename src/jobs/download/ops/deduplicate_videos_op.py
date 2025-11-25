"""Deduplicate downloaded videos using OpenCV analysis"""
import hashlib
import os
from typing import Any, Dict, List

import cv2
from dagster import OpExecutionContext, op


@op(
    name="deduplicate_videos",
    description="Remove duplicate videos using multi-level detection (file size, hash, OpenCV perceptual hash)",
    tags={"kind": "processing", "purpose": "deduplication"}
)
def deduplicate_videos_op(
    context: OpExecutionContext,
    downloaded_videos: List[Dict[str, Any]]
) -> List[Dict[str, Any]]:
    """
    Deduplicate downloaded videos using multi-level analysis.
    
    Levels of detection:
    1. File size grouping (quick filter)
    2. File hash comparison (start + end chunks)
    3. OpenCV perceptual hash (visual similarity)
    
    Keeps highest quality video from each duplicate group.
    
    Args:
        downloaded_videos: List of dicts with local_path, file_size, metadata
        
    Returns:
        List of unique videos (duplicates removed)
    """
    if not downloaded_videos:
        context.log.warning("⚠️  No videos to deduplicate")
        return []
    
    if len(downloaded_videos) == 1:
        context.log.info("✅ Only 1 video - no deduplication needed")
        return downloaded_videos
    
    context.log.info(f"🔍 Deduplicating {len(downloaded_videos)} videos using multi-level analysis")
    
    # Level 1: Group by file size
    context.log.info("   📏 Level 1: Grouping by file size...")
    size_groups = {}
    for video in downloaded_videos:
        size = video.get("file_size", 0)
        if size not in size_groups:
            size_groups[size] = []
        size_groups[size].append(video)
    
    context.log.info(f"   📊 Found {len(size_groups)} different file sizes")
    
    unique_videos = []
    
    for size, videos in size_groups.items():
        if len(videos) == 1:
            # Only one video of this size - definitely unique
            unique_videos.extend(videos)
            context.log.info(f"   ✅ Size {size} bytes: 1 video (unique)")
            continue
        
        context.log.info(f"   🔍 Size {size} bytes: {len(videos)} videos (checking for duplicates)")
        
        # Level 2: File hash comparison
        hash_groups = {}
        for video in videos:
            try:
                local_path = video.get("local_path")
                if not local_path or not os.path.exists(local_path):
                    context.log.warning(f"   ⚠️  Video file not found: {local_path}")
                    continue
                
                with open(local_path, "rb") as f:
                    # Use first 64KB + last 64KB for faster hashing
                    start_chunk = f.read(65536)
                    f.seek(-65536, 2)
                    end_chunk = f.read()
                    combined_content = start_chunk + end_chunk
                    file_hash = hashlib.md5(combined_content).hexdigest()
                
                if file_hash not in hash_groups:
                    hash_groups[file_hash] = []
                hash_groups[file_hash].append(video)
            
            except Exception as e:
                context.log.error(f"   ❌ Error hashing video: {e}")
                unique_videos.append(video)
                continue
        
        # Level 3: OpenCV perceptual hash for each hash group
        for file_hash, hash_group_videos in hash_groups.items():
            if len(hash_group_videos) == 1:
                unique_videos.extend(hash_group_videos)
                context.log.info(f"   ✅ Hash {file_hash[:8]}: 1 video (unique)")
                continue
            
            context.log.info(f"   🔍 Hash {file_hash[:8]}: {len(hash_group_videos)} videos (OpenCV analysis)")
            
            # Compute perceptual hashes
            perceptual_hashes = []
            for video in hash_group_videos:
                try:
                    phash = compute_video_perceptual_hash(video.get("local_path"), context)
                    perceptual_hashes.append((video, phash))
                except Exception as e:
                    context.log.error(f"   ❌ Error computing perceptual hash: {e}")
                    unique_videos.append(video)
                    continue
            
            # Group by perceptual hash similarity
            used_indices = set()
            for i, (video1, phash1) in enumerate(perceptual_hashes):
                if i in used_indices:
                    continue
                
                duplicate_group = [video1]
                used_indices.add(i)
                
                for j, (video2, phash2) in enumerate(perceptual_hashes):
                    if j <= i or j in used_indices:
                        continue
                    
                    # Compare hashes (Hamming distance)
                    if phash1 and phash2:
                        similarity = hamming_distance(phash1, phash2)
                        if similarity < 5:  # Threshold for considering duplicates
                            duplicate_group.append(video2)
                            used_indices.add(j)
                
                # Keep highest quality from group
                best_video = max(duplicate_group, key=lambda v: v.get("file_size", 0))
                unique_videos.append(best_video)
                
                if len(duplicate_group) > 1:
                    context.log.info(f"   🗑️  Found {len(duplicate_group)} duplicates, keeping best quality")
    
    duplicates_removed = len(downloaded_videos) - len(unique_videos)
    context.log.info(f"✅ Deduplication complete: {len(downloaded_videos)} → {len(unique_videos)} videos ({duplicates_removed} duplicates removed)")
    
    return unique_videos


def compute_video_perceptual_hash(video_path: str, context: OpExecutionContext) -> str | None:
    """
    Compute perceptual hash of video using OpenCV.
    Samples middle frame for hash computation.
    Returns None if video cannot be processed.
    """
    try:
        cap = cv2.VideoCapture(video_path)
        
        if not cap.isOpened():
            context.log.warning(f"   ⚠️  Could not open video: {video_path}")
            return None
        
        # Get middle frame
        frame_count = int(cap.get(cv2.CAP_PROP_FRAME_COUNT))
        middle_frame_idx = frame_count // 2
        
        cap.set(cv2.CAP_PROP_POS_FRAMES, middle_frame_idx)
        ret, frame = cap.read()
        cap.release()
        
        if not ret or frame is None:
            return None
        
        # Resize to 8x8 and convert to grayscale
        frame_small = cv2.resize(frame, (8, 8), interpolation=cv2.INTER_AREA)
        frame_gray = cv2.cvtColor(frame_small, cv2.COLOR_BGR2GRAY)
        
        # Compute hash
        avg = frame_gray.mean()
        hash_bits = (frame_gray > avg).flatten()
        
        # Convert to hex string
        hash_bytes = [int(''.join(map(str, hash_bits[i:i+8].astype(int))), 2) for i in range(0, 64, 8)]
        return ''.join(f'{b:02x}' for b in hash_bytes)
    
    except Exception as e:
        context.log.error(f"   ❌ Error computing perceptual hash for {video_path}: {e}")
        return None


def hamming_distance(hash1: str, hash2: str) -> int:
    """Calculate Hamming distance between two hex hash strings"""
    if not hash1 or not hash2 or len(hash1) != len(hash2):
        return 999  # Large distance for invalid hashes
    
    # Convert hex to binary and compare
    distance = 0
    for c1, c2 in zip(hash1, hash2):
        xor = int(c1, 16) ^ int(c2, 16)
        distance += bin(xor).count('1')
    
    return distance
