"""Tests for download flow - Updated for yt-dlp implementation"""
import pytest
import sys  # ✅ ADD: Missing import
import tempfile  # ✅ ADD: Missing import
import os  # ✅ ADD: Missing import
from unittest.mock import patch, Mock, MagicMock
from datetime import datetime, timezone
from typing import Optional, List, Dict, Any

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
            del sys.modules[module]  # ✅ FIX: Complete the line
    
    yield
    
    for module in modules_to_clear:
        if module in sys.modules:
            del sys.modules[module]  # ✅ FIX: Complete the line

class TestDownloadFlow:
    """Test suite for download flow with yt-dlp"""
    
    def test_download_flow_successful_download(self, clear_download_flow_modules):
        """Test successful yt-dlp download and S3 upload"""
        with patch('found_footy.flows.download_flow.store') as mock_store, \
             patch('found_footy.flows.download_flow.s3_store') as mock_s3_store, \
             patch('found_footy.flows.download_flow.yt_dlp.YoutubeDL') as mock_ytdl, \
             patch('found_footy.flows.download_flow.tempfile.TemporaryDirectory') as mock_tempdir, \
             patch('found_footy.flows.download_flow.os.listdir') as mock_listdir, \
             patch('found_footy.flows.download_flow.os.path.getsize') as mock_getsize, \
             patch('found_footy.flows.download_flow.os.path.join') as mock_join:
            
            # Setup goal document with discovered videos
            goal_doc = {
                "_id": "12345_67_789",
                "team_name": "Barcelona",
                "player_name": "Lionel Messi",
                "minute": 67,
                "fixture_id": 12345,
                "discovered_videos": [
                    {
                        "tweet_url": "https://twitter.com/user/status/1234567890",
                        "search_term": "Messi Barcelona",
                        "search_index": 0,
                        "video_index": 0,
                        "tweet_id": "1234567890"
                    }
                ]
            }
            mock_store.goals_pending.find_one.return_value = goal_doc
            mock_store.goals_processed.replace_one.return_value = Mock()
            mock_store.goals_pending.delete_one.return_value = Mock()
            
            # Mock yt-dlp
            mock_ytdl_instance = Mock()
            mock_ytdl_instance.extract_info.return_value = {
                "url": "https://video.twimg.com/actual_video.mp4",
                "width": 1280,
                "height": 720,
                "duration": 45,
                "ext": "mp4",
                "title": "Goal video",
                "uploader": "football_fan"
            }
            mock_ytdl_instance.download.return_value = None
            mock_ytdl.return_value.__enter__.return_value = mock_ytdl_instance
            mock_ytdl.return_value.__exit__.return_value = None
            
            # Mock temp directory
            mock_temp_path = "/tmp/test_download"
            mock_tempdir.return_value.__enter__.return_value = mock_temp_path
            mock_tempdir.return_value.__exit__.return_value = None
            
            # Mock file operations
            mock_join.return_value = "/tmp/test_download/12345_67_789_0_0.mp4"
            mock_listdir.return_value = ["12345_67_789_0_0.mp4"]
            mock_getsize.return_value = 2048
            
            # Setup S3 mock
            mock_s3_store.upload_video_file.return_value = {
                "status": "success",
                "s3_key": "fixtures/12345/goals/12345_67_789_0_0.mp4",
                "s3_url": "http://localhost:9000/footy-videos/test.mp4",
                "bucket": "footy-videos",
                "file_size": 2048,
                "metadata": {}
            }
            mock_s3_store.bucket_name = "footy-videos"
            mock_s3_store.get_bucket_stats.return_value = {"total_videos": 10, "total_size_mb": 50.0}
            
            from found_footy.flows.download_flow import download_flow
            
            result = download_flow(goal_id="12345_67_789")
            
            assert result["status"] == "completed"
            assert result["storage_backend"] == "s3"
            assert result["download_method"] == "yt-dlp_python"
            
            # Verify yt-dlp was called
            mock_ytdl_instance.extract_info.assert_called()
            mock_ytdl_instance.download.assert_called()
            
            # Verify S3 upload
            mock_s3_store.upload_video_file.assert_called_once()
            
            # Verify database operations
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
                "discovered_videos": []  # ✅ FIX: Complete the line
            }
            mock_store.goals_pending.find_one.return_value = goal_doc
            
            from found_footy.flows.download_flow import download_flow
            
            result = download_flow(goal_id="12345_67_789")
            
            assert result["status"] == "completed"
    
    def test_download_flow_ytdlp_failure(self, clear_download_flow_modules):
        """Test download flow when yt-dlp fails"""
        with patch('found_footy.flows.download_flow.store') as mock_store, \
             patch('found_footy.flows.download_flow.s3_store') as mock_s3_store, \
             patch('found_footy.flows.download_flow.yt_dlp.YoutubeDL') as mock_ytdl, \
             patch('found_footy.flows.download_flow.tempfile.TemporaryDirectory') as mock_tempdir:
            
            goal_doc = {
                "_id": "12345_67_789",
                "team_name": "Barcelona", 
                "player_name": "Lionel Messi",
                "minute": 67,
                "fixture_id": 12345,
                "discovered_videos": [
                    {
                        "tweet_url": "https://twitter.com/user/status/1234567890",
                        "search_index": 0,
                        "video_index": 0
                    }
                ]
            }
            mock_store.goals_pending.find_one.return_value = goal_doc
            mock_store.goals_processed.replace_one.return_value = Mock()
            mock_store.goals_pending.delete_one.return_value = Mock()
            
            # Mock yt-dlp to fail
            mock_ytdl_instance = Mock()
            mock_ytdl_instance.extract_info.side_effect = Exception("yt-dlp failed")
            mock_ytdl.return_value.__enter__.return_value = mock_ytdl_instance
            mock_ytdl.return_value.__exit__.return_value = None
            
            mock_tempdir.return_value.__enter__.return_value = "/tmp/test"
            mock_tempdir.return_value.__exit__.return_value = None
            
            mock_s3_store.bucket_name = "footy-videos"
            mock_s3_store.get_bucket_stats.return_value = {"total_videos": 10, "total_size_mb": 50.0}
            
            from found_footy.flows.download_flow import download_flow
            
            result = download_flow(goal_id="12345_67_789")
            
            assert result["status"] == "completed"
            # Should handle yt-dlp failures gracefully
    
    def test_download_flow_s3_failure(self, clear_download_flow_modules):
        """Test download flow when S3 upload fails"""
        with patch('found_footy.flows.download_flow.store') as mock_store, \
             patch('found_footy.flows.download_flow.s3_store') as mock_s3_store, \
             patch('found_footy.flows.download_flow.yt_dlp.YoutubeDL') as mock_ytdl, \
             patch('found_footy.flows.download_flow.tempfile.TemporaryDirectory') as mock_tempdir, \
             patch('found_footy.flows.download_flow.os.listdir') as mock_listdir, \
             patch('found_footy.flows.download_flow.os.path.getsize') as mock_getsize:
            
            goal_doc = {
                "_id": "12345_67_789",
                "team_name": "Barcelona",
                "player_name": "Lionel Messi", 
                "minute": 67,
                "fixture_id": 12345,
                "discovered_videos": [
                    {
                        "tweet_url": "https://twitter.com/user/status/1234567890",
                        "search_index": 0,
                        "video_index": 0
                    }
                ]
            }
            mock_store.goals_pending.find_one.return_value = goal_doc
            mock_store.goals_processed.replace_one.return_value = Mock()
            mock_store.goals_pending.delete_one.return_value = Mock()
            
            # Mock successful yt-dlp
            mock_ytdl_instance = Mock()
            mock_ytdl_instance.extract_info.return_value = {"url": "test.mp4", "width": 720, "height": 480}
            mock_ytdl_instance.download.return_value = None
            mock_ytdl.return_value.__enter__.return_value = mock_ytdl_instance
            mock_ytdl.return_value.__exit__.return_value = None
            
            mock_tempdir.return_value.__enter__.return_value = "/tmp/test"
            mock_tempdir.return_value.__exit__.return_value = None
            mock_listdir.return_value = ["12345_67_789_0_0.mp4"]
            mock_getsize.return_value = 1024
            
            # Mock S3 failure
            mock_s3_store.upload_video_file.return_value = {
                "status": "error",
                "error": "S3 connection failed"
            }
            mock_s3_store.bucket_name = "footy-videos"
            mock_s3_store.get_bucket_stats.return_value = {"total_videos": 10, "total_size_mb": 50.0}
            
            from found_footy.flows.download_flow import download_flow
            
            result = download_flow(goal_id="12345_67_789")
            
            assert result["status"] == "completed"
            # Should handle S3 failures gracefully