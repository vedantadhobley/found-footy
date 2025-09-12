"""Tests for goal flow - BUSINESS LOGIC ONLY"""
import pytest
from unittest.mock import patch, Mock

class TestGoalFlow:
    """Test goal flow logic without running actual Prefect flows"""
    
    def test_goal_flow_successful_processing(self, sample_goal_data):
        """Test successful goal processing logic"""
        
        # ✅ MOCK STORE INSTANCE: Mock the actual store instance used in goal_flow
        with patch('found_footy.storage.mongo_store.FootyMongoStore') as mock_store_class, \
             patch('found_footy.flows.flow_triggers.schedule_twitter_flow') as mock_schedule:
            
            # Create mock store instance
            mock_store = Mock()
            mock_store_class.return_value = mock_store
            
            # Mock successful goal storage
            mock_store.store_goal_pending.return_value = True
            
            # Mock successful Twitter scheduling
            mock_schedule.return_value = {
                "status": "scheduled",
                "scheduled_time": "2025-01-15T15:07:00",
                "delay_minutes": 2
            }
            
            # Import the goal flow function
            from found_footy.flows.goal_flow import goal_flow
            
            # Test the business logic
            result = goal_flow(
                fixture_id=12345,
                goal_events=[sample_goal_data]
            )
            
            # Verify business logic results
            assert result["status"] == "success"
            assert result["fixture_id"] == 12345
            assert result["goals_processed"] == 1
            assert result["twitter_flows_scheduled"] == 1
            assert result["delay_minutes"] == 5
            
            # Verify method calls
            mock_store.store_goal_pending.assert_called_once_with(12345, sample_goal_data)
            mock_schedule.assert_called_once()
    
    def test_goal_flow_duplicate_detection(self, sample_goal_data):
        """Test duplicate goal handling"""
        
        with patch('found_footy.storage.mongo_store.FootyMongoStore') as mock_store_class, \
             patch('found_footy.flows.flow_triggers.schedule_twitter_flow') as mock_schedule:
            
            mock_store = Mock()
            mock_store_class.return_value = mock_store
            
            # Mock duplicate detection (store returns False)
            mock_store.store_goal_pending.return_value = False
            
            from found_footy.flows.goal_flow import goal_flow
            
            result = goal_flow(
                fixture_id=12345,
                goal_events=[sample_goal_data]
            )
            
            assert result["status"] == "success"
            assert result["goals_processed"] == 0  # No goals processed due to duplicates
            assert result["twitter_flows_scheduled"] == 0
            
            mock_store.store_goal_pending.assert_called_once()
            mock_schedule.assert_not_called()
    
    def test_goal_flow_empty_events(self):
        """Test goal flow with no events"""
        
        from found_footy.flows.goal_flow import goal_flow
        
        result = goal_flow(
            fixture_id=12345,
            goal_events=[]
        )
        
        assert result["status"] == "no_goals"
        assert result["fixture_id"] == 12345
    
    def test_goal_flow_schedule_failure(self, sample_goal_data):
        """Test Twitter scheduling failure"""
        
        with patch('found_footy.storage.mongo_store.FootyMongoStore') as mock_store_class, \
             patch('found_footy.flows.flow_triggers.schedule_twitter_flow') as mock_schedule:
            
            mock_store = Mock()
            mock_store_class.return_value = mock_store
            mock_store.store_goal_pending.return_value = True
            
            # Mock scheduling failure
            mock_schedule.return_value = {
                "status": "error",
                "error": "Deployment not found"
            }
            
            from found_footy.flows.goal_flow import goal_flow
            
            result = goal_flow(
                fixture_id=12345,
                goal_events=[sample_goal_data]
            )
            
            assert result["status"] == "success"
            assert result["goals_processed"] == 0  # Goal stored but no Twitter scheduled
            assert result["twitter_flows_scheduled"] == 0