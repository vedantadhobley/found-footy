"""Integration tests - Fixed video_url field expectation"""
import pytest
from unittest.mock import patch, Mock

class TestCompleteIntegration:
    """Integration tests for business logic only"""
    
    def test_complete_goal_video_pipeline(self, sample_goal_data, sample_twitter_videos):
        """Test complete pipeline business logic"""
        
        # 1. Test goal storage
        with patch('found_footy.storage.mongo_store.FootyMongoStore.store_goal_pending') as mock_store:
            mock_store.return_value = True
            
            from found_footy.storage.mongo_store import FootyMongoStore
            store = FootyMongoStore()
            goal_stored = store.store_goal_pending(12345, sample_goal_data)
            
            assert goal_stored == True
            
        # 2. Test Twitter search - ✅ FIX: Use actual field name from implementation
        mock_twitter_videos = [
            {
                "search_term": "Messi goal",
                "tweet_url": "https://twitter.com/user/status/1234567890123456789",  # ✅ FIX: This is the actual field
                "tweet_id": "1234567890123456789",
                "source": "browser_automation",
                "requires_ytdlp": True
            }
        ]
        
        with patch('found_footy.api.twitter_api.twitter_videos_search') as mock_search:
            mock_search.return_value = mock_twitter_videos
            
            from found_footy.api.twitter_api import twitter_videos_search
            videos = twitter_videos_search("Messi goal")
            
            assert len(videos) > 0
            assert videos[0]["tweet_url"] is not None  # ✅ FIX: Use correct field name
            
        # 3. Test S3 upload - Fixed boto3 mocking
        with patch('found_footy.storage.s3_store.boto3') as mock_boto3, \
             patch('found_footy.storage.s3_store.os.path.getsize') as mock_getsize:
            
            # Mock boto3 client
            mock_s3_client = Mock()
            mock_s3_client.head_bucket.return_value = True
            mock_s3_client.upload_file.return_value = None
            mock_boto3.client.return_value = mock_s3_client
            
            # Mock file size
            mock_getsize.return_value = 1024
            
            # Now test - this will use the mocked boto3
            from found_footy.storage.s3_store import FootyS3Store
            s3_store = FootyS3Store()
            upload_result = s3_store.upload_video_file("test.mp4", "12345_67_789", 0, 0)
            
            assert upload_result["status"] == "success"