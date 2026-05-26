"""
Upload activities — S3 deduplication and upload operations for UploadWorkflow.

Phase 3 (P3b, 2026-05-26) converted this from a single 1484-line module to
a package:
  - upload/core.py   — the upload pipeline + Mongo writes + cleanup
  - upload/dedup.py  — MD5 and perceptual deduplication activities
  - src/utils/dedup_match.py — pure perceptual-hash matching helpers
                                 (earmarked for deletion in Phase 5)

This __init__.py re-exports every public activity (and the back-compat
internal helpers a few external places reference) so callers using
`from src.activities import upload as upload_activities` continue to work
unchanged.
"""

# Core activities
from src.activities.upload.core import (
    queue_videos_for_upload,
    fetch_event_data,
    upload_single_video,
    update_video_in_place,
    bump_video_popularity,
    save_video_objects,
    recalculate_video_ranks,
    cleanup_individual_files,
    cleanup_fixture_temp_dirs,
    cleanup_upload_temp,
)

# Dedup activities + cluster helpers
from src.activities.upload.dedup import (
    deduplicate_by_md5,
    deduplicate_videos,
    _pick_best_video_from_cluster,
    _should_replace_s3_video,
)

# Pure perceptual-hash matching helpers
from src.utils.dedup_match import (
    _hamming_distance,
    _perceptual_hashes_match,
    _dense_hashes_match,
    _parse_perceptual_hash,
)

__all__ = [
    # Core
    "queue_videos_for_upload",
    "fetch_event_data",
    "upload_single_video",
    "update_video_in_place",
    "bump_video_popularity",
    "save_video_objects",
    "recalculate_video_ranks",
    "cleanup_individual_files",
    "cleanup_fixture_temp_dirs",
    "cleanup_upload_temp",
    # Dedup
    "deduplicate_by_md5",
    "deduplicate_videos",
]
