"""Tests for goal flow - Updated for .fn() approach"""
import pytest
from unittest.mock import patch, Mock

class TestGoalFlowWithFn:
    """Test goal flow using .fn() approach"""
    
    def test_goal_flow_fn_successful_processing(self, sample_goal_data):
        """Test goal_flow.fn() with successful goal processing"""
        
        with patch('found_footy.flows.goal_flow.FootyMongoStore') as MockStore, \
             patch('found_footy.flows.goal_flow.schedule_twitter_flow') as mock_schedule:
            
            mock_store = Mock()
            mock_store.store_goal_pending.return_value = True
            MockStore.return_value = mock_store
            
            mock_schedule.return_value = {
                "status": "scheduled",
                "scheduled_time": "2025-01-15T15:07:00",
                "delay_minutes": 2
            }
            
            # ✅ USE .fn() TO TEST ACTUAL BUSINESS LOGIC
            from found_footy.flows.goal_flow import goal_flow
            
            goal_events = [sample_goal_data]
            result = goal_flow.fn(fixture_id=12345, goal_events=goal_events)
            
            # Test actual flow return structure
            assert result["status"] == "success"
            assert result["goals_processed"] == 1
            assert result["twitter_flows_scheduled"] == 1
            assert result["delay_minutes"] == 5
            
            # Verify actual business logic was called
            mock_store.store_goal_pending.assert_called_once()
            mock_schedule.assert_called_once()
    
    def test_goal_flow_fn_duplicate_detection(self, sample_goal_data):
        """Test goal_flow.fn() duplicate detection logic"""
        
        with patch('found_footy.flows.goal_flow.FootyMongoStore') as MockStore, \
             patch('found_footy.flows.goal_flow.schedule_twitter_flow') as mock_schedule:
            
            mock_store = Mock()
            mock_store.store_goal_pending.return_value = False  # Duplicate detected
            MockStore.return_value = mock_store
            
            # ✅ USE .fn() TO TEST ACTUAL BUSINESS LOGIC
            from found_footy.flows.goal_flow import goal_flow
            
            goal_events = [sample_goal_data]
            result = goal_flow.fn(fixture_id=12345, goal_events=goal_events)
            
            assert result["status"] == "success"
            assert result["goals_processed"] == 0  # Should be 0 for duplicates
            assert result["twitter_flows_scheduled"] == 0
            
            # Verify store was called but schedule was not
            mock_store.store_goal_pending.assert_called_once()
            mock_schedule.assert_not_called()