"""Tests for download flow"""
import pytest
import sys
from unittest.mock import patch, Mock

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

class TestDownloadFlow:
    """Test suite for download flow"""
    
    def test_download_flow_successful_download(self, clear_download_flow_modules):
        """Test successful video download and S3 upload"""
        with patch('found_footy.flows.download_flow.store') as mock_store, \
             patch('found_footy.flows.download_flow.s3_store') as mock_s3_store:
            
            # Setup goal document with videos
            goal_doc = {
                "_id": "12345_67_789",
                "team_name": "Barcelona",
                "player_name": "Lionel Messi",
                "minute": 67,
                "fixture_id": 12345,
                "discovered_videos": [
                    {
                        "video_url": "https://video.twimg.com/test.mp4",
                        "search_term": "Messi Barcelona",
                        "search_index": 0,
                        "video_index": 0
                    }
                ]
            }
            mock_store.goals_pending.find_one.return_value = goal_doc
            mock_store.goals_processed.replace_one.return_value = Mock()
            mock_store.goals_pending.delete_one.return_value = Mock()
            
            # Setup S3 mock
            mock_s3_store.download_video_from_url.return_value = {
                "status": "success",
                "s3_key": "fixtures/12345/goals/12345_67_789_0_0.mp4",
                "s3_url": "http://localhost:9000/footy-videos/test.mp4",
                "bucket": "footy-videos",
                "file_size": 1024,
                "metadata": {}
            }
            mock_s3_store.bucket_name = "footy-videos"
            mock_s3_store.get_bucket_stats.return_value = {"total_videos": 10, "total_size_mb": 50.0}
            
            from found_footy.flows.download_flow import download_flow
            
            result = download_flow(goal_id="12345_67_789")
            
            assert result["status"] == "completed"
            assert result["storage_backend"] == "s3"
            
            mock_s3_store.download_video_from_url.assert_called_once()
            mock_store.goals_processed.replace_one.assert_called_once()
            mock_store.goals_pending.delete_one.assert_called_once()
    
    def test_download_flow_goal_not_found(self, clear_download_flow_modules):
        """Test download flow when goal is not found"""
        with patch('found_footy.flows.download_flow.store') as mock_store:
            mock_store.goals_pending.find_one.return_value = None
            
            from found_footy.flows.download_flow import download_flow
            
            result = download_flow(goal_id="12345_67_789")
            
            assert result["status"] == "completed"
            # Should handle gracefully when goal not found
    
    def test_download_flow_no_videos(self, clear_download_flow_modules):
        """Test download flow when no videos are discovered"""
        with patch('found_footy.flows.download_flow.store') as mock_store:
            goal_doc = {
                "_id": "12345_67_789",
                "team_name": "Barcelona",
                "player_name": "Lionel Messi",
                "minute": 67,
                "fixture_id": 12345,
                "discovered_videos": []  # No videos
            }
            mock_store.goals_pending.find_one.return_value = goal_doc
            
            from found_footy.flows.download_flow import download_flow
            
            result = download_flow(goal_id="12345_67_789")
            
            assert result["status"] == "completed"
    
    def test_download_flow_s3_failure(self, clear_download_flow_modules):
        """Test download flow when S3 upload fails"""
        with patch('found_footy.flows.download_flow.store') as mock_store, \
             patch('found_footy.flows.download_flow.s3_store') as mock_s3_store:
            
            goal_doc = {
                "_id": "12345_67_789",
                "team_name": "Barcelona",
                "player_name": "Lionel Messi",
                "minute": 67,
                "fixture_id": 12345,
                "discovered_videos": [
                    {
                        "video_url": "https://video.twimg.com/test.mp4",
                        "search_index": 0,
                        "video_index": 0
                    }
                ]
            }
            mock_store.goals_pending.find_one.return_value = goal_doc
            mock_store.goals_processed.replace_one.return_value = Mock()
            mock_store.goals_pending.delete_one.return_value = Mock()
            
            mock_s3_store.download_video_from_url.return_value = {
                "status": "error",
                "error": "S3 connection failed"
            }
            mock_s3_store.bucket_name = "footy-videos"
            mock_s3_store.get_bucket_stats.return_value = {"total_videos": 10, "total_size_mb": 50.0}
            
            from found_footy.flows.download_flow import download_flow
            
            result = download_flow(goal_id="12345_67_789")
            
            assert result["status"] == "completed"
            # Should handle S3 failures gracefully