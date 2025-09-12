"""Integration tests for complete workflows"""
import pytest
import sys
from unittest.mock import patch, Mock, MagicMock

# ✅ REAL FIX: Clear module cache and patch BEFORE any imports
@pytest.fixture(autouse=True)
def clear_module_cache():
    """Clear module cache before each test to ensure fresh imports"""
    modules_to_clear = [
        'found_footy.flows.goal_flow',
        'found_footy.flows.twitter_flow', 
        'found_footy.flows.download_flow',
        'found_footy.flows.shared_tasks'
    ]
    
    for module in modules_to_clear:
        if module in sys.modules:
            del sys.modules[module]
    
    yield
    
    # Clean up after test
    for module in modules_to_clear:
        if module in sys.modules:
            del sys.modules[module]

class TestIntegration:
    """Integration tests for complete workflows"""
    
    def test_goal_flow_complete_workflow(self, sample_goal_data):
        """Test complete goal processing workflow"""
        # ✅ REAL FIX: Patch at import time, then import
        with patch('found_footy.storage.mongo_store.FootyMongoStore') as mock_store_class:
            # Setup the mock BEFORE any imports
            mock_store_instance = Mock()
            mock_store_instance.store_goal_pending.return_value = True
            mock_store_class.return_value = mock_store_instance
            
            # Now patch the flow triggers  
            with patch('found_footy.flows.flow_triggers.schedule_twitter_flow') as mock_schedule:
                mock_schedule.return_value = {
                    "status": "scheduled", 
                    "scheduled_time": "2025-01-15T15:05:00",
                    "delay_minutes": 5
                }
                
                # Import AFTER patching
                from found_footy.flows.goal_flow import goal_flow
                
                # Execute the flow
                result = goal_flow(fixture_id=12345, goal_events=[sample_goal_data])
                
                # Verify results
                assert result["status"] == "success"
                assert result["goals_processed"] == 1
                assert result["twitter_flows_scheduled"] == 1
                assert result["delay_minutes"] == 5
                
                # Verify mocks were called
                mock_store_instance.store_goal_pending.assert_called_once()
                mock_schedule.assert_called_once()
    
    def test_twitter_flow_complete_workflow(self, sample_twitter_videos):
        """Test complete Twitter search workflow"""
        # ✅ REAL FIX: Patch MongoDB and Twitter API
        with patch('found_footy.storage.mongo_store.FootyMongoStore') as mock_store_class, \
             patch('found_footy.api.twitter_api.twitter_videos_search') as mock_search, \
             patch('prefect.deployments.run_deployment') as mock_run_deployment:
            
            # Setup MongoDB mock
            mock_store_instance = Mock()
            goal_doc = {
                "_id": "12345_67_789",
                "team_name": "Barcelona", 
                "player_name": "Lionel Messi",
                "minute": 67,
                "fixture_id": 12345
            }
            mock_store_instance.goals_pending.find_one.return_value = goal_doc
            mock_store_instance.goals_pending.replace_one.return_value = Mock()
            mock_store_class.return_value = mock_store_instance
            
            # Setup Twitter API mock
            mock_search.return_value = sample_twitter_videos
            mock_run_deployment.return_value = Mock()
            
            # Import AFTER patching
            from found_footy.flows.twitter_flow import twitter_flow
            
            # Execute the flow
            result = twitter_flow(goal_id="12345_67_789")
            
            # Verify results - ✅ FIX: Use correct search method
            assert result["status"] == "completed"
            assert result["search_method"] == "simplified_search"  # Updated expectation
            
            # Verify mocks were called
            mock_search.assert_called_once()
            mock_run_deployment.assert_called_once()
    
    def test_download_flow_integration(self):
        """Test download flow integration with S3"""
        with patch('found_footy.storage.mongo_store.FootyMongoStore') as mock_store_class, \
             patch('found_footy.storage.s3_store.FootyS3Store') as mock_s3_class:
            
            # Setup MongoDB mock
            mock_store_instance = Mock()
            goal_doc = {
                "_id": "12345_67_789",
                "team_name": "Barcelona",
                "player_name": "Lionel Messi", 
                "minute": 67,
                "fixture_id": 12345,
                "discovered_videos": [
                    {
                        "video_url": "https://video.twimg.com/test.mp4",
                        "search_term": "Messi Barcelona",
                        "search_index": 0,
                        "video_index": 0
                    }
                ]
            }
            mock_store_instance.goals_pending.find_one.return_value = goal_doc
            mock_store_instance.goals_processed.replace_one.return_value = Mock()
            mock_store_instance.goals_pending.delete_one.return_value = Mock()
            mock_store_class.return_value = mock_store_instance
            
            # Setup S3 mock
            mock_s3_instance = Mock()
            mock_s3_instance.download_video_from_url.return_value = {
                "status": "success",
                "s3_info": {
                    "s3_key": "fixtures/12345/goals/12345_67_789_0_0.mp4",
                    "s3_url": "http://localhost:9000/footy-videos/test.mp4",
                    "bucket": "footy-videos",
                    "file_size": 1024,
                    "metadata": {}
                }
            }
            mock_s3_instance.bucket_name = "footy-videos"
            mock_s3_instance.get_bucket_stats.return_value = {"total_videos": 10, "total_size_mb": 50.0}
            mock_s3_class.return_value = mock_s3_instance
            
            # Import AFTER patching
            from found_footy.flows.download_flow import download_flow
            
            # Execute the flow
            result = download_flow(goal_id="12345_67_789")
            
            # Verify results
            assert result["status"] == "completed"
            assert result["storage_backend"] == "s3"
            
            # Verify S3 mock was called
            mock_s3_instance.download_video_from_url.assert_called_once()
    
    def test_download_flow_multiple_videos(self):
        """Test download flow with multiple discovered videos"""
        with patch('found_footy.storage.mongo_store.FootyMongoStore') as mock_store_class, \
             patch('found_footy.storage.s3_store.FootyS3Store') as mock_s3_class:
            
            # Setup MongoDB mock
            mock_store_instance = Mock()
            goal_doc = {
                "_id": "12345_67_789",
                "team_name": "Barcelona",
                "player_name": "Lionel Messi", 
                "minute": 67,
                "fixture_id": 12345,
                "discovered_videos": [
                    {
                        "video_url": "https://video.twimg.com/test1.mp4",
                        "search_term": "Messi Barcelona",
                        "search_index": 0,
                        "video_index": 0
                    },
                    {
                        "video_url": "https://video.twimg.com/test2.mp4",
                        "search_term": "Messi Barcelona",
                        "search_index": 0,
                        "video_index": 1
                    }
                ]
            }
            mock_store_instance.goals_pending.find_one.return_value = goal_doc
            mock_store_instance.goals_processed.replace_one.return_value = Mock()
            mock_store_instance.goals_pending.delete_one.return_value = Mock()
            mock_store_class.return_value = mock_store_instance
            
            # Setup S3 mock
            mock_s3_instance = Mock()
            mock_s3_instance.download_video_from_url.return_value = {
                "status": "success",
                "s3_info": {
                    "s3_key": "fixtures/12345/goals/12345_67_789_0_0.mp4",
                    "s3_url": "http://localhost:9000/footy-videos/test.mp4",
                    "bucket": "footy-videos",
                    "file_size": 1024,
                    "metadata": {}
                }
            }
            mock_s3_instance.bucket_name = "footy-videos"
            mock_s3_instance.get_bucket_stats.return_value = {"total_videos": 10, "total_size_mb": 50.0}
            mock_s3_class.return_value = mock_s3_instance
            
            # Import AFTER patching
            from found_footy.flows.download_flow import download_flow
            
            # Execute the flow
            result = download_flow(goal_id="12345_67_789")
            
            # Verify results
            assert result["status"] == "completed"
            assert result["storage_backend"] == "s3"
            
            # ✅ FIX: Verify correct number of download calls
            assert mock_s3_instance.download_video_from_url.call_count == 2
            
            # Verify download stats
            download_stats = result["download_result"]["download_stats"]
            assert download_stats["total_discovered"] == 2
            assert download_stats["successful"] == 2
            assert download_stats["failed"] == 0
    
    def test_goal_flow_duplicate_detection(self, sample_goal_data):
        """Test that goal flow handles duplicate goals correctly"""
        with patch('found_footy.storage.mongo_store.FootyMongoStore') as mock_store_class:
            # Setup mock to return False (duplicate detected)
            mock_store_instance = Mock()
            mock_store_instance.store_goal_pending.return_value = False
            mock_store_class.return_value = mock_store_instance
            
            with patch('found_footy.flows.flow_triggers.schedule_twitter_flow') as mock_schedule:
                # Import AFTER patching
                from found_footy.flows.goal_flow import goal_flow
                
                # Execute the flow
                result = goal_flow(fixture_id=12345, goal_events=[sample_goal_data])
                
                # Verify duplicate handling
                assert result["status"] == "success"
                assert result["goals_processed"] == 0  # No goals processed due to duplicates
                assert result["twitter_flows_scheduled"] == 0  # No flows scheduled
                
                # Verify store was called but schedule was not
                mock_store_instance.store_goal_pending.assert_called_once()
                mock_schedule.assert_not_called()
    
    def test_goal_flow_handles_empty_events(self):
        """Test goal flow handles empty goal events"""
        with patch('found_footy.storage.mongo_store.FootyMongoStore') as mock_store_class:
            mock_store_instance = Mock()
            mock_store_class.return_value = mock_store_instance
            
            # Import AFTER patching
            from found_footy.flows.goal_flow import goal_flow
            
            # Execute the flow with empty events
            result = goal_flow(fixture_id=12345, goal_events=[])
            
            # Verify empty handling
            assert result["status"] == "no_goals"
            assert result["fixture_id"] == 12345
            
            # Should not call store for empty events
            mock_store_instance.store_goal_pending.assert_not_called()
    
    def test_twitter_flow_goal_not_found(self):
        """Test twitter flow when goal is not found in database"""
        with patch('found_footy.storage.mongo_store.FootyMongoStore') as mock_store_class:
            # Setup mock to return None (goal not found)
            mock_store_instance = Mock()
            mock_store_instance.goals_pending.find_one.return_value = None
            mock_store_class.return_value = mock_store_instance
            
            # Import AFTER patching
            from found_footy.flows.twitter_flow import twitter_flow
            
            # Execute the flow
            result = twitter_flow(goal_id="12345_67_789")
            
            # Verify not found handling
            assert result["status"] == "completed"
            
            # Verify database was queried
            mock_store_instance.goals_pending.find_one.assert_called_once()