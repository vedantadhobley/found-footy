"""Complete integration tests for Found Footy workflows"""
import pytest
import sys
from unittest.mock import patch, Mock, MagicMock, AsyncMock
from datetime import datetime, timezone

@pytest.fixture(autouse=True)
def clear_all_modules():
    """Clear all flow modules before each test"""
    modules_to_clear = [
        'found_footy.flows.ingest_flow',
        'found_footy.flows.monitor_flow',
        'found_footy.flows.advance_flow',
        'found_footy.flows.goal_flow',
        'found_footy.flows.twitter_flow',
        'found_footy.flows.download_flow',
        'found_footy.flows.shared_tasks',
        'found_footy.flows.flow_triggers',
        'found_footy.storage.mongo_store',
        'found_footy.storage.s3_store',
        'found_footy.api.twitter_api'
    ]
    
    for module in modules_to_clear:
        if module in sys.modules:
            del sys.modules[module]
    
    yield
    
    for module in modules_to_clear:
        if module in sys.modules:
            del sys.modules[module]

class TestCompleteIntegration:
    """Complete end-to-end integration tests"""
    
    def test_complete_goal_video_pipeline(self, sample_fixture_data, sample_goal_data, sample_twitter_videos):
        """Test complete pipeline: Goal detected → Twitter search → Video download → S3 storage"""
        
        # Test Goal Flow with proper mocking
        with patch('found_footy.flows.shared_tasks.store') as mock_store, \
             patch('found_footy.flows.goal_flow.schedule_twitter_flow') as mock_schedule:
            
            mock_store.store_goal_pending.return_value = True
            # ✅ FIX: Return synchronous mock, not async
            mock_schedule.return_value = {
                "status": "scheduled",
                "scheduled_time": "2025-01-15T15:07:00"
            }
            
            from found_footy.flows.goal_flow import goal_flow
            goal_result = goal_flow(fixture_id=12345, goal_events=[sample_goal_data])
            
            assert goal_result["status"] == "success"
            assert goal_result["goals_processed"] == 1
            assert goal_result["twitter_flows_scheduled"] == 1
        
        # Test Twitter Flow
        with patch('found_footy.flows.twitter_flow.store') as mock_store, \
             patch('found_footy.api.twitter_api.twitter_videos_search') as mock_search, \
             patch('prefect.deployments.run_deployment') as mock_run:
            
            goal_doc = {
                "_id": "12345_67_789",
                "team_name": "Real Madrid",
                "player_name": "Karim Benzema",
                "minute": 67,
                "fixture_id": 12345
            }
            mock_store.goals_pending.find_one.return_value = goal_doc
            mock_store.goals_pending.replace_one.return_value = Mock()
            mock_search.return_value = sample_twitter_videos
            mock_run.return_value = Mock()
            
            from found_footy.flows.twitter_flow import twitter_flow
            twitter_result = twitter_flow(goal_id="12345_67_789")
            
            assert twitter_result["status"] == "completed"
            assert twitter_result["search_method"] == "simplified_search"
            assert twitter_result["next_step"] == "download_flow"
        
        # Test Download Flow
        with patch('found_footy.flows.download_flow.store') as mock_store, \
             patch('found_footy.flows.download_flow.s3_store') as mock_s3:
            
            goal_doc_with_videos = {
                "_id": "12345_67_789",
                "team_name": "Real Madrid",
                "player_name": "Karim Benzema",
                "minute": 67,
                "fixture_id": 12345,
                "discovered_videos": [
                    {
                        "video_url": "https://video.twimg.com/test1.mp4",
                        "search_term": "Benzema Real Madrid",
                        "search_index": 0,
                        "video_index": 0
                    }
                ]
            }
            mock_store.goals_pending.find_one.return_value = goal_doc_with_videos
            mock_store.goals_processed.replace_one.return_value = Mock()
            mock_store.goals_pending.delete_one.return_value = Mock()
            
            mock_s3.download_video_from_url.return_value = {
                "status": "success",
                "s3_info": {
                    "s3_key": "fixtures/12345/goals/12345_67_789_0_0.mp4",
                    "s3_url": "http://localhost:9000/footy-videos/test.mp4",
                    "bucket": "footy-videos",
                    "file_size": 1024000,
                    "metadata": {}
                }
            }
            mock_s3.bucket_name = "footy-videos"
            mock_s3.get_bucket_stats.return_value = {"total_videos": 10, "total_size_mb": 50.0}
            
            from found_footy.flows.download_flow import download_flow
            download_result = download_flow(goal_id="12345_67_789")
            
            assert download_result["status"] == "completed"
            assert download_result["storage_backend"] == "s3"
    
    def test_fixture_lifecycle_integration(self, sample_fixture_data):
        """Test complete fixture lifecycle"""
        
        # ✅ FIX: Mock the task functions, not the flow functions
        with patch('found_footy.flows.shared_tasks.fixtures_process_parameters_task') as mock_params, \
             patch('found_footy.flows.shared_tasks.fixtures_fetch_api_task') as mock_fetch, \
             patch('found_footy.flows.shared_tasks.fixtures_categorize_task') as mock_categorize, \
             patch('found_footy.flows.shared_tasks.fixtures_store_task') as mock_store, \
             patch('found_footy.flows.ingest_flow.schedule_advance_flow') as mock_schedule:
            
            mock_params.return_value = {
                "query_date": datetime.now().date(),
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
            ingest_result = ingest_flow()

            assert ingest_result["status"] == "success"
            assert ingest_result["staging_fixtures"] == 1
            assert ingest_result["scheduled_advances"] == 1
    
    def test_error_handling_integration(self, sample_goal_data):
        """Test error handling across the complete pipeline"""
        
        # Test goal flow with storage failure
        with patch('found_footy.flows.shared_tasks.store') as mock_store, \
             patch('found_footy.flows.goal_flow.schedule_twitter_flow') as mock_schedule:
            
            mock_store.store_goal_pending.return_value = False  # Storage fails
            
            from found_footy.flows.goal_flow import goal_flow
            result = goal_flow(fixture_id=12345, goal_events=[sample_goal_data])
            
            assert result["status"] == "success"
            assert result["goals_processed"] == 0
            assert result["twitter_flows_scheduled"] == 0
    
    def test_concurrent_goal_processing(self, sample_goal_data):
        """Test handling of multiple concurrent goals"""
        
        # Create multiple goal events with different times and players
        goal_events = []
        for i in range(3):
            goal_event = sample_goal_data.copy()
            goal_event["time"]["elapsed"] = 45 + i * 10  # Different minutes
            goal_event["player"]["id"] = 789 + i  # Different players
            goal_event["player"]["name"] = f"Player {i+1}"
            goal_events.append(goal_event)
        
        with patch('found_footy.flows.shared_tasks.store') as mock_store, \
             patch('found_footy.flows.goal_flow.schedule_twitter_flow') as mock_schedule:
            
            mock_store.store_goal_pending.return_value = True
            mock_schedule.return_value = {"status": "scheduled"}
            
            from found_footy.flows.goal_flow import goal_flow
            result = goal_flow(fixture_id=12345, goal_events=goal_events)
            
            assert result["status"] == "success"
            assert result["goals_processed"] == 3
            assert result["twitter_flows_scheduled"] == 3
            
            # Verify all goals were processed
            assert mock_store.store_goal_pending.call_count == 3
            assert mock_schedule.call_count == 3