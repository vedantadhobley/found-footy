"""S3 storage utilities for Found Footy video downloads"""
import os
import tempfile
import boto3
from botocore.exceptions import ClientError, NoCredentialsError
from datetime import datetime
from typing import Optional, Dict, Any
from pathlib import Path


class FootyS3Store:
    """S3 storage manager for video files"""
    
    def __init__(self):
        self.endpoint_url = os.getenv('S3_ENDPOINT_URL', 'http://found-footy-minio:9000')
        self.access_key = os.getenv('S3_ACCESS_KEY', 'ffuser')
        self.secret_key = os.getenv('S3_SECRET_KEY', 'ffpass--')
        self.bucket_name = os.getenv('S3_BUCKET_NAME', 'footy-videos')
        
        # Initialize S3 client
        self.s3_client = boto3.client(
            's3',
            endpoint_url=self.endpoint_url,
            aws_access_key_id=self.access_key,
            aws_secret_access_key=self.secret_key,
            region_name='us-east-1'  # MinIO default
        )
        
        self._ensure_bucket_exists()
    
    def _ensure_bucket_exists(self):
        """Create bucket if it doesn't exist - REQUIRED for all operations"""
        try:
            self.s3_client.head_bucket(Bucket=self.bucket_name)
            print(f"✅ S3 bucket '{self.bucket_name}' exists")
        except ClientError as e:
            error_code = e.response.get('Error', {}).get('Code')
            # Handle both 404 (numeric) and 'NoSuchBucket' (string) error codes
            if error_code == 404 or error_code == '404' or 'NoSuchBucket' in str(e):
                print(f"📦 Bucket '{self.bucket_name}' doesn't exist, creating...")
                try:
                    self.s3_client.create_bucket(Bucket=self.bucket_name)
                    print(f"✅ Created S3 bucket: {self.bucket_name}")
                except ClientError as create_error:
                    # Check if bucket was created by another process (race condition)
                    if 'BucketAlreadyOwnedByYou' in str(create_error):
                        print(f"✅ Bucket {self.bucket_name} already exists (created by another process)")
                    else:
                        print(f"❌ FATAL: Failed to create bucket: {create_error}")
                        raise  # Re-raise - this is a fatal error
            else:
                print(f"❌ FATAL: Error checking bucket: {e}")
                raise  # Re-raise - this is a fatal error
        except NoCredentialsError as e:
            print(f"❌ FATAL: S3 credentials not configured: {e}")
            raise
        except Exception as e:
            print(f"❌ FATAL: Unexpected S3 error: {e}")
            raise
    
    def upload_video(self, local_file_path: str, s3_key: str, metadata: Dict[str, Any] = None, 
                    tags: Dict[str, str] = None) -> Optional[str]:
        """Upload video file to S3 with metadata and tags
        
        Args:
            local_file_path: Path to local video file
            s3_key: S3 key (path) for the file
            metadata: Optional metadata to attach to object headers
            tags: Optional tags for S3 object tagging (better for filtering/search)
            
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
            
            # Add tags if provided (separate from metadata, better for filtering)
            if tags:
                # Build tag string: "key1=value1&key2=value2"
                tag_string = '&'.join([f"{k}={v}" for k, v in tags.items()])
                extra_args['Tagging'] = tag_string
            
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
                    print(f"⚠️ Bucket disappeared, recreating and retrying upload...")
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
            
            s3_url = f"{self.endpoint_url}/{self.bucket_name}/{s3_key}"
            print(f"✅ Uploaded: {s3_key}")
            return s3_url
            
        except Exception as e:
            print(f"❌ FATAL: Upload failed for {s3_key}: {e}")
            import traceback
            traceback.print_exc()
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
            print(f"❌ Upload failed: {e}")
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
            print(f"❌ Error listing videos: {e}")
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
            print(f"❌ Error getting S3 hashes: {e}")
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
            print(f"❌ Error getting tags: {e}")
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
            print(f"❌ Error listing videos: {e}")
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
            
            print(f"✅ Tagged {len(videos)} videos in fixture {fixture_id}")
            return True
        except Exception as e:
            print(f"❌ Error tagging fixture directory: {e}")
            return False
    
    def delete_video(self, s3_key: str) -> bool:
        """Delete a video from S3"""
        try:
            self.s3_client.delete_object(Bucket=self.bucket_name, Key=s3_key)
            print(f"🗑️ Deleted: {s3_key}")
            return True
        except Exception as e:
            print(f"❌ Delete failed: {e}")
            return False
