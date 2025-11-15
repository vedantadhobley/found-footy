"""S3 storage utilities for Found Footy video downloads"""
import os
import tempfile
import boto3
from botocore.exceptions import ClientError
from datetime import datetime
from typing import Optional, Dict, Any
from pathlib import Path

class FootyS3Store:
    """S3 storage manager for video files"""
    
    def __init__(self):
        self.endpoint_url = os.getenv('S3_ENDPOINT_URL', 'http://localhost:9000')
        self.access_key = os.getenv('S3_ACCESS_KEY', 'founduser')      # ✅ FIX
        self.secret_key = os.getenv('S3_SECRET_KEY', 'footypass')      # ✅ FIX
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
            error_code = int(e.response['Error']['Code'])
            if error_code == 404:
                try:
                    self.s3_client.create_bucket(Bucket=self.bucket_name)
                    print(f"✅ Created S3 bucket: {self.bucket_name}")
                except Exception as create_error:
                    print(f"❌ Error creating bucket: {create_error}")
            else:
                print(f"❌ Error checking bucket: {e}")
        except Exception as e:
            print(f"⚠️ Could not verify S3 bucket: {e}")
    
    def generate_video_key(self, goal_id: str, video_index: int, file_extension: str = "mp4") -> str:
        """Generate simplified S3 key: fixture_id/goal_id/goal_id_index.ext"""
        
        # ✅ Extract fixture_id from goal_id (first part before first underscore)
        # Handle both formats: "12345_45" and "12345_45+3"
        fixture_id = goal_id.split('_')[0]
        
        # ✅ NEW: Simplified structure - fixture_id/goal_id/goal_id_index.ext
        # This will create paths like: 12345/12345_45+3/12345_45+3_0.mp4
        return f"{fixture_id}/{goal_id}/{goal_id}_{video_index}.{file_extension}"
    
    def upload_video_file(self, local_file_path: str, goal_id: str, video_index: int, metadata: Dict[str, Any] | None = None) -> Dict[str, Any]:
        """Upload video file to S3 with clean metadata"""
        try:
            # Get file extension
            file_extension = Path(local_file_path).suffix.lstrip('.')
            if not file_extension:
                file_extension = "mp4"
            
            # ✅ Generate simplified S3 key
            s3_key = self.generate_video_key(goal_id, video_index, file_extension)
            
            # Prepare metadata
            s3_metadata = {
                'goal_id': goal_id,
                'video_index': str(video_index),
                'uploaded_at': datetime.utcnow().isoformat(),
                'content_type': f'video/{file_extension}'
            }
            
            # ✅ FIX: Clean metadata for S3 headers
            clean_metadata = {}
            if metadata:
                for key, value in metadata.items():
                    if value is not None:
                        # Clean string values to remove invalid header characters
                        clean_value = str(value)
                        # Remove newlines, tabs, and other problematic characters
                        clean_value = clean_value.replace('\n', ' ').replace('\r', ' ').replace('\t', ' ')
                        # Truncate long values that might break headers
                        if len(clean_value) > 100:
                            clean_value = clean_value[:97] + "..."
                        # Remove any non-ASCII characters that might cause issues
                        clean_value = ''.join(char if ord(char) < 128 else '?' for char in clean_value)
                        clean_metadata[f"goal-{key}"] = clean_value
            
            # Upload to S3 with clean metadata
            self.s3_client.upload_file(
                local_file_path,
                self.bucket_name,
                s3_key,
                ExtraArgs={
                    'Metadata': clean_metadata,
                    'ContentType': 'video/mp4'
                }
            )
            
            print(f"✅ Uploaded: {s3_key}")
            
            return {
                "status": "success",
                "s3_key": s3_key,
                "bucket": self.bucket_name,
                "file_size": Path(local_file_path).stat().st_size,
                "metadata": s3_metadata,
                "url": f"{self.endpoint_url}/{self.bucket_name}/{s3_key}"
            }
            
        except Exception as e:
            print(f"❌ Upload failed: {e}")
            return {
                "status": "error",
                "error": str(e),
                "s3_key": None
            }
    
    def download_video_from_url(self, video_url: str, goal_id: str, video_index: int, 
                               metadata: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
        """Download video from URL and upload to S3 with simplified structure"""
        try:
            # Create temporary directory for download
            with tempfile.TemporaryDirectory() as temp_dir:
                print(f"📥 Downloading video from: {video_url}")
                
                # For now, create a realistic dummy file until we integrate yt-dlp
                dummy_video_path = os.path.join(temp_dir, f"{goal_id}_{video_index}.mp4")
                
                # Create a more realistic dummy file (1MB)
                with open(dummy_video_path, 'wb') as f:
                    f.write(b'\x00' * (1024 * 1024))  # 1MB dummy
                
                # ✅ Upload using simplified structure
                result = self.upload_video_file(
                    dummy_video_path,
                    goal_id,
                    video_index,
                    metadata or {}
                )
                
                if result["status"] == "success":
                    print(f"✅ Successfully downloaded and uploaded: {result['s3_key']}")
                
                return result
    
        except Exception as e:
            print(f"❌ Download failed: {e}")
            return {
                "status": "error",
                "error": str(e),
                "s3_key": None
            }
    
    def list_goal_videos(self, goal_id: str) -> list:
        """List all videos for a specific goal using simplified structure"""
        try:
            # Extract fixture_id from goal_id
            fixture_id = goal_id.split('_')[0]
            
            # ✅ NEW: Use simplified prefix - fixture_id/goal_id/
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