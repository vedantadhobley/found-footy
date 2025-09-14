"""Test infrastructure for Found Footy"""
import os
import sys
import pytest
from unittest.mock import patch, MagicMock, Mock
from datetime import datetime, timezone
import logging

# Run flows and tasks as plain functions to avoid Prefect engine (and server imports)
@pytest.fixture(scope="session", autouse=True)
def configure_test_environment():
    os.environ.setdefault("TESTING", "true")
    os.environ.setdefault("PREFECT_API_URL", "http://localhost:4200/api")
    os.environ.setdefault("PYTHONHTTPSVERIFY", "0")
    try:
        from prefect.flows import Flow
        from prefect.tasks import Task
        Flow.__call__ = lambda self, *a, **k: self.fn(*a, **k)
        Task.__call__ = lambda self, *a, **k: self.fn(*a, **k)
    except Exception:
        pass
    yield

@pytest.fixture(autouse=True)
def mock_external_services():
    """Mock external services - Fixed to patch the class correctly"""
    with (
        patch("pymongo.MongoClient") as mock_mongo,
        patch("boto3.client") as mock_boto3,
        patch("prefect.deployments.run_deployment") as mock_run_deployment,
        # ✅ FIX: Remove the incorrect TwitterBrowserScraper patch
        patch("selenium.webdriver.Chrome") as mock_chrome,
        patch("yt_dlp.YoutubeDL") as mock_ytdlp,
        patch("found_footy.utils.team_data.get_team_ids", return_value=[541, 529, 157, 50]),
        patch(
            "found_footy.utils.fixture_status.get_fixture_statuses",
            return_value={"completed": ["FT", "AET", "PEN"], "active": ["1H", "HT", "2H"], "staging": ["TBD", "NS"]},
        ),
    ):
        # Mongo mock setup
        mock_collection = MagicMock()
        mock_collection.find.return_value = []
        mock_collection.find_one.return_value = None
        mock_collection.count_documents.return_value = 0
        mock_collection.bulk_write.return_value = MagicMock(upserted_count=0, modified_count=0)
        mock_collection.delete_one.return_value = MagicMock()
        mock_collection.delete_many.return_value = MagicMock()
        mock_collection.replace_one.return_value = MagicMock()
        mock_collection.insert_one.return_value = MagicMock()
        mock_collection.create_index.return_value = None

        mock_db = MagicMock()
        mock_db.fixtures_staging = mock_collection
        mock_db.fixtures_active = mock_collection
        mock_db.fixtures_completed = mock_collection
        mock_db.goals_pending = mock_collection
        mock_db.goals_processed = mock_collection

        mock_mongo_instance = MagicMock()
        mock_mongo_instance.found_footy = mock_db
        mock_mongo.return_value = mock_mongo_instance

        # S3 mock setup
        mock_s3_client = MagicMock()
        mock_s3_client.head_bucket.return_value = True
        mock_s3_client.upload_file.return_value = None
        mock_s3_client.list_objects_v2.return_value = {"Contents": []}
        mock_boto3.return_value = mock_s3_client

        # yt-dlp mock setup
        ytdlp_instance = Mock()
        ytdlp_instance.extract_info.return_value = {"width": 1280, "height": 720, "duration": 45, "ext": "mp4"}
        ytdlp_instance.download.return_value = None
        mock_ytdlp.return_value.__enter__.return_value = ytdlp_instance
        mock_ytdlp.return_value.__exit__.return_value = None

        yield

# Test data fixtures used by multiple tests
@pytest.fixture
def sample_goal_data():
    return {
        "time": {"elapsed": 67},
        "player": {"id": 789, "name": "Lionel Messi"},
        "team": {"name": "Barcelona"},
        "assist": {"name": "Teammate"},
        "type": "Goal",
        "detail": "Normal Goal"
    }

@pytest.fixture
def sample_fixture_data():
    return {
        "id": 12345,
        "home": "Real Madrid",
        "home_id": 541,
        "away": "Barcelona",
        "away_id": 529,
        "league": "La Liga",
        "league_id": 140,
        "time": datetime.now(timezone.utc).isoformat()
    }

@pytest.fixture
def sample_twitter_videos():
    return [
        {
            "source": "browser_automation",
            "tweet_id": "1234567890123456789",
            "tweet_url": "https://twitter.com/user/status/1234567890123456789",
            "video_page_url": "https://x.com/i/status/1234567890123456789",
            "tweet_text": "🔥 Amazing goal!",
            "username": "football_fan",
            "timestamp": datetime.now(timezone.utc).isoformat(),
            "discovered_at": datetime.now(timezone.utc).isoformat(),
            "requires_ytdlp": True,
            "search_index": 0,
            "video_index": 0
        }
    ]