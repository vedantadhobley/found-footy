"""Tests for advance flow - Updated for .fn() approach"""
import pytest
from unittest.mock import patch, Mock

class TestAdvanceFlowWithFn:
    """Test advance flow using .fn() approach"""
    
    def test_advance_flow_fn_staging_to_active(self):
        """Test advance_flow.fn() staging to active advancement"""
        
        with patch('found_footy.flows.advance_flow.fixtures_advance_task') as mock_advance:
            mock_advance.return_value = {
                "status": "success", 
                "advanced_count": 3
            }
            
            # ✅ USE .fn() TO TEST ACTUAL BUSINESS LOGIC
            from found_footy.flows.advance_flow import advance_flow
            result = advance_flow.fn(
                source_collection="fixtures_staging",
                destination_collection="fixtures_active",
                fixture_id=None
            )
            
            assert result["status"] == "success"
            assert result["advanced_count"] == 3
            assert result["source_collection"] == "fixtures_staging"
            assert result["destination_collection"] == "fixtures_active"
            
            # Verify task was called with correct parameters
            mock_advance.assert_called_once_with("fixtures_staging", "fixtures_active", None)
    
    def test_advance_flow_fn_specific_fixture(self):
        """Test advance_flow.fn() with specific fixture advancement"""
        
        with patch('found_footy.flows.advance_flow.fixtures_advance_task') as mock_advance:
            mock_advance.return_value = {
                "status": "success", 
                "advanced_count": 1
            }
            
            # ✅ USE .fn() TO TEST ACTUAL BUSINESS LOGIC
            from found_footy.flows.advance_flow import advance_flow
            result = advance_flow.fn(
                source_collection="fixtures_active",
                destination_collection="fixtures_completed",
                fixture_id=12345
            )
            
            assert result["status"] == "success"
            assert result["advanced_count"] == 1
            assert result["fixture_id"] == 12345
            
            # Verify task was called with specific fixture ID
            mock_advance.assert_called_once_with("fixtures_active", "fixtures_completed", 12345)
    
    def test_fixtures_advance_task_fn_business_logic(self):
        """Test fixtures_advance_task.fn() business logic"""
        
        with patch('found_footy.flows.shared_tasks.FootyMongoStore') as MockStore:
            mock_store = Mock()
            mock_store.fixtures_advance.return_value = {
                "status": "success",
                "advanced_count": 2
            }
            MockStore.return_value = mock_store
            
            # ✅ USE .fn() TO TEST ACTUAL TASK LOGIC
            from found_footy.flows.shared_tasks import fixtures_advance_task
            result = fixtures_advance_task.fn("fixtures_staging", "fixtures_active", None)
            
            assert result["status"] == "success"
            assert result["advanced_count"] == 2
            
            # Verify store method was called
            mock_store.fixtures_advance.assert_called_once_with("fixtures_staging", "fixtures_active", None)