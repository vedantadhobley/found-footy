"""Tests for ingest flow - Updated for .fn() approach"""
import pytest
from unittest.mock import patch, Mock
from datetime import date

class TestIngestFlowWithFn:
    """Test ingest flow using .fn() approach"""
    
    def test_ingest_flow_fn_successful_processing(self, sample_fixture_data):
        """Test ingest_flow.fn() with successful fixture ingestion"""
        
        with patch('found_footy.flows.ingest_flow.fixtures_process_parameters_task') as mock_params, \
             patch('found_footy.flows.ingest_flow.fixtures_fetch_api_task') as mock_fetch, \
             patch('found_footy.flows.ingest_flow.fixtures_categorize_task') as mock_categorize, \
             patch('found_footy.flows.ingest_flow.fixtures_store_task') as mock_store, \
             patch('found_footy.flows.ingest_flow.schedule_advance_flow') as mock_schedule:
            
            # Setup all the business logic mocks
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
            
            # ✅ USE .fn() TO TEST ACTUAL BUSINESS LOGIC
            from found_footy.flows.ingest_flow import ingest_flow
            result = ingest_flow.fn(date_str="20250115", team_ids="541,529")
            
            # Test actual flow return structure
            assert result["status"] == "success"
            assert result["staging_fixtures"] == 1
            assert result["scheduled_advances"] == 1
            
            # Verify actual business logic was called
            mock_params.assert_called_once()
            mock_fetch.assert_called_once()
            mock_categorize.assert_called_once()
            mock_store.assert_called_once()
    
    def test_ingest_flow_fn_no_fixtures_found(self):
        """Test ingest_flow.fn() when no fixtures are found"""
        
        with patch('found_footy.flows.ingest_flow.fixtures_process_parameters_task') as mock_params, \
             patch('found_footy.flows.ingest_flow.fixtures_fetch_api_task') as mock_fetch:
            
            mock_params.return_value = {
                "query_date": date.today(),
                "valid_team_ids": [541, 529]
            }
            mock_fetch.return_value = []  # No fixtures found
            
            # ✅ USE .fn() TO TEST ACTUAL BUSINESS LOGIC
            from found_footy.flows.ingest_flow import ingest_flow
            result = ingest_flow.fn(date_str="20250115", team_ids="541,529")
            
            assert result["status"] == "no_fixtures"
            assert result["message"] == "No fixtures found"