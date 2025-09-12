"""Professional test infrastructure for Found Footy"""
import pytest
import sys
import os
from unittest.mock import Mock, patch, MagicMock
from datetime import datetime, timezone

# ✅ CRITICAL: Set test environment before any imports
os.environ['TESTING'] = 'true'
os.environ['MONGODB_URL'] = 'mongodb://test:test@localhost:27017/test_db'
os.environ['S3_ENDPOINT_URL'] = 'http://localhost:9000'

@pytest.fixture(scope="session", autouse=True)
def configure_test_environment():
    """Configure global test environment"""
    os.environ['TESTING'] = 'true'
    
@pytest.fixture(autouse=True) 
def mock_all_external_dependencies():
    """Mock ALL external dependencies globally"""
    with patch('pymongo.MongoClient') as mock_mongo, \
         patch('boto3.client') as mock_boto3, \
         patch('prefect.deployments.run_deployment') as mock_run_deployment, \
         patch('prefect.get_client') as mock_prefect_client, \
         patch('found_footy.utils.team_data.get_team_ids') as mock_team_ids, \
         patch('found_footy.utils.fixture_status.get_fixture_statuses') as mock_fixture_statuses, \
         patch('found_footy.api.mongo_api.fixtures') as mock_fixtures_api, \
         patch('found_footy.api.mongo_api.fixtures_batch') as mock_fixtures_batch, \
         patch('found_footy.api.mongo_api.fixtures_events') as mock_fixtures_events, \
         patch('found_footy.api.twitter_api.twitter_videos_search') as mock_twitter_search:
        
        # ✅ Mock MongoDB
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
        
        # ✅ Mock S3
        mock_s3_client = MagicMock()
        mock_s3_client.head_bucket.return_value = True
        mock_s3_client.upload_file.return_value = None
        mock_s3_client.list_objects_v2.return_value = {'Contents': []}
        mock_boto3.return_value = mock_s3_client
        
        # ✅ Mock Prefect
        mock_run_deployment.return_value = MagicMock()
        mock_prefect_client.return_value.__aenter__.return_value = MagicMock()
        mock_prefect_client.return_value.__aexit__.return_value = None
        
        # ✅ Mock Utils
        mock_team_ids.return_value = [541, 529, 157, 50]  # Sample team IDs
        mock_fixture_statuses.return_value = {
            "completed": ["FT", "AET", "PEN"],
            "active": ["1H", "HT", "2H", "ET"],
            "staging": ["TBD", "NS"]
        }
        
        # ✅ Mock APIs
        mock_fixtures_api.return_value = []
        mock_fixtures_batch.return_value = []
        mock_fixtures_events.return_value = []
        mock_twitter_search.return_value = []
        
        yield {
            'mongo_client': mock_mongo_instance,
            'mongo_db': mock_db,
            'mongo_collection': mock_collection,
            's3_client': mock_s3_client,
            'run_deployment': mock_run_deployment,
            'team_ids': mock_team_ids,
            'fixture_statuses': mock_fixture_statuses,
            'fixtures_api': mock_fixtures_api,
            'fixtures_batch': mock_fixtures_batch,
            'fixtures_events': mock_fixtures_events,
            'twitter_search': mock_twitter_search
        }

@pytest.fixture
def sample_fixture_data():
    """Sample fixture data for testing"""
    return {
        "id": 12345,
        "fixture_id": 12345,
        "home": "Real Madrid",
        "home_id": 541,
        "away": "Barcelona", 
        "away_id": 529,
        "league": "La Liga",
        "league_id": 140,
        "time": "2025-01-15T20:00:00Z",
        "status": "NS",
        "goals": {"home": 0, "away": 0},
        "team_names": {
            "home": "Real Madrid",
            "away": "Barcelona"
        },
        "kickoff_time": datetime(2025, 1, 15, 20, 0, 0, tzinfo=timezone.utc)
    }

@pytest.fixture 
def sample_goal_data():
    """Sample goal event data for testing"""
    return {
        "time": {
            "elapsed": 67,
            "extra": None
        },
        "team": {
            "id": 541,
            "name": "Real Madrid"
        },
        "player": {
            "id": 789,
            "name": "Karim Benzema"
        },
        "assist": {
            "id": 790,
            "name": "Luka Modric"
        },
        "type": "Goal",
        "detail": "Normal Goal"
    }

@pytest.fixture
def sample_twitter_videos():
    """Sample Twitter video search results"""
    return [
        {
            "source": "nitter",
            "tweet_id": "1234567890",
            "tweet_url": "https://twitter.com/user/status/1234567890",
            "video_url": "https://video.twimg.com/ext_tw_video/test1.mp4",
            "tweet_text": "🔥 Amazing goal by Benzema! What a strike! ⚽",
            "username": "football_highlights",
            "timestamp": "2025-01-15T20:07:00Z",
            "video_metadata": {
                "resolution": "1280x720",
                "duration": 45,
                "format": "mp4",
                "quality": "720p"
            },
            "url_validated": True
        }
    ]

@pytest.fixture(autouse=True)
def clear_module_cache():
    """Clear module cache before each test"""
    modules_to_clear = [
        'found_footy.flows.advance_flow',
        'found_footy.flows.goal_flow', 
        'found_footy.flows.twitter_flow',
        'found_footy.flows.download_flow',
        'found_footy.flows.monitor_flow',
        'found_footy.flows.ingest_flow',
        'found_footy.flows.shared_tasks',
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