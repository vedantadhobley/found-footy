"""Tests for MongoDB API functions"""
import pytest
from unittest.mock import patch, Mock
from found_footy.api.mongo_api import (
    fixtures, 
    fixtures_events, 
    fixtures_batch,
    filter_fixtures_by_teams,
    parse_team_ids_parameter
)

class TestMongoAPI:
    """Test suite for MongoDB API functions"""
    
    @patch('found_footy.api.mongo_api.requests.get')
    def test_fixtures_success(self, mock_get, sample_fixture_data):
        """Test successful fixtures API call"""
        # Mock API response
        mock_response = Mock()
        mock_response.json.return_value = {
            "response": [{
                "fixture": {"id": 12345, "date": "2025-01-15T15:00:00Z"},
                "teams": {
                    "home": {"name": "Real Madrid", "id": 541},
                    "away": {"name": "Barcelona", "id": 529}
                },
                "league": {"name": "La Liga", "id": 140}
            }]
        }
        mock_response.raise_for_status.return_value = None
        mock_get.return_value = mock_response
        
        # Test
        result = fixtures("2025-01-15")
        
        # Assertions
        assert len(result) == 1
        assert result[0]["id"] == 12345
        assert result[0]["home"] == "Real Madrid"
        assert result[0]["away"] == "Barcelona"
        mock_get.assert_called_once()
    
    @patch('found_footy.api.mongo_api.requests.get')
    def test_fixtures_events_goals_only(self, mock_get):
        """Test fixtures_events returns only goal events"""
        # Mock API response with mixed events
        mock_response = Mock()
        mock_response.json.return_value = {
            "response": [
                {"type": "Goal", "detail": "Goal", "player": {"name": "Messi"}},
                {"type": "Card", "detail": "Yellow Card", "player": {"name": "Ronaldo"}},
                {"type": "Goal", "detail": "Missed Penalty", "player": {"name": "Benzema"}},
                {"type": "Goal", "detail": "Goal", "player": {"name": "Vinicius"}}
            ]
        }
        mock_response.raise_for_status.return_value = None
        mock_get.return_value = mock_response
        
        # Test
        result = fixtures_events(12345)
        
        # Assertions - should only return actual goals, not cards or missed penalties
        assert len(result) == 2
        assert all(event["type"] == "Goal" for event in result)
        assert all(event["detail"] != "Missed Penalty" for event in result)
    
    def test_filter_fixtures_by_teams(self, sample_fixture_data):
        """Test fixture filtering by team IDs"""
        fixtures_list = [sample_fixture_data]
        team_ids = [541, 42]  # Real Madrid + another team
        
        result = filter_fixtures_by_teams(fixtures_list, team_ids)
        
        assert len(result) == 1  # Should include Real Madrid fixture
        assert result[0]["home_id"] == 541
    
    def test_filter_fixtures_by_teams_no_match(self, sample_fixture_data):
        """Test fixture filtering with no matching teams"""
        fixtures_list = [sample_fixture_data]
        team_ids = [999, 888]  # Teams not in fixture
        
        result = filter_fixtures_by_teams(fixtures_list, team_ids)
        
        assert len(result) == 0
    
    def test_parse_team_ids_parameter_string(self):
        """Test parsing team IDs from comma-separated string"""
        result = parse_team_ids_parameter("541,529,50")
        assert result == [541, 529, 50]
    
    def test_parse_team_ids_parameter_json_list(self):
        """Test parsing team IDs from JSON list"""
        result = parse_team_ids_parameter("[541, 529, 50]")
        assert result == [541, 529, 50]
    
    def test_parse_team_ids_parameter_single_int(self):
        """Test parsing single team ID"""
        result = parse_team_ids_parameter(541)
        assert result == [541]
    
    def test_parse_team_ids_parameter_empty(self):
        """Test parsing empty parameter"""
        assert parse_team_ids_parameter("") == []
        assert parse_team_ids_parameter(None) == []