"""Tests for monitor flow - BUSINESS LOGIC ONLY"""
import pytest
from unittest.mock import patch, Mock

class TestMonitorFlow:
    """Test monitor flow logic"""
    
    def test_monitor_flow_no_active_fixtures(self):
        """Test monitor flow when no active fixtures exist"""
        
        with patch('found_footy.storage.mongo_store.FootyMongoStore') as mock_store_class:
            mock_store = Mock()
            mock_store_class.return_value = mock_store
            mock_store.check_collections_empty.return_value = True
            
            from found_footy.flows.monitor_flow import monitor_flow
            
            result = monitor_flow()
            
            assert result["status"] == "no_work_skipped"
            assert result["reason"] == "no_active_fixtures"
            
            mock_store.check_collections_empty.assert_called_once_with(["fixtures_active"])
    
    def test_monitor_flow_with_active_fixtures(self):
        """Test monitor flow with active fixtures"""
        
        with patch('found_footy.storage.mongo_store.FootyMongoStore') as mock_store_class, \
             patch('found_footy.flows.shared_tasks.fixtures_delta_task') as mock_delta_task:
            
            mock_store = Mock()
            mock_store_class.return_value = mock_store
            mock_store.check_collections_empty.return_value = False
            
            # Mock delta task results
            mock_delta_task.return_value = {
                "status": "success",
                "fixtures_with_changes": [],
                "fixtures_completed": [],
                "total_goals_detected": 0
            }
            
            from found_footy.flows.monitor_flow import monitor_flow
            
            result = monitor_flow()
            
            assert result["status"] == "work_completed"
            assert "monitor_result" in result
            
            mock_store.check_collections_empty.assert_called_once()
            mock_delta_task.assert_called_once()
    
    def test_fixtures_monitor_task_goal_detection(self):
        """Test monitor task goal detection"""
        
        with patch('found_footy.flows.shared_tasks.fixtures_delta_task') as mock_delta, \
             patch('found_footy.api.mongo_api.fixtures_events') as mock_events, \
             patch('prefect.deployments.run_deployment') as mock_run, \
             patch('found_footy.storage.mongo_store.FootyMongoStore') as mock_store_class:
            
            mock_store = Mock()
            mock_store_class.return_value = mock_store
            
            # Mock goal detection
            mock_delta.return_value = {
                "status": "success",
                "fixtures_with_changes": [
                    {
                        "fixture_id": 12345,
                        "delta_result": {
                            "goals_changed": True,
                            "total_goal_increase": 1,
                            "current_goals": {"home": 1, "away": 0}
                        }
                    }
                ],
                "fixtures_completed": [],
                "total_goals_detected": 1
            }
            
            mock_events.return_value = [{"goal": "data"}]
            mock_store.fixtures_active.find_one.return_value = {
                "team_names": {"home": "Real Madrid", "away": "Barcelona"}
            }
            mock_store.fixtures_update.return_value = True
            
            from found_footy.flows.monitor_flow import fixtures_monitor_task
            
            result = fixtures_monitor_task()
            
            assert result["status"] == "success"
            assert result["goal_flows_triggered"] == 1
            
            mock_run.assert_called_once()