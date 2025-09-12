"""Tests for advance flow - BUSINESS LOGIC ONLY"""
import pytest
from unittest.mock import patch, Mock

class TestAdvanceFlow:
    """Test advance flow business logic WITHOUT running actual flows"""
    
    def test_advance_flow_staging_to_active(self):
        """Test advance flow logic by directly testing the task"""
        
        # ✅ DIRECT TASK TESTING: Test the actual business logic
        with patch('found_footy.storage.mongo_store.FootyMongoStore') as mock_store_class:
            # Mock the store instance returned by FootyMongoStore()
            mock_store = Mock()
            mock_store_class.return_value = mock_store
            
            # Mock the fixtures_advance method to return success
            mock_store.fixtures_advance.return_value = {
                "status": "success",
                "advanced_count": 3
            }
            
            # Import and test the task directly
            from found_footy.flows.shared_tasks import fixtures_advance_task
            
            result = fixtures_advance_task("fixtures_staging", "fixtures_active", None)
            
            # Verify business logic
            assert result["status"] == "success"
            assert result["advanced_count"] == 3
            
            # Verify the store method was called correctly
            mock_store.fixtures_advance.assert_called_once_with(
                "fixtures_staging", "fixtures_active", None
            )
    
    def test_advance_flow_specific_fixture(self):
        """Test advancing a specific fixture"""
        
        with patch('found_footy.storage.mongo_store.FootyMongoStore') as mock_store_class:
            mock_store = Mock()
            mock_store_class.return_value = mock_store
            mock_store.fixtures_advance.return_value = {
                "status": "success", 
                "advanced_count": 1
            }
            
            from found_footy.flows.shared_tasks import fixtures_advance_task
            
            result = fixtures_advance_task("fixtures_active", "fixtures_completed", 12345)
            
            assert result["status"] == "success"
            assert result["advanced_count"] == 1
            
            mock_store.fixtures_advance.assert_called_once_with(
                "fixtures_active", "fixtures_completed", 12345
            )
    
    def test_advance_flow_error_handling(self):
        """Test error handling in advance task"""
        
        with patch('found_footy.storage.mongo_store.FootyMongoStore') as mock_store_class:
            mock_store = Mock()
            mock_store_class.return_value = mock_store
            
            # Mock an exception during advancement
            mock_store.fixtures_advance.side_effect = Exception("Database connection failed")
            
            from found_footy.flows.shared_tasks import fixtures_advance_task
            
            result = fixtures_advance_task("fixtures_staging", "fixtures_active", None)
            
            assert result["status"] == "error"
            assert result["advanced_count"] == 0
            assert "Database connection failed" in str(result.get("error", ""))

class TestAdvanceFlowIntegration:
    """Test advance flow with mocked dependencies"""
    
    def test_advance_flow_complete_logic(self):
        """Test the complete advance flow logic"""
        
        with patch('found_footy.flows.shared_tasks.fixtures_advance_task') as mock_task:
            mock_task.return_value = {
                "status": "success",
                "advanced_count": 2
            }
            
            # Import the flow function
            from found_footy.flows.advance_flow import advance_flow
            
            # Test the flow wrapper logic
            result = advance_flow(
                source_collection="fixtures_staging",
                destination_collection="fixtures_active",
                fixture_id=None
            )
            
            # Verify the flow logic
            assert result["status"] == "success"
            assert result["source_collection"] == "fixtures_staging"
            assert result["destination_collection"] == "fixtures_active"
            assert result["advanced_count"] == 2
            
            # Verify the task was called
            mock_task.assert_called_once_with(
                "fixtures_staging", "fixtures_active", None
            )