"""Test configuration with centralized module clearing"""
import os
import sys
import pytest
from datetime import datetime, timezone
from typing import List
from unittest.mock import patch, MagicMock, Mock

@pytest.fixture(scope="session", autouse=True)
def configure_test_environment():
    """Configure test environment for .fn() testing"""
    os.environ.setdefault("TESTING", "true")
    
    # ✅ Enable .fn() testing - this makes flows testable without Prefect server
    try:
        from prefect.flows import Flow
        from prefect.tasks import Task
        Flow.__call__ = lambda self, *a, **k: self.fn(*a, **k)
        Task.__call__ = lambda self, *a, **k: self.fn(*a, **k)
    except Exception:
        pass

@pytest.fixture
def clear_flow_modules():
    """✅ CONSOLIDATED: Shared module clearing for all flow tests"""
    def _clear_modules(module_names: List[str]):
        """Clear specified modules from sys.modules"""
        cleared_count = 0
        for module in module_names:
            if module in sys.modules:
                del sys.modules[module]
                cleared_count += 1
        return cleared_count
    return _clear_modules

@pytest.fixture
def common_flow_modules():
    """✅ CONSOLIDATED: Common modules that need clearing"""
    return [
        'found_footy.flows.twitter_flow',
        'found_footy.flows.monitor_flow', 
        'found_footy.flows.goal_flow',
        'found_footy.flows.download_flow',
        'found_footy.flows.advance_flow',
        'found_footy.flows.ingest_flow',
        'found_footy.storage.mongo_store',
        'found_footy.storage.s3_store',
        'found_footy.flows.shared_tasks'
    ]

@pytest.fixture(autouse=True)
def clear_all_flow_modules(clear_flow_modules, common_flow_modules):
    """✅ CONSOLIDATED: Auto-clear all flow modules before each test"""
    cleared_count = clear_flow_modules(common_flow_modules)
    print(f"🧹 Cleared {cleared_count} modules before test")

@pytest.fixture(autouse=True)
def mock_external_services():
    """Mock external services for .fn() testing"""
    with (
        patch("pymongo.MongoClient") as mock_mongo,
        patch("boto3.client") as mock_boto3,
        patch("requests.post") as mock_requests,
        patch("yt_dlp.YoutubeDL") as mock_ytdlp,
        patch("found_footy.utils.team_data.get_team_ids", return_value=[541, 529, 157, 50]),
        patch("found_footy.utils.fixture_status.get_fixture_statuses", return_value={
            "completed": ["FT", "AET", "PEN"], 
            "active": ["1H", "HT", "2H"], 
            "staging": ["TBD", "NS"]
        }),
    ):
        # Setup MongoDB mock
        mock_collection = MagicMock()
        mock_collection.find.return_value = []
        mock_collection.find_one.return_value = None
        mock_collection.count_documents.return_value = 0
        mock_collection.bulk_write.return_value = MagicMock(upserted_count=0, modified_count=0)
        mock_collection.delete_one.return_value = MagicMock()
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

        # Setup S3 mock
        mock_s3_client = MagicMock()
        mock_s3_client.head_bucket.return_value = True
        mock_s3_client.upload_file.return_value = None
        mock_s3_client.list_objects_v2.return_value = {"Contents": []}
        mock_boto3.return_value = mock_s3_client

        # Setup requests mock (for Twitter session)
        mock_requests.return_value = MagicMock(
            status_code=200,
            json=lambda: {"status": "success", "videos": []}
        )

        # Setup yt-dlp mock
        ytdlp_instance = Mock()
        ytdlp_instance.extract_info.return_value = {
            "width": 1280, 
            "height": 720, 
            "duration": 45, 
            "ext": "mp4"
        }
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