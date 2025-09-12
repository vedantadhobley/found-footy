"""Tests for Prefect flows"""
import pytest
from unittest.mock import patch, Mock
from found_footy.flows.shared_tasks import (
    fixtures_process_parameters_task,
    fixtures_categorize_task,
    fixtures_store_task
)

class TestSharedTasks:
    """Test suite for shared flow tasks"""
    
    @patch('found_footy.flows.shared_tasks.get_team_ids')
    def test_fixtures_process_parameters_task(self, mock_get_team_ids):
        """Test parameter processing task"""
        mock_get_team_ids.return_value = [541, 529, 50]
        
        result = fixtures_process_parameters_task(team_ids="541,529", date_str="20250115")
        
        assert "query_date" in result
        assert "valid_team_ids" in result
        assert len(result["valid_team_ids"]) == 2  # Only 2 of 3 requested teams are valid
    
    def test_fixtures_categorize_task(self, sample_fixture_data):
        """Test fixture categorization by status"""
        # Test different statuses
        fixtures = [
            {**sample_fixture_data, "api_status": "NS"},  # Staging
            {**sample_fixture_data, "id": 12346, "api_status": "1H"},  # Active
            {**sample_fixture_data, "id": 12347, "api_status": "FT"}   # Completed
        ]
        
        with patch('found_footy.flows.shared_tasks.fixtures_batch') as mock_batch:
            mock_batch.return_value = []  # No detailed data needed for this test
            
            result = fixtures_categorize_task(fixtures)
        
        assert "staging_fixtures" in result
        assert "active_fixtures" in result
        assert "completed_fixtures" in result
    
    @patch('found_footy.flows.shared_tasks.store')
    def test_fixtures_store_task(self, mock_store):
        """Test fixture storage task"""
        mock_store.bulk_insert_fixtures.return_value = 3
        
        result = fixtures_store_task([], [], [])
        
        assert "staging_count" in result
        assert "active_count" in result
        assert "completed_count" in result