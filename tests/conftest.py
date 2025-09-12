"""Professional test infrastructure for Found Footy - Updated for new dependencies"""
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
    """Mock ALL external dependencies globally - Updated for snscrape and yt-dlp"""
    with patch('pymongo.MongoClient') as mock_mongo, \
         patch('boto3.client') as mock_boto3, \
         patch('prefect.deployments.run_deployment') as mock_run_deployment, \
         patch('prefect.get_client') as mock_prefect_client, \
         patch('found_footy.utils.team_data.get_team_ids') as mock_team_ids, \
         patch('found_footy.utils.fixture_status.get_fixture_statuses') as mock_fixture_statuses, \
         patch('found_footy.api.mongo_api.fixtures') as mock_fixtures_api, \
         patch('found_footy.api.mongo_api.fixtures_batch') as mock_fixtures_batch, \
         patch('found_footy.api.mongo_api.fixtures_events') as mock_fixtures_events, \
         patch('found_footy.api.twitter_api.twitter_videos_search') as mock_twitter_search, \
         patch('found_footy.flows.flow_triggers.schedule_advance_flow') as mock_schedule_advance, \
         patch('found_footy.flows.flow_triggers.schedule_twitter_flow') as mock_schedule_twitter, \
         patch('snscrape.modules.twitter.TwitterSearchScraper') as mock_snscrape, \
         patch('yt_dlp.YoutubeDL') as mock_ytdlp:
        
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
        
        # ✅ Mock Flow Triggers
        mock_schedule_advance.return_value = {"status": "scheduled", "flow_run_id": "test-123"}
        mock_schedule_twitter.return_value = {"status": "scheduled", "flow_run_id": "test-456", "delay_minutes": 2}
        
        # ✅ NEW: Mock snscrape
        mock_tweet = Mock()
        mock_tweet.url = "https://twitter.com/test/status/123"
        mock_tweet.id = "123"
        mock_tweet.rawContent = "Test tweet"
        mock_tweet.user.username = "test_user"
        mock_tweet.date = datetime.now(timezone.utc)
        mock_tweet.media = [Mock(type="video")]
        
        mock_scraper_instance = Mock()
        mock_scraper_instance.get_items.return_value = [mock_tweet]
        mock_snscrape.return_value = mock_scraper_instance
        
        # ✅ NEW: Mock yt-dlp
        mock_ytdlp_instance = Mock()
        mock_ytdlp_instance.extract_info.return_value = {
            "url": "https://video.twimg.com/test.mp4",
            "width": 1280,
            "height": 720,
            "duration": 45,
            "ext": "mp4"
        }
        mock_ytdlp_instance.download.return_value = None
        mock_ytdlp.return_value.__enter__.return_value = mock_ytdlp_instance
        mock_ytdlp.return_value.__exit__.return_value = None
        
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
            'twitter_search': mock_twitter_search,
            'schedule_advance': mock_schedule_advance,
            'schedule_twitter': mock_schedule_twitter,  # ✅ ADD
            'snscrape': mock_snscrape,  # ✅ ADD
            'ytdlp': mock_ytdlp  # ✅ ADD
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
    """Sample Twitter video search results - Updated for snscrape format"""
    return [
        {
            "source": "snscrape_python",
            "tweet_id": "1234567890",
            "tweet_url": "https://twitter.com/user/status/1234567890",
            "video_url": "https://video.twimg.com/ext_tw_video/test1.mp4",
            "tweet_text": "🔥 Amazing goal by Benzema! What a strike! ⚽",
            "username": "football_highlights",
            "timestamp": "2025-01-15T20:07:00Z",
            "discovered_at": "2025-01-15T20:07:00Z",
            "search_index": 0,
            "video_index": 0,
            "requires_ytdlp": True,
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