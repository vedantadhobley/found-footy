"""Tests for download flow - Updated for .fn() approach"""
import pytest
import sys
from unittest.mock import patch, Mock, MagicMock

@pytest.fixture(autouse=True)
def clear_download_flow_modules():
    """Clear download flow modules before each test"""
    modules_to_clear = [
        'found_footy.flows.download_flow',
        'found_footy.storage.mongo_store',
        'found_footy.storage.s3_store'
    ]
    
    for module in modules_to_clear:
        if module in sys.modules:
            del sys.modules[module]
    
    yield
    
    for module in modules_to_clear:
        if module in sys.modules:
            del sys.modules[module]

class TestDownloadFlowWithFn:
    """Test download flow using .fn() approach"""
    
    def test_download_flow_fn_successful_download(self, clear_download_flow_modules):
        """Test download_flow.fn() with successful yt-dlp download and S3 upload"""
        
        with patch('found_footy.flows.download_flow.FootyMongoStore') as MockStore, \
             patch('found_footy.flows.download_flow.FootyS3Store') as MockS3, \
             patch('found_footy.flows.download_flow.yt_dlp.YoutubeDL') as MockYtdl, \
             patch('found_footy.flows.download_flow.tempfile.TemporaryDirectory') as MockTemp, \
             patch('found_footy.flows.download_flow.os.listdir') as mock_listdir:
            
            # Setup mocks for actual implementation
            mock_store = Mock()
            mock_store.goals_pending.find_one.return_value = {
                "_id": "12345_67",
                "discovered_videos": [
                    {
                        "tweet_url": "https://twitter.com/user/status/123",
                        "search_index": 0,
                        "video_index": 0,
                        "tweet_id": "123"
                    }
                ]
            }
            mock_store.goals_processed.replace_one.return_value = Mock()
            mock_store.goals_pending.delete_one.return_value = Mock()
            MockStore.return_value = mock_store
            
            # Mock S3 store
            mock_s3 = Mock()
            mock_s3.upload_video_file.return_value = {
                "status": "success",
                "s3_key": "test.mp4",
                "s3_url": "http://s3/test.mp4",
                "file_size": 1024
            }
            mock_s3.bucket_name = "footy-videos"
            MockS3.return_value = mock_s3
            
            # Mock yt-dlp
            mock_ytdl_instance = Mock()
            mock_ytdl_instance.extract_info.return_value = {
                "width": 1280, "height": 720, "duration": 45, "ext": "mp4"
            }
            MockYtdl.return_value.__enter__.return_value = mock_ytdl_instance
            MockYtdl.return_value.__exit__.return_value = None
            
            # Mock file operations
            MockTemp.return_value.__enter__.return_value = "/tmp/test"
            MockTemp.return_value.__exit__.return_value = None
            mock_listdir.return_value = ["12345_67.mp4"]
            
            # ✅ USE .fn() TO TEST ACTUAL BUSINESS LOGIC
            from found_footy.flows.download_flow import download_flow
            result = download_flow.fn(goal_id="12345_67")
            
            # Test actual flow return structure
            assert result["status"] == "completed"
            assert result["storage_backend"] == "s3"
            assert result["download_method"] == "yt-dlp_python"
            
            # Verify actual yt-dlp logic was called
            mock_ytdl_instance.extract_info.assert_called()
            mock_s3.upload_video_file.assert_called()
    
    def test_download_videos_task_fn_business_logic(self, clear_download_flow_modules):
        """Test download_videos_task.fn() business logic directly"""
        
        with patch('found_footy.flows.download_flow.FootyMongoStore') as MockStore, \
             patch('found_footy.flows.download_flow.FootyS3Store') as MockS3:
            
            mock_store = Mock()
            mock_store.goals_pending.find_one.return_value = None  # Goal not found
            MockStore.return_value = mock_store
            
            # ✅ USE .fn() TO TEST ACTUAL TASK LOGIC
            from found_footy.flows.download_flow import download_videos_task
            result = download_videos_task.fn("nonexistent_goal")
            
            assert result["status"] == "not_found"
            assert result["goal_id"] == "nonexistent_goal"