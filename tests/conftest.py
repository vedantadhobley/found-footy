"""Pytest configuration and shared fixtures"""
import pytest
import os
import warnings
import tempfile
from unittest.mock import Mock, patch
from found_footy.storage.mongo_store import FootyMongoStore
from found_footy.storage.s3_store import FootyS3Store

# ✅ SUPPRESS: Prefect version warnings in tests
warnings.filterwarnings("ignore", message="Your Prefect server is running an older version")

@pytest.fixture(autouse=True)
def suppress_prefect_warnings():
    """Automatically suppress Prefect warnings in all tests"""
    import logging
    logging.getLogger("prefect.client").setLevel(logging.ERROR)

@pytest.fixture
def mock_mongo_store():
    """Mock MongoDB store for testing"""
    with patch('found_footy.storage.mongo_store.FootyMongoStore') as mock:
        store = Mock()
        store.fixtures_active.find.return_value = []
        store.fixtures_staging.find.return_value = []
        store.goals_pending.find_one.return_value = None  # ✅ Default: no existing goals
        store.goals_processed.find_one.return_value = None  # ✅ Default: no existing goals
        store.bulk_insert_fixtures.return_value = 5
        store.fixtures_advance.return_value = {"status": "success", "advanced_count": 3}
        store.store_goal_pending.return_value = True  # ✅ Default: successfully store goals
        mock.return_value = store
        yield store

@pytest.fixture
def mock_s3_store():
    """Mock S3 store for testing"""
    with patch('found_footy.storage.s3_store.FootyS3Store') as mock:
        store = Mock()
        store.upload_video_file.return_value = {
            "status": "success",
            "s3_key": "fixtures/123/goals/test_goal_0.mp4",
            "s3_url": "http://localhost:9000/footy-videos/fixtures/123/goals/test_goal_0.mp4",
            "bucket": "footy-videos",
            "file_size": 1024
        }
        store.get_bucket_stats.return_value = {
            "bucket_name": "footy-videos",
            "total_videos": 10,
            "total_size_mb": 50.0
        }
        mock.return_value = store
        yield store

@pytest.fixture
def sample_fixture_data():
    """Sample fixture data for testing"""
    return {
        "id": 12345,
        "home": "Real Madrid",
        "home_id": 541,
        "away": "Barcelona", 
        "away_id": 529,
        "league": "La Liga",
        "league_id": 140,
        "time": "2025-01-15T15:00:00Z",
        "status": "NS",
        "current_goals": {"home": 0, "away": 0}
    }

@pytest.fixture
def sample_goal_data():
    """Sample goal data for testing - creates goal ID 12345_67_789"""
    return {
        "time": {"elapsed": 67},
        "player": {"id": 789, "name": "Lionel Messi"},
        "team": {"id": 529, "name": "Barcelona"},
        "detail": "Goal"
    }

@pytest.fixture
def sample_twitter_videos():
    """Sample Twitter video search results"""
    return [
        {
            "source": "nitter_basic",
            "tweet_id": "123456789",
            "tweet_url": "https://twitter.com/i/status/123456789",
            "video_url": "https://video.twimg.com/ext_tw_video/test.mp4",
            "tweet_text": "Amazing goal by Messi!",
            "username": "football_fan",
            "video_metadata": {
                "resolution": "720p",
                "duration": "45s",
                "format": "mp4"
            },
            "url_validated": True
        }
    ]