"""Tests for monitor flow - Updated for .fn() approach"""
import pytest
from unittest.mock import patch, Mock

class TestMonitorFlowWithFn:
    """Test monitor flow using .fn() approach"""
    
    def test_monitor_flow_fn_no_active_fixtures(self):
        """Test monitor_flow.fn() with no active fixtures"""
        
        with patch('found_footy.flows.monitor_flow.FootyMongoStore') as MockStore:
            mock_store = Mock()
            mock_store.check_collections_empty.return_value = True
            MockStore.return_value = mock_store
            
            # ✅ USE .fn() TO TEST ACTUAL BUSINESS LOGIC
            from found_footy.flows.monitor_flow import monitor_flow
            result = monitor_flow.fn()
            
            assert result["status"] == "no_work_skipped"
            assert result["reason"] == "no_active_fixtures"
    
    def test_monitor_flow_fn_with_active_fixtures(self):
        """Test monitor_flow.fn() with active fixtures"""
        
        with patch('found_footy.flows.monitor_flow.FootyMongoStore') as MockStore, \
             patch('found_footy.flows.monitor_flow.fixtures_monitor_task') as mock_monitor:
            
            mock_store = Mock()
            mock_store.check_collections_empty.return_value = False
            MockStore.return_value = mock_store
            
            mock_monitor.return_value = {
                "status": "success",
                "goals_detected": 2,
                "goal_flows_triggered": 2
            }
            
            # ✅ USE .fn() TO TEST ACTUAL BUSINESS LOGIC
            from found_footy.flows.monitor_flow import monitor_flow
            result = monitor_flow.fn()
            
            assert result["status"] == "work_completed"
            assert "monitor_result" in result
            
            # Verify monitor task was called
            mock_monitor.assert_called_once()
    
    def test_fixtures_monitor_task_fn_business_logic(self):
        """Test fixtures_monitor_task.fn() business logic"""
        
        with patch('found_footy.flows.monitor_flow.FootyMongoStore') as MockStore, \
             patch('found_footy.flows.monitor_flow.fixtures_delta_task') as mock_delta, \
             patch('found_footy.flows.monitor_flow.run_deployment') as mock_run:
            
            mock_store = Mock()
            MockStore.return_value = mock_store
            
            mock_delta.return_value = {
                "status": "success",
                "fixtures_with_changes": [],
                "fixtures_completed": [],
                "total_goals_detected": 0
            }
            
            # ✅ USE .fn() TO TEST ACTUAL TASK LOGIC
            from found_footy.flows.monitor_flow import fixtures_monitor_task
            result = fixtures_monitor_task.fn()
            
            assert result["status"] == "success"
            assert result["goal_flows_triggered"] == 0
            assert result["completed_fixtures_count"] == 0