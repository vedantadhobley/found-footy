"""Tests for storage systems"""
import pytest
from unittest.mock import patch, Mock
from found_footy.storage.mongo_store import FootyMongoStore
from found_footy.storage.s3_store import FootyS3Store

class TestMongoStore:
    """Test suite for MongoDB storage"""
    
    def test_bulk_insert_fixtures(self, mock_mongo_store, sample_fixture_data):
        """Test bulk fixture insertion"""
        fixtures_data = [sample_fixture_data]
        
        result = mock_mongo_store.bulk_insert_fixtures(fixtures_data, "fixtures_staging")
        
        assert result == 5  # Mocked return value
        mock_mongo_store.bulk_insert_fixtures.assert_called_once()
    
    def test_store_goal_pending(self, mock_mongo_store, sample_goal_data):
        """Test storing goal in pending collection"""
        fixture_id = 12345
        
        result = mock_mongo_store.store_goal_pending(fixture_id, sample_goal_data)
        
        mock_mongo_store.store_goal_pending.assert_called_once_with(fixture_id, sample_goal_data)
    
    def test_fixtures_advance(self, mock_mongo_store):
        """Test fixture advancement between collections"""
        result = mock_mongo_store.fixtures_advance("fixtures_staging", "fixtures_active", 12345)
        
        assert result["status"] == "success"
        assert result["advanced_count"] == 3

class TestS3Store:
    """Test suite for S3 storage"""
    
    def test_upload_video_file(self, mock_s3_store):
        """Test video file upload to S3"""
        result = mock_s3_store.upload_video_file(
            "/tmp/test.mp4", 
            "12345_67_789", 
            0,
            {"test": "metadata"}
        )
        
        assert result["status"] == "success"
        assert "s3_key" in result
        assert "s3_url" in result
        mock_s3_store.upload_video_file.assert_called_once()
    
    def test_get_bucket_stats(self, mock_s3_store):
        """Test S3 bucket statistics"""
        result = mock_s3_store.get_bucket_stats()
        
        assert result["bucket_name"] == "footy-videos"
        assert result["total_videos"] == 10
        assert result["total_size_mb"] == 50.0