"""S3 storage utilities for Found Footy video downloads"""
import os
import time
import tempfile
import requests
import boto3
from botocore.exceptions import ClientError, NoCredentialsError
from datetime import datetime
from typing import Optional, Dict, Any
from pathlib import Path

# ✅ FIXED: Use proper logging
from found_footy.utils.logging import get_logger, log_error_with_trace

class FootyS3Store:
    """S3 storage manager for video files"""
    
    def __init__(self):
        self.logger = get_logger(self.__class__.__name__)
        
        self.endpoint_url = os.getenv('S3_ENDPOINT_URL', 'http://localhost:9000')
        self.access_key = os.getenv('S3_ACCESS_KEY', 'footy_admin')
        self.secret_key = os.getenv('S3_SECRET_KEY', 'footy_secure_pass')
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
            self.logger.info(f"✅ S3 bucket '{self.bucket_name}' exists")
        except ClientError as e:
            error_code = int(e.response['Error']['Code'])
            if error_code == 404:
                try:
                    self.s3_client.create_bucket(Bucket=self.bucket_name)
                    self.logger.info(f"✅ Created S3 bucket: {self.bucket_name}")
                except Exception as create_error:
                    log_error_with_trace(self.logger, "❌ Error creating bucket", create_error)
            else:
                log_error_with_trace(self.logger, "❌ Error checking bucket", e)
        except Exception as e:
            log_error_with_trace(self.logger, "⚠️ Could not verify S3 bucket", e)
    
    def generate_video_key(self, goal_id: str, search_index: int, video_index: int, file_extension: str = "mp4") -> str:
        """Generate S3 key for video file - HANDLES NEW FORMAT"""
        
        # ✅ NEW: Parse goal_id correctly for new format
        # Format: "fixture_id_minute" or "fixture_id_minute+extra"
        if '+' in goal_id:
            # Handle extra time: "959546_90+2"
            main_part, extra_part = goal_id.split('+')
            fixture_id = main_part.split('_')[0]
        else:
            # Handle regular time: "959546_44"
            parts = goal_id.split('_')
            fixture_id = parts[0] if len(parts) >= 2 else "unknown"
        
        # ✅ SIMPLIFIED: fixture_id/goal_id_search_video.extension  
        return f"{fixture_id}/{goal_id}_{search_index}_{video_index}.{file_extension}"
    
    def upload_video_file(self, local_file_path: str, goal_id: str, search_index: int, video_index: int,
                         metadata: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
        """Upload video file to S3 with metadata"""
        try:
            # Get file extension
            file_extension = Path(local_file_path).suffix.lstrip('.')
            if not file_extension:
                file_extension = "mp4"
            
            # Generate S3 key
            s3_key = self.generate_video_key(goal_id, search_index, video_index, file_extension)
            
            # Prepare metadata
            s3_metadata = {
                'goal_id': goal_id,
                'search_index': str(search_index),
                'video_index': str(video_index),
                'uploaded_at': datetime.utcnow().isoformat(),
                'content_type': f'video/{file_extension}'
            }
            
            if metadata:
                s3_metadata.update({k: str(v) for k, v in metadata.items()})
            
            # Upload file
            self.s3_client.upload_file(
                local_file_path,
                self.bucket_name,
                s3_key,
                ExtraArgs={
                    'Metadata': s3_metadata,
                    'ContentType': f'video/{file_extension}'
                }
            )
            
            # Generate URL
            s3_url = f"{self.endpoint_url}/{self.bucket_name}/{s3_key}"
            
            self.logger.info(f"✅ Uploaded video to S3: {s3_key}")
            
            return {
                "status": "success",
                "s3_key": s3_key,
                "s3_url": s3_url,
                "bucket": self.bucket_name,
                "file_size": os.path.getsize(local_file_path),
                "metadata": s3_metadata
            }
            
        except FileNotFoundError:
            error_msg = f"Local file not found: {local_file_path}"
            self.logger.error(f"❌ {error_msg}")
            return {"status": "error", "error": error_msg}
        except NoCredentialsError:
            error_msg = "S3 credentials not configured"
            self.logger.error(f"❌ {error_msg}")
            return {"status": "error", "error": error_msg}
        except ClientError as e:
            log_error_with_trace(self.logger, "❌ S3 client error", e)
            return {"status": "error", "error": f"S3 client error: {e}"}
        except Exception as e:
            log_error_with_trace(self.logger, "❌ Upload failed", e)
            return {"status": "error", "error": f"Upload failed: {e}"}
    
    def download_video_from_url(self, video_url: str, goal_id: str, file_suffix: str, 
                               metadata: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
        """Download video from URL and upload to S3"""
        try:
            # Parse file suffix to get search_index and video_index
            parts = file_suffix.split('_')
            search_index = int(parts[0]) if len(parts) > 0 else 0
            video_index = int(parts[1]) if len(parts) > 1 else 0
            
            # Create temporary directory for download
            with tempfile.TemporaryDirectory() as temp_dir:
                self.logger.info(f"📥 Downloading video from: {video_url}")
                
                # For now, create a realistic dummy file until we integrate yt-dlp
                dummy_video_path = os.path.join(temp_dir, f"goal_{goal_id}_{file_suffix}.mp4")
                
                # Create a more realistic dummy file (1MB)
                with open(dummy_video_path, 'wb') as f:
                    # Write 1MB of data to simulate a video file
                    f.write(b'\x00' * (1024 * 1024))
                
                # Upload to S3
                upload_result = self.upload_video_file(
                    dummy_video_path, 
                    goal_id, 
                    search_index,
                    video_index,
                    metadata={
                        'source_url': video_url,
                        'download_method': 'simulation',
                        **(metadata or {})
                    }
                )
                
                if upload_result["status"] == "success":
                    self.logger.info(f"✅ Downloaded and uploaded: {upload_result['s3_key']}")
                    return upload_result
                else:
                    return upload_result
        
        except Exception as e:
            log_error_with_trace(self.logger, "❌ Download failed", e)
            return {"status": "error", "error": f"Download failed: {e}"}
    
    def list_goal_videos(self, goal_id: str) -> list:
        """List all videos for a specific goal - SIMPLIFIED"""
        try:
            # Parse goal_id for fixture prefix
            parts = goal_id.split('_')
            if len(parts) >= 3:
                fixture_id = parts[0]
                # ✅ SIMPLIFIED: Look in fixture folder for goal files
                prefix = f"{fixture_id}/{goal_id}_"
            else:
                prefix = f"unknown/{goal_id}_"
            
            response = self.s3_client.list_objects_v2(
                Bucket=self.bucket_name,
                Prefix=prefix
            )
            
            videos = []
            for obj in response.get('Contents', []):
                video_info = {
                    "s3_key": obj['Key'],
                    "size": obj['Size'],
                    "last_modified": obj['LastModified'],
                    "s3_url": f"{self.endpoint_url}/{self.bucket_name}/{obj['Key']}"
                }
                videos.append(video_info)
            
            return videos
        
        except Exception as e:
            log_error_with_trace(self.logger, "❌ Error listing videos", e)
            return []
    
    def get_fixture_videos(self, fixture_id: int) -> list:
        """Get ALL videos for a fixture - NEW CONVENIENCE METHOD"""
        try:
            # ✅ SIMPLIFIED: All videos in fixture folder
            prefix = f"{fixture_id}/"
            
            response = self.s3_client.list_objects_v2(
                Bucket=self.bucket_name,
                Prefix=prefix
            )
            
            videos = []
            for obj in response.get('Contents', []):
                # Extract goal_id from filename
                filename = obj['Key'].split('/')[-1]  # Get just the filename
                goal_id = '_'.join(filename.split('_')[:3])  # fixture_minute_player
                
                video_info = {
                    "s3_key": obj['Key'],
                    "goal_id": goal_id,
                    "fixture_id": fixture_id,
                    "filename": filename,
                    "size": obj['Size'],
                    "last_modified": obj['LastModified'],
                    "s3_url": f"{self.endpoint_url}/{self.bucket_name}/{obj['Key']}"
                }
                videos.append(video_info)
            
            return videos
        
        except Exception as e:
            log_error_with_trace(self.logger, "❌ Error listing fixture videos", e)
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
            log_error_with_trace(self.logger, "❌ Could not get stats", e)
            return {"error": f"Could not get stats: {e}"}