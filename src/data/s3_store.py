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
        """Create bucket if it doesn't exist"""
        try:
            self.s3_client.head_bucket(Bucket=self.bucket_name)
            print(f"✅ S3 bucket '{self.bucket_name}' exists")
        except ClientError as e:
            error_code = e.response.get('Error', {}).get('Code', '')
            if error_code in ('404', 'NoSuchBucket'):
                try:
                    self.s3_client.create_bucket(Bucket=self.bucket_name)
                    print(f"✅ Created S3 bucket: {self.bucket_name}")
                except Exception as create_error:
                    print(f"❌ Error creating bucket: {create_error}")
            else:
                print(f"❌ Error checking bucket: {e}")
        except Exception as e:
            print(f"⚠️ Could not verify S3 bucket: {e}")
    
    def upload_video(self, local_file_path: str, s3_key: str, metadata: Dict[str, Any] = None) -> Optional[str]:
        """Upload video file to S3
        
        Args:
            local_file_path: Path to local video file
            s3_key: S3 key (path) for the file
            metadata: Optional metadata to attach to the object
            
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
            
            # Upload
            self.s3_client.upload_file(
                local_file_path,
                self.bucket_name,
                s3_key,
                ExtraArgs={
                    'Metadata': clean_metadata,
                    'ContentType': content_type
                }
            )
            
            s3_url = f"{self.endpoint_url}/{self.bucket_name}/{s3_key}"
            print(f"✅ Uploaded: {s3_key}")
            return s3_url
            
        except Exception as e:
            print(f"❌ Upload failed: {e}")
            return None
    
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
    
    def delete_video(self, s3_key: str) -> bool:
        """Delete a video from S3"""
        try:
            self.s3_client.delete_object(Bucket=self.bucket_name, Key=s3_key)
            print(f"🗑️ Deleted: {s3_key}")
            return True
        except Exception as e:
            print(f"❌ Delete failed: {e}")
            return False
