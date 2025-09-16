"""Tests for Twitter Flow - Using .fn() approach"""
import pytest
import sys
from unittest.mock import patch, Mock
from datetime import datetime, timezone

# Clear modules before each test to ensure clean imports
@pytest.fixture(autouse=True)
def clear_twitter_modules():
    """Clear Twitter flow modules before each test"""
    modules_to_clear = [
        'found_footy.flows.twitter_flow',
        'found_footy.storage.mongo_store'
    ]
    
    for module in modules_to_clear:
        if module in sys.modules:
            del sys.modules[module]
    
    yield
    
    for module in modules_to_clear:
        if module in sys.modules:
            del sys.modules[module]

class TestTwitterFlowWithFn:
    """Test Twitter flow using .fn() approach"""
    
    def test_twitter_flow_fn_successful_search(self, clear_twitter_modules):
        """Test twitter_flow.fn() with successful video discovery"""
        
        with patch('found_footy.flows.twitter_flow.FootyMongoStore') as MockStore, \
             patch('found_footy.flows.twitter_flow.TwitterAPIClient') as MockClient, \
             patch('found_footy.flows.twitter_flow.run_deployment') as mock_run:
            
            # Setup MongoDB mock
            mock_store = Mock()
            mock_store.goals_pending.find_one.return_value = {
                "_id": "12345_67",
                "player_name": "L. Messi",
                "team_name": "Barcelona",
                "minute": 67
            }
            mock_store.goals_pending.replace_one.return_value = Mock()
            MockStore.return_value = mock_store
            
            # Setup Twitter API mock
            mock_client = Mock()
            mock_client.search_videos.return_value = [
                {
                    "tweet_url": "https://twitter.com/user/status/123",
                    "source": "session_service",
                    "search_index": 0,
                    "video_index": 0,
                    "requires_ytdlp": True
                }
            ]
            MockClient.return_value = mock_client
            
            mock_run.return_value = Mock()
            
            # ✅ USE .fn() TO TEST ACTUAL BUSINESS LOGIC
            from found_footy.flows.twitter_flow import twitter_flow
            result = twitter_flow.fn(goal_id="12345_67")
            
            # Test actual flow return structure
            assert result["status"] == "completed"
            assert result["search_method"] == "session_service"
            assert result["goal_id"] == "12345_67"
            
            # Verify actual business logic was called
            mock_client.search_videos.assert_called_once()
            mock_store.goals_pending.replace_one.assert_called_once()
    
    def test_twitter_flow_fn_goal_not_found(self, clear_twitter_modules):
        """Test twitter_flow.fn() when goal is not found"""
        
        with patch('found_footy.flows.twitter_flow.FootyMongoStore') as MockStore:
            mock_store = Mock()
            mock_store.goals_pending.find_one.return_value = None
            MockStore.return_value = mock_store
            
            # ✅ USE .fn() TO TEST ACTUAL BUSINESS LOGIC
            from found_footy.flows.twitter_flow import twitter_flow
            result = twitter_flow.fn(goal_id="nonexistent_goal")
            
            # Test actual error handling
            assert result["status"] == "error"
            assert result.get("message") == "No goal_id provided" or "not found" in str(result)
    
    def test_twitter_search_task_fn_business_logic(self, clear_twitter_modules):
        """Test twitter_search_task.fn() business logic"""
        
        with patch('found_footy.flows.twitter_flow.FootyMongoStore') as MockStore, \
             patch('found_footy.flows.twitter_flow.TwitterAPIClient') as MockClient:
            
            mock_store = Mock()
            mock_store.goals_pending.find_one.return_value = {
                "_id": "12345_67",  # ✅ NEW FORMAT: fixture_minute (no player_id)
                "player_name": "D. Szoboszlai",
                "team_name": "Liverpool",
                "minute": 83
            }
            mock_store.goals_pending.replace_one.return_value = Mock()
            MockStore.return_value = mock_store
            
            mock_client = Mock()
            mock_client.search_videos.return_value = [
                {
                    "tweet_url": "https://twitter.com/user/status/123",
                    "source": "browser_automation",
                    "search_index": 0,
                    "video_index": 0
                }
            ]
            MockClient.return_value = mock_client
            
            # ✅ USE .fn() TO TEST ACTUAL TASK LOGIC
            from found_footy.flows.twitter_flow import twitter_search_task
            result = twitter_search_task.fn("12345_67")  # ✅ NEW FORMAT
            
            assert result["status"] == "success"
            assert result["goal_id"] == "12345_67"
            assert result["video_count"] == 1
            assert "Szoboszlai Liverpool" in result.get("search_terms", [])