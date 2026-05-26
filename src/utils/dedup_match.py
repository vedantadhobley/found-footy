"""
Perceptual hash matching helpers — pure functions for video deduplication.

Extracted from src/activities/upload.py in Phase 3 (P3b, 2026-05-26).
No Temporal imports, no MongoDB, no S3 — these are pure-Python comparison
helpers operating on hash strings + video metadata dicts.

Phase 5 of the roadmap may delete this entire module once image-embedding
similarity replaces dHash + Hamming. Until then, isolating the helpers
here:
  - makes them unit-testable in isolation
  - constrains the surface area Phase 5 has to swap out
  - keeps src/activities/upload/ focused on Temporal activities
"""

from typing import Any, Dict, List

from src.utils.config import HASH_VERSION, MAX_HAMMING_DISTANCE, MIN_CONSECUTIVE_MATCHES


def _hamming_distance(hex_a: str, hex_b: str) -> int:
    """
    Calculate hamming distance (bit difference) between two hex hashes.
    
    Args:
        hex_a: First hex hash (e.g., "1fcee5dad69a67cc")
        hex_b: Second hex hash
        
    Returns:
        Number of differing bits (0-64 for 64-bit hashes)
    """
    try:
        int_a = int(hex_a, 16)
        int_b = int(hex_b, 16)
        return bin(int_a ^ int_b).count('1')
    except (ValueError, TypeError):
        return 64  # Max distance on error

def _perceptual_hashes_match(
    hash_a: str, 
    hash_b: str, 
    max_hamming: int = MAX_HAMMING_DISTANCE,
    min_consecutive_matches: int = MIN_CONSECUTIVE_MATCHES
) -> bool:
    """
    Check if two perceptual hashes represent the same video.
    
    Requires multiple CONSECUTIVE frames to match to avoid false positives
    from videos with similar content (e.g., goals 1 minute apart).
    
    Supports two formats:
    1. Dense format (new): "dense:<interval>:<ts1>=<hash1>,<ts2>=<hash2>,..."
       - Uses sliding window to find consecutive matching frames
       - Requires min_consecutive_matches frames in a row to match
       
    2. Legacy format: "hash1:hash2:hash3" or "duration:hash1:hash2:hash3"
       - Fixed timestamp comparison (backward compatible)
       - Requires 2 of 3 frames to match
    
    Args:
        hash_a: First hash (dense or legacy format)
        hash_b: Second hash (dense or legacy format)
        max_hamming: Max hamming distance for a frame match (from config)
        min_consecutive_matches: Min consecutive frames that must match (from config)
        
    Returns:
        True if videos match (have consecutive matching frames)
    """
    # Early return for empty hashes (videos without hashes can't be compared)
    if not hash_a or not hash_b:
        return False
    
    try:
        # Check if both are dense format
        is_dense_a = hash_a.startswith("dense:")
        is_dense_b = hash_b.startswith("dense:")
        
        if is_dense_a and is_dense_b:
            # Dense format: require consecutive frames to match
            return _dense_hashes_match(hash_a, hash_b, max_hamming, min_consecutive_matches)
        
        # Legacy format or mixed: use simple matching
        hashes_a = _parse_perceptual_hash(hash_a)
        hashes_b = _parse_perceptual_hash(hash_b)
        
        if not hashes_a or not hashes_b:
            return False
        
        # For legacy format (3 hashes at 25%, 50%, 75%), require 2 of 3 to match
        matches = 0
        for h_a in hashes_a:
            for h_b in hashes_b:
                dist = _hamming_distance(h_a, h_b)
                if dist <= max_hamming:
                    matches += 1
                    break
        
        # Legacy: 2 of 3 frames must match
        return matches >= 2
        
    except Exception:
        return False

def _dense_hashes_match(
    hash_a: str,
    hash_b: str,
    max_hamming: int = MAX_HAMMING_DISTANCE,
    min_consecutive: int = MIN_CONSECUTIVE_MATCHES
) -> bool:
    """
    Check if two dense perceptual hashes match with consecutive frame requirement.
    
    For true duplicates (possibly with different start times), consecutive frames
    in video A should match frames in video B with a CONSISTENT time offset.
    
    Algorithm:
    1. For each possible time offset between A and B
    2. Count how many consecutive frames match at that offset
    3. If any offset has >= min_consecutive matches, videos are duplicates
    
    Args:
        hash_a: Dense hash "dense:<interval>:<ts1>=<hash1>,..."
        hash_b: Dense hash "dense:<interval>:<ts2>=<hash2>,..."
        max_hamming: Max hamming distance for frame match
        min_consecutive: Min consecutive frames required
        
    Returns:
        True if videos have consecutive matching frames at consistent offset
    """
    try:
        # Parse dense hashes into {timestamp: hash_int}
        def parse_dense(h):
            parts = h.split(":", 2)
            if len(parts) < 3:
                return {}, 0.25
            interval = float(parts[1])
            frames = {}
            for pair in parts[2].split(","):
                if "=" in pair:
                    ts_str, hash_hex = pair.split("=", 1)
                    frames[float(ts_str)] = int(hash_hex, 16)
            return frames, interval
        
        frames_a, interval_a = parse_dense(hash_a)
        frames_b, interval_b = parse_dense(hash_b)
        
        if len(frames_a) < min_consecutive or len(frames_b) < min_consecutive:
            return False
        
        timestamps_a = sorted(frames_a.keys())
        timestamps_b = sorted(frames_b.keys())
        
        # Try each possible starting alignment between A and B
        # For each frame in A, try aligning it with each frame in B
        for start_a in timestamps_a:
            for start_b in timestamps_b:
                offset = start_b - start_a  # Time offset: B = A + offset
                
                # Count consecutive matches at this offset
                consecutive = 0
                max_consecutive = 0
                
                for ts_a in timestamps_a:
                    ts_b = ts_a + offset
                    
                    # Find closest timestamp in B (within tolerance)
                    tolerance = interval_a / 2
                    matched = False
                    
                    for actual_ts_b in timestamps_b:
                        if abs(actual_ts_b - ts_b) <= tolerance:
                            h_a = frames_a[ts_a]
                            h_b = frames_b[actual_ts_b]
                            dist = bin(h_a ^ h_b).count('1')
                            if dist <= max_hamming:
                                matched = True
                                break
                    
                    if matched:
                        consecutive += 1
                        max_consecutive = max(max_consecutive, consecutive)
                        if max_consecutive >= min_consecutive:
                            return True
                    else:
                        consecutive = 0
        
        return False
        
    except Exception:
        return False

def _parse_perceptual_hash(hash_str: str) -> list:
    """
    Parse a perceptual hash string into a list of frame hashes.
    
    Supports:
    - Dense format: "dense:0.25:0.25=abc123,0.50=def456,..."
    - Legacy format: "hash1:hash2:hash3" or "duration:hash1:hash2:hash3"
    
    Returns:
        List of hex hash strings
    """
    if not hash_str:
        return []
    
    if hash_str.startswith("dense:"):
        # Dense format: "dense:<interval>:<ts1>=<hash1>,<ts2>=<hash2>,..."
        parts = hash_str.split(":", 2)
        if len(parts) < 3:
            return []
        
        hash_list = parts[2]
        hashes = []
        for item in hash_list.split(","):
            if "=" in item:
                _, frame_hash = item.split("=", 1)
                if frame_hash:
                    hashes.append(frame_hash)
        return hashes
    else:
        # Legacy format: "hash1:hash2:hash3" or "duration:hash1:hash2:hash3"
        parts = hash_str.split(":")
        if len(parts) == 4:
            # Old format with duration prefix - skip it
            return parts[1:4]
        elif len(parts) == 3:
            # New legacy format without duration
            return parts
        else:
            return []
