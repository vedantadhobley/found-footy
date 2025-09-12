"""Debug utility for S3 storage"""
import sys
import os
sys.path.append('/app')

from found_footy.storage.s3_store import FootyS3Store  # ✅ FIX: Correct import

def test_s3_connection():
    """Test S3 connection and bucket operations"""
    print("🧪 Testing S3 Connection")
    print("=" * 50)
    
    try:
        s3 = FootyS3Store()  # ✅ FIX: Use correct class name
        
        # Test bucket stats
        stats = s3.get_bucket_stats()
        print(f"📊 Bucket Stats: {stats}")
        
        # Test dummy upload
        import tempfile
        with tempfile.NamedTemporaryFile(suffix='.mp4', delete=False) as tmp:
            tmp.write(b'test video content')
            tmp_path = tmp.name
        
        try:
            result = s3.upload_video_file(tmp_path, "test_123_45_67", 1, {"test": "true"})
            print(f"📤 Upload Test: {result}")
            
            # List videos
            videos = s3.list_goal_videos("test_123_45_67")
            print(f"📋 Listed Videos: {videos}")
            
        finally:
            os.unlink(tmp_path)
        
        print("✅ S3 tests completed successfully!")
        
    except Exception as e:
        print(f"❌ S3 test failed: {e}")

if __name__ == "__main__":
    test_s3_connection()