"""S3 storage utilities for Found Footy video downloads"""
import os
import boto3
import tempfile
from botocore.exceptions import ClientError, NoCredentialsError
from datetime import datetime
from typing import Optional, Dict, Any
from pathlib import Path

class FootyS3Store:
    """S3 storage manager for video files"""
    
    def __init__(self):
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
            print(f"✅ S3 bucket '{self.bucket_name}' exists")
        except ClientError as e:
            error_code = int(e.response['Error']['Code'])
            if error_code == 404:
                try:
                    self.s3_client.create_bucket(Bucket=self.bucket_name)
                    print(f"✅ Created S3 bucket: {self.bucket_name}")
                except Exception as create_error:
                    print(f"❌ Failed to create bucket: {create_error}")
            else:
                print(f"❌ Error checking bucket: {e}")
        except Exception as e:
            print(f"⚠️ Could not verify S3 bucket: {e}")
    
    def generate_video_key(self, goal_id: str, search_index: int, file_extension: str = "mp4") -> str:
        """Generate S3 key for video file"""
        # Parse goal_id for better organization
        parts = goal_id.split('_')
        if len(parts) >= 3:
            fixture_id, minute, player_id = parts[:3]
            # Organize by fixture, then by goal
            return f"fixtures/{fixture_id}/goals/{goal_id}_{search_index}.{file_extension}"
        else:
            # Fallback structure
            return f"goals/{goal_id}_{search_index}.{file_extension}"
    
    def upload_video_file(self, local_file_path: str, goal_id: str, search_index: int, 
                         metadata: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
        """Upload video file to S3 with metadata"""
        try:
            # Get file extension
            file_extension = Path(local_file_path).suffix.lstrip('.')
            if not file_extension:
                file_extension = "mp4"
            
            # Generate S3 key
            s3_key = self.generate_video_key(goal_id, search_index, file_extension)
            
            # Prepare metadata
            s3_metadata = {
                'goal_id': goal_id,
                'search_index': str(search_index),
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
            
            print(f"✅ Uploaded video to S3: {s3_key}")
            
            return {
                "status": "success",
                "s3_key": s3_key,
                "s3_url": s3_url,
                "bucket": self.bucket_name,
                "file_size": os.path.getsize(local_file_path),
                "metadata": s3_metadata
            }
            
        except FileNotFoundError:
            return {"status": "error", "error": f"Local file not found: {local_file_path}"}
        except NoCredentialsError:
            return {"status": "error", "error": "S3 credentials not configured"}
        except ClientError as e:
            return {"status": "error", "error": f"S3 client error: {e}"}
        except Exception as e:
            return {"status": "error", "error": f"Upload failed: {e}"}
    
    def download_with_ytdlp(self, search_term: str, goal_id: str, search_index: int) -> Dict[str, Any]:
        """Download video using yt-dlp and upload to S3"""
        try:
            # Create temporary directory for download
            with tempfile.TemporaryDirectory() as temp_dir:
                output_template = os.path.join(temp_dir, f"video_{search_index}.%(ext)s")
                
                # yt-dlp configuration for Twitter
                ydl_opts = {
                    'outtmpl': output_template,
                    'format': 'best[height<=720]',  # Limit to 720p for storage efficiency
                    'noplaylist': True,
                    'extract_flat': False,
                }
                
                # This is a simulation - in real implementation you'd search Twitter first
                # then extract video URLs and download them
                print(f"🔍 Simulating Twitter search for: '{search_term}'")
                
                # Simulate finding a video URL (replace with actual Twitter search)
                # video_url = "https://twitter.com/some_tweet_with_video"
                
                # For now, create a dummy file to demonstrate S3 upload
                dummy_video_path = os.path.join(temp_dir, f"goal_{goal_id}_{search_index}.mp4")
                
                # Create a small dummy video file (replace with actual yt-dlp download)
                with open(dummy_video_path, 'wb') as f:
                    f.write(b'dummy video content for testing')
                
                # Upload to S3
                upload_result = self.upload_video_file(
                    dummy_video_path, 
                    goal_id, 
                    search_index,
                    metadata={
                        'search_term': search_term,
                        'source': 'twitter',
                        'download_method': 'yt-dlp'
                    }
                )
                
                if upload_result["status"] == "success":
                    return {
                        "status": "success",
                        "s3_info": upload_result,
                        "download_method": "yt-dlp_simulation"
                    }
                else:
                    return {
                        "status": "error",
                        "error": f"S3 upload failed: {upload_result.get('error')}"
                    }
        
        except ImportError:
            return {"status": "error", "error": "yt-dlp not installed"}
        except Exception as e:
            return {"status": "error", "error": f"Download failed: {e}"}
    
    def list_goal_videos(self, goal_id: str) -> list:
        """List all videos for a specific goal"""
        try:
            # Parse goal_id for prefix
            parts = goal_id.split('_')
            if len(parts) >= 3:
                fixture_id = parts[0]
                prefix = f"fixtures/{fixture_id}/goals/{goal_id}"
            else:
                prefix = f"goals/{goal_id}"
            
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
    
    def download_video_from_url(self, video_url: str, goal_id: str, file_suffix: str, 
                               metadata: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
        """Download video from URL using yt-dlp and upload to S3"""
        try:
            # Create temporary directory for download
            with tempfile.TemporaryDirectory() as temp_dir:
                output_template = os.path.join(temp_dir, f"video_{file_suffix}.%(ext)s")
                
                # yt-dlp configuration
                ydl_opts = {
                    'outtmpl': output_template,
                    'format': 'best[height<=720]',
                    'noplaylist': True,
                    'extract_flat': False,
                    'quiet': True,
                }
                
                print(f"📥 Downloading video from: {video_url}")
                
                # For simulation - create dummy file (replace with actual yt-dlp)
                dummy_video_path = os.path.join(temp_dir, f"goal_{goal_id}_{file_suffix}.mp4")
                
                # Simulate video download
                # In real implementation, use:
                # with yt_dlp.YoutubeDL(ydl_opts) as ydl:
                #     ydl.download([video_url])
                #     downloaded_files = glob.glob(os.path.join(temp_dir, "video_*"))
                #     if downloaded_files:
                #         dummy_video_path = downloaded_files[0]
                
                # Create dummy video content for testing
                with open(dummy_video_path, 'wb') as f:
                    f.write(b'simulated video content from URL download')
                
                # Upload to S3
                upload_result = self.upload_video_file(
                    dummy_video_path, 
                    goal_id, 
                    file_suffix,
                    metadata={
                        **(metadata or {}),
                        'source_url': video_url,
                        'download_method': 'yt-dlp'
                    }
                )
                
                if upload_result["status"] == "success":
                    return {
                        "status": "success",
                        "s3_info": upload_result,
                        "download_method": "yt-dlp_simulation",
                        "source_url": video_url
                    }
                else:
                    return {
                        "status": "error",
                        "error": f"S3 upload failed: {upload_result.get('error')}"
                    }
        
        except ImportError:
            return {"status": "error", "error": "yt-dlp not installed"}
        except Exception as e:
            return {"status": "error", "error": f"Download failed: {e}"}