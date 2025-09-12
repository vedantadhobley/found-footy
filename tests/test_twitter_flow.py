"""Tests for twitter flow - BUSINESS LOGIC TESTING"""
import pytest
from unittest.mock import patch, Mock

class TestTwitterFlow:
    """Test twitter flow business logic"""
    
    def test_twitter_flow_successful_search(self, sample_twitter_videos):
        """Test successful Twitter search logic"""
        
        # ✅ MOCK STORE INSTANCE: Mock the FootyMongoStore class
        with patch('found_footy.storage.mongo_store.FootyMongoStore') as mock_store_class, \
             patch('found_footy.api.twitter_api.twitter_videos_search') as mock_search, \
             patch('prefect.deployments.run_deployment') as mock_run:
            
            # Create mock store instance
            mock_store = Mock()
            mock_store_class.return_value = mock_store
            
            # Mock goal data
            goal_doc = {
                "_id": "12345_67_789",
                "team_name": "Barcelona", 
                "player_name": "Lionel Messi",
                "minute": 67,
                "fixture_id": 12345
            }
            mock_store.goals_pending.find_one.return_value = goal_doc
            mock_store.goals_pending.replace_one.return_value = Mock()
            
            # Mock search results
            mock_search.return_value = sample_twitter_videos
            
            from found_footy.flows.twitter_flow import twitter_flow
            
            result = twitter_flow(goal_id="12345_67_789")
            
            assert result["status"] == "completed"
            assert result["search_method"] == "simplified_search"
            assert result["next_step"] == "download_flow"
            
            mock_search.assert_called_once()
            mock_run.assert_called_once()
    
    def test_twitter_flow_goal_not_found(self):
        """Test when goal is not found"""
        
        with patch('found_footy.storage.mongo_store.FootyMongoStore') as mock_store_class:
            mock_store = Mock()
            mock_store_class.return_value = mock_store
            mock_store.goals_pending.find_one.return_value = None
            
            from found_footy.flows.twitter_flow import twitter_flow
            
            result = twitter_flow(goal_id="12345_67_789")
            
            assert result["status"] == "completed"
            assert result["next_step"] == "failed"
    
    def test_twitter_flow_no_goal_id(self):
        """Test without goal_id"""
        
        from found_footy.flows.twitter_flow import twitter_flow
        
        result = twitter_flow(goal_id=None)
        
        assert result["status"] == "error"
        assert "No goal_id provided" in result["message"]
    
    def test_twitter_flow_search_failure(self):
        """Test search failure with fallback"""
        
        with patch('found_footy.storage.mongo_store.FootyMongoStore') as mock_store_class, \
             patch('found_footy.api.twitter_api.twitter_videos_search') as mock_search, \
             patch('prefect.deployments.run_deployment') as mock_run:
            
            mock_store = Mock()
            mock_store_class.return_value = mock_store
            
            goal_doc = {
                "_id": "12345_67_789",
                "team_name": "Barcelona",
                "player_name": "Lionel Messi", 
                "minute": 67,
                "fixture_id": 12345
            }
            mock_store.goals_pending.find_one.return_value = goal_doc
            mock_store.goals_pending.replace_one.return_value = Mock()
            
            # Mock search exception with fallback
            mock_search.side_effect = Exception("Search failed")
            
            from found_footy.flows.twitter_flow import twitter_flow
            
            result = twitter_flow(goal_id="12345_67_789")
            
            # Should still complete with fallback data
            assert result["status"] == "completed"
            
            # Verify the search was attempted (even though it failed)
            mock_search.assert_called_once()