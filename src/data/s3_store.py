"""S3 storage utilities for Found Footy video downloads (MinIO backend)"""
import os
import tempfile
import boto3
from botocore.exceptions import ClientError, NoCredentialsError
from datetime import datetime
from typing import Optional, Dict, Any
from pathlib import Path

from src.utils.config import (
    S3_ENDPOINT_URL,
    S3_ACCESS_KEY,
    S3_SECRET_KEY,
    S3_BUCKET_NAME,
    S3_REGION,
)
from src.utils.footy_logging import get_fallback_logger, log

MODULE = "s3_store"

def _log_info(action: str, msg: str, **kwargs):
    """Log info using fallback logger."""
    log.info(get_fallback_logger(), MODULE, action, msg, **kwargs)

def _log_warning(action: str, msg: str, **kwargs):
    """Log warning using fallback logger."""
    log.warning(get_fallback_logger(), MODULE, action, msg, **kwargs)

def _log_error(action: str, msg: str, **kwargs):
    """Log error using fallback logger."""
    log.error(get_fallback_logger(), MODULE, action, msg, error=kwargs.get('error', ''), **{k: v for k, v in kwargs.items() if k != 'error'})


class FootyS3Store:
    """MinIO S3-compatible storage manager for video files"""
    
    def __init__(self):
        # Use centralized config (can be overridden by env vars)
        self.endpoint_url = os.getenv('S3_ENDPOINT_URL') or S3_ENDPOINT_URL
        self.access_key = os.getenv('S3_ACCESS_KEY') or S3_ACCESS_KEY
        self.secret_key = os.getenv('S3_SECRET_KEY') or S3_SECRET_KEY
        self.bucket_name = os.getenv('S3_BUCKET_NAME') or S3_BUCKET_NAME
        
        # Initialize S3 client (MinIO via boto3)
        self.s3_client = boto3.client(
            's3',
            endpoint_url=self.endpoint_url,
            aws_access_key_id=self.access_key,
            aws_secret_access_key=self.secret_key,
            region_name=S3_REGION  # MinIO requires a region but ignores it
        )
        
        self._ensure_bucket_exists()
    
    def _ensure_bucket_exists(self):
        """Create bucket if it doesn't exist - REQUIRED for all operations"""
        try:
            self.s3_client.head_bucket(Bucket=self.bucket_name)
            _log_info("bucket_exists", f"S3 bucket '{self.bucket_name}' exists", bucket=self.bucket_name)
        except ClientError as e:
            error_code = e.response.get('Error', {}).get('Code')
            # Handle both 404 (numeric) and 'NoSuchBucket' (string) error codes
            if error_code == 404 or error_code == '404' or 'NoSuchBucket' in str(e):
                _log_info("bucket_creating", f"Bucket '{self.bucket_name}' doesn't exist, creating...", bucket=self.bucket_name)
                try:
                    self.s3_client.create_bucket(Bucket=self.bucket_name)
                    _log_info("bucket_created", f"Created S3 bucket: {self.bucket_name}", bucket=self.bucket_name)
                except ClientError as create_error:
                    # Check if bucket was created by another process (race condition)
                    if 'BucketAlreadyOwnedByYou' in str(create_error):
                        _log_info("bucket_exists_race", f"Bucket {self.bucket_name} already exists (created by another process)", bucket=self.bucket_name)
                    else:
                        _log_error("bucket_create_failed", f"FATAL: Failed to create bucket", error=str(create_error), bucket=self.bucket_name)
                        raise  # Re-raise - this is a fatal error
            else:
                _log_error("bucket_check_failed", f"FATAL: Error checking bucket", error=str(e), bucket=self.bucket_name)
                raise  # Re-raise - this is a fatal error
        except NoCredentialsError as e:
            _log_error("no_credentials", f"FATAL: S3 credentials not configured", error=str(e))
            raise
        except Exception as e:
            _log_error("unexpected_s3_error", f"FATAL: Unexpected S3 error", error=str(e))
            raise
    
    def upload_video(self, local_file_path: str, s3_key: str, metadata: Dict[str, Any] = None) -> Optional[str]:
        """Upload video file to S3 with metadata
        
        Args:
            local_file_path: Path to local video file
            s3_key: S3 key (path) for the file
            metadata: Optional metadata to attach to object headers
            
        Returns:
            S3 URL if successful, None otherwise
        """
        try:
            # Clean metadata for S3 headers
            clean_metadata = {}
            if metadata:
                for key, value in metadata.items():
                    if value is not None:
                        clean_value = str(value)
                        # Remove problematic characters
                        clean_value = clean_value.replace('\n', ' ').replace('\r', ' ').replace('\t', ' ')
                        # Truncate long values
                        if len(clean_value) > 100:
                            clean_value = clean_value[:97] + "..."
                        # Remove non-ASCII
                        clean_value = ''.join(char if ord(char) < 128 else '?' for char in clean_value)
                        clean_metadata[key] = clean_value
            
            # Determine content type
            extension = Path(local_file_path).suffix.lower().lstrip('.')
            content_type = f'video/{extension}' if extension else 'video/mp4'
            
            # Build upload arguments
            extra_args = {
                'Metadata': clean_metadata,
                'ContentType': content_type
            }
            
            # Upload with auto-retry on NoSuchBucket
            try:
                self.s3_client.upload_file(
                    local_file_path,
                    self.bucket_name,
                    s3_key,
                    ExtraArgs=extra_args
                )
            except ClientError as upload_error:
                # If bucket disappeared, recreate and retry ONCE
                if 'NoSuchBucket' in str(upload_error):
                    _log_warning("bucket_disappeared", "Bucket disappeared, recreating and retrying upload...", bucket=self.bucket_name)
                    self._ensure_bucket_exists()
                    # Retry upload
                    self.s3_client.upload_file(
                        local_file_path,
                        self.bucket_name,
                        s3_key,
                        ExtraArgs=extra_args
                    )
                else:
                    raise  # Re-raise other errors
            
            # Return relative path for frontend video proxy (not internal MinIO URL)
            # Frontend API will serve videos via /video/{bucket}/{path}
            s3_url = f"/video/{self.bucket_name}/{s3_key}"
            _log_info("video_uploaded", f"Uploaded: {s3_key}", s3_key=s3_key)
            return s3_url
            
        except Exception as e:
            import traceback
            _log_error("upload_failed", f"FATAL: Upload failed for {s3_key}", error=str(e), s3_key=s3_key, traceback=traceback.format_exc())
            raise  # RAISE instead of returning None - let caller handle retry
    
    def upload_video_file(self, local_file_path: str, goal_id: str, video_index: int, 
                         metadata: Dict[str, Any] = None) -> Dict[str, Any]:
        """Upload video file to S3 with goal-based structure
        
        Args:
            local_file_path: Path to local video file
            goal_id: Goal/event ID
            video_index: Video index number
            metadata: Optional metadata
            
        Returns:
            Dict with status, s3_key, etc.
        """
        try:
            # Get file extension
            file_extension = Path(local_file_path).suffix.lstrip('.')
            if not file_extension:
                file_extension = "mp4"
            
            # Generate S3 key: fixture_id/goal_id/goal_id_index.ext
            fixture_id = goal_id.split('_')[0]
            s3_key = f"{fixture_id}/{goal_id}/{goal_id}_{video_index}.{file_extension}"
            
            # Upload
            s3_url = self.upload_video(local_file_path, s3_key, metadata)
            
            if s3_url:
                return {
                    "status": "success",
                    "s3_key": s3_key,
                    "bucket": self.bucket_name,
                    "file_size": Path(local_file_path).stat().st_size,
                    "url": s3_url
                }
            else:
                return {"status": "error", "error": "Upload failed", "s3_key": None}
            
        except Exception as e:
            _log_error("upload_video_file_failed", f"Upload failed", error=str(e), goal_id=goal_id)
            return {"status": "error", "error": str(e), "s3_key": None}
    
    def list_goal_videos(self, goal_id: str) -> list:
        """List all videos for a specific goal"""
        try:
            fixture_id = goal_id.split('_')[0]
            prefix = f"{fixture_id}/{goal_id}/"
            
            response = self.s3_client.list_objects_v2(
                Bucket=self.bucket_name,
                Prefix=prefix
            )
            
            videos = []
            for obj in response.get('Contents', []):
                videos.append({
                    'key': obj['Key'],
                    'size': obj['Size'],
                    'last_modified': obj['LastModified'].isoformat(),
                    'url': f"{self.endpoint_url}/{self.bucket_name}/{obj['Key']}"
                })
            
            return videos
        
        except Exception as e:
            _log_error("list_goal_videos_error", f"Error listing videos", error=str(e), goal_id=goal_id)
            return []
    
    def get_existing_video_hashes(self, fixture_id: int, event_id: str) -> Dict[str, Dict[str, Any]]:
        """Get MD5 hashes of videos already uploaded to S3 for this event.
        
        Returns:
            Dict mapping hash -> {key, size, url}
        """
        try:
            prefix = f"{fixture_id}/{event_id}/"
            
            response = self.s3_client.list_objects_v2(
                Bucket=self.bucket_name,
                Prefix=prefix
            )
            
            existing_hashes = {}
            for obj in response.get('Contents', []):
                # Extract hash from filename (format: {event_id}_{index}_{hash}.mp4)
                key = obj['Key']
                filename = key.split('/')[-1]
                # Hash is embedded in filename, extract it
                parts = filename.replace('.mp4', '').split('_')
                if len(parts) >= 3:
                    file_hash = parts[-1]  # Last part is hash
                    existing_hashes[file_hash] = {
                        'key': key,
                        'size': obj['Size'],
                        'url': f"{self.endpoint_url}/{self.bucket_name}/{key}"
                    }
            
            return existing_hashes
        
        except Exception as e:
            _log_error("get_hashes_error", f"Error getting S3 hashes", error=str(e), fixture_id=fixture_id, event_id=event_id)
            return {}
    
    def get_bucket_stats(self) -> Dict[str, Any]:
        """Get bucket statistics"""
        try:
            response = self.s3_client.list_objects_v2(Bucket=self.bucket_name)
            
            total_objects = response.get('KeyCount', 0)
            total_size = sum(obj['Size'] for obj in response.get('Contents', []))
            
            return {
                "bucket_name": self.bucket_name,
                "total_videos": total_objects,
                "total_size_bytes": total_size,
                "total_size_mb": round(total_size / (1024 * 1024), 2),
                "endpoint": self.endpoint_url
            }
        
        except Exception as e:
            return {"error": f"Could not get stats: {e}"}
    
    def get_video_tags(self, s3_key: str) -> Dict[str, str]:
        """Get tags for a specific video.
        
        Returns:
            Dict of tag key-value pairs
        """
        try:
            response = self.s3_client.get_object_tagging(
                Bucket=self.bucket_name,
                Key=s3_key
            )
            tags = {tag['Key']: tag['Value'] for tag in response.get('TagSet', [])}
            return tags
        except Exception as e:
            _log_error("get_tags_error", f"Error getting tags", error=str(e), s3_key=s3_key)
            return {}
    
    def list_videos_by_fixture(self, fixture_id: int) -> list:
        """List all videos for a fixture.
        
        Returns:
            List of video info dicts with keys, sizes, tags
        """
        try:
            prefix = f"{fixture_id}/"
            response = self.s3_client.list_objects_v2(
                Bucket=self.bucket_name,
                Prefix=prefix
            )
            
            videos = []
            for obj in response.get('Contents', []):
                video_info = {
                    'key': obj['Key'],
                    'size': obj['Size'],
                    'last_modified': obj['LastModified'].isoformat(),
                    'url': f"{self.endpoint_url}/{self.bucket_name}/{obj['Key']}"
                }
                # Optionally fetch tags (can be slow for many objects)
                # tags = self.get_video_tags(obj['Key'])
                # video_info['tags'] = tags
                videos.append(video_info)
            
            return videos
        except Exception as e:
            _log_error("list_videos_by_fixture_error", f"Error listing videos", error=str(e), fixture_id=fixture_id)
            return []
    
    def tag_fixture_directory(self, fixture_id: int, home_team: str, away_team: str) -> bool:
        """Tag all videos in a fixture directory with fixture-level metadata.
        
        This is a one-time operation to add fixture context to existing videos.
        New videos should be tagged during upload.
        
        Args:
            fixture_id: Fixture ID
            home_team: Home team name
            away_team: Away team name
        
        Returns:
            True if successful
        """
        try:
            videos = self.list_videos_by_fixture(fixture_id)
            
            for video in videos:
                s3_key = video['key']
                
                # Get existing tags
                existing_tags = self.get_video_tags(s3_key)
                
                # Add fixture-level tags
                existing_tags['home_team'] = home_team.replace(" ", "_")[:50]
                existing_tags['away_team'] = away_team.replace(" ", "_")[:50]
                
                # Update tags
                tag_set = [{'Key': k, 'Value': v} for k, v in existing_tags.items()]
                self.s3_client.put_object_tagging(
                    Bucket=self.bucket_name,
                    Key=s3_key,
                    Tagging={'TagSet': tag_set}
                )
            
            _log_info("fixture_tagged", f"Tagged {len(videos)} videos in fixture {fixture_id}", fixture_id=fixture_id, video_count=len(videos))
            return True
        except Exception as e:
            _log_error("tag_fixture_error", f"Error tagging fixture directory", error=str(e), fixture_id=fixture_id)
            return False
    
    def delete_video(self, s3_key: str) -> bool:
        """Delete a video from S3"""
        try:
            self.s3_client.delete_object(Bucket=self.bucket_name, Key=s3_key)
            _log_info("video_deleted", f"Deleted: {s3_key}", s3_key=s3_key)
            return True
        except Exception as e:
            _log_error("delete_failed", f"Delete failed", error=str(e), s3_key=s3_key)
            return False
