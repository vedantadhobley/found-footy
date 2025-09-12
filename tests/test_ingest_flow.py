"""Tests for ingest flow - PURE LOGIC TESTING"""
import pytest
from unittest.mock import patch, Mock
from datetime import date

class TestIngestFlow:
    """Test ingest flow business logic"""
    
    def test_ingest_flow_successful_processing(self, sample_fixture_data):
        """Test successful fixture ingestion"""
        
        # ✅ FIX: Mock all the imported task functions from shared_tasks module
        with patch('found_footy.flows.shared_tasks.fixtures_process_parameters_task') as mock_params, \
             patch('found_footy.flows.shared_tasks.fixtures_fetch_api_task') as mock_fetch, \
             patch('found_footy.flows.shared_tasks.fixtures_categorize_task') as mock_categorize, \
             patch('found_footy.flows.shared_tasks.fixtures_store_task') as mock_store, \
             patch('found_footy.flows.ingest_flow.schedule_advance_flow') as mock_schedule:
            
            # Mock successful pipeline
            mock_params.return_value = {
                "query_date": date.today(),
                "valid_team_ids": [541, 529]
            }
            mock_fetch.return_value = [sample_fixture_data]
            mock_categorize.return_value = {
                "staging_fixtures": [sample_fixture_data],
                "active_fixtures": [],
                "completed_fixtures": []
            }
            mock_store.return_value = {
                "staging_count": 1,
                "active_count": 0,
                "completed_count": 0
            }
            mock_schedule.return_value = {"status": "scheduled"}
            
            from found_footy.flows.ingest_flow import ingest_flow
            
            result = ingest_flow()
            
            # Test business logic
            assert result["status"] == "success"
            assert result["staging_fixtures"] == 1
            assert result["scheduled_advances"] == 1
            
            # Verify call chain
            mock_params.assert_called_once()
            mock_fetch.assert_called_once()
            mock_categorize.assert_called_once()
            mock_store.assert_called_once()
    
    def test_ingest_flow_no_fixtures_found(self):
        """Test ingest flow when no fixtures found"""
        
        with patch('found_footy.flows.shared_tasks.fixtures_process_parameters_task') as mock_params, \
             patch('found_footy.flows.shared_tasks.fixtures_fetch_api_task') as mock_fetch:
            
            mock_params.return_value = {
                "query_date": date.today(),
                "valid_team_ids": [541, 529]
            }
            mock_fetch.return_value = []  # No fixtures
            
            from found_footy.flows.ingest_flow import ingest_flow
            
            result = ingest_flow()
            
            assert result["status"] == "no_fixtures"
            assert "No fixtures found" in result["message"]
    
    def test_ingest_flow_with_parameters(self):
        """Test ingest flow with specific parameters"""
        
        with patch('found_footy.flows.shared_tasks.fixtures_process_parameters_task') as mock_params, \
             patch('found_footy.flows.shared_tasks.fixtures_fetch_api_task') as mock_fetch, \
             patch('found_footy.flows.shared_tasks.fixtures_categorize_task') as mock_categorize, \
             patch('found_footy.flows.shared_tasks.fixtures_store_task') as mock_store:
            
            mock_params.return_value = {
                "query_date": date(2025, 1, 15),
                "valid_team_ids": [541]
            }
            mock_fetch.return_value = [{"fixture": "data"}]
            mock_categorize.return_value = {
                "staging_fixtures": [],
                "active_fixtures": [{"fixture": "data"}],
                "completed_fixtures": []
            }
            mock_store.return_value = {
                "staging_count": 0,
                "active_count": 1,
                "completed_count": 0
            }
            
            from found_footy.flows.ingest_flow import ingest_flow
            
            result = ingest_flow(date_str="20250115", team_ids="541")
            
            assert result["status"] == "success"
            assert result["active_fixtures"] == 1
            
            mock_params.assert_called_once_with("541", "20250115")