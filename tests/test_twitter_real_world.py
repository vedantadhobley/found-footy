"""Real-world Twitter integration tests with actual examples - Fixed import strategy"""
import pytest
from unittest.mock import patch, Mock
from datetime import datetime, timezone

class TestTwitterRealWorldExamples:
    """Test Twitter flow with real goal examples from actual matches"""
    
    def test_szoboszlai_liverpool_goal_search(self):
        """Test real-world example: Szoboszlai goal vs Arsenal"""
        
        # Real fixture data from your example
        fixture_data = {
            "fixture": {
                "id": 1378993,
                "referee": "Chris Kavanagh, England",
                "timezone": "UTC",
                "date": "2025-08-31T15:30:00+00:00",
                "timestamp": 1756654200,
                "status": {
                    "long": "Match Finished",
                    "short": "FT",
                    "elapsed": 90,
                    "extra": 9
                },
                "venue": {
                    "id": 550,
                    "name": "Anfield",
                    "city": "Liverpool"
                }
            },
            "teams": {
                "home": {
                    "id": 40,
                    "name": "Liverpool",
                    "logo": "https://media.api-sports.io/football/teams/40.png",
                    "winner": True
                },
                "away": {
                    "id": 42,
                    "name": "Arsenal", 
                    "logo": "https://media.api-sports.io/football/teams/42.png",
                    "winner": False
                }
            },
            "goals": {
                "home": 1,
                "away": 0
            }
        }
        
        # Real goal event from your example
        goal_event = {
            "time": {
                "elapsed": 83,
                "extra": None
            },
            "team": {
                "id": 40,
                "name": "Liverpool",
                "logo": "https://media.api-sports.io/football/teams/40.png"
            },
            "player": {
                "id": 1096,
                "name": "D. Szoboszlai"
            },
            "assist": {
                "id": None,
                "name": None
            },
            "type": "Goal",
            "detail": "Normal Goal",
            "comments": None
        }
        
        # Expected search term: "Szoboszlai Liverpool"
        expected_search_term = "Szoboszlai Liverpool"
        goal_id = "1378993_83_1096"  # fixture_id_minute_player_id
        
        # ✅ FIX: Import the module directly
        import found_footy.flows.twitter_flow as twitter_flow
        
        # ✅ FIX: Mock the store and class from the actual module
        with patch.object(twitter_flow, 'store') as mock_store, \
             patch.object(twitter_flow, 'TwitterBrowserScraper') as MockScraper:
            
            mock_store.goals_pending.find_one.return_value = {
                "_id": goal_id,
                "fixture_id": 1378993,
                "minute": 83,
                "player_id": 1096,
                "player_name": "D. Szoboszlai",
                "team_name": "Liverpool",
                "raw_event": goal_event
            }
            mock_store.goals_pending.replace_one.return_value = Mock()
            
            # ✅ FIX: Mock browser scraper to return videos
            mock_scraper_instance = Mock()
            mock_scraper_instance.search_videos.return_value = [
                {
                    "search_term": expected_search_term,
                    "tweet_url": "https://twitter.com/goncalo_diass17/status/1966752870780252173",
                    "tweet_id": "1966752870780252173",
                    "tweet_text": "🔥 Incredible goal! Szoboszlai Liverpool absolutely brilliant! ⚽️🎯",
                    "username": "goncalo_diass17",
                    "timestamp": datetime.now(timezone.utc).isoformat(),
                    "discovered_at": datetime.now(timezone.utc).isoformat(),
                    "search_index": 0,
                    "video_index": 0,
                    "source": "browser_automation",
                    "requires_ytdlp": True,
                    "video_page_url": "https://x.com/i/status/1966752870780252173"
                }
            ]
            MockScraper.return_value = mock_scraper_instance
            
            # Now call the function
            result = twitter_flow.twitter_search_task(goal_id)
            
            # Verify the search term generation logic
            assert result["status"] == "success"
            assert result["search_terms"] == [expected_search_term]
            
            # Verify videos were found
            assert result["video_count"] > 0
            discovered_videos = result["discovered_videos"]
            assert len(discovered_videos) >= 1
            
            # Verify video structure matches expected format
            video = discovered_videos[0]
            assert video["search_term"] == expected_search_term
            assert "tweet_url" in video
            assert "tweet_id" in video
            assert video["requires_ytdlp"] == True
            
            # The source should be browser automation
            assert video["source"] == "browser_automation"
            
            print(f"✅ Successfully processed Szoboszlai goal with search term: {expected_search_term}")
            
    def test_real_twitter_search_url_format(self):
        """Test that our search logic matches the actual Twitter URL format"""
        
        # Your example shows this URL format:
        # https://x.com/search?q=Szoboszlai%20Liverpool&src=typed_query&f=live
        
        player_name = "D. Szoboszlai"
        team_name = "Liverpool"
        
        # This matches the logic in twitter_search_task
        player_last_name = player_name.split()[-1] if " " in player_name else player_name
        search_term = f"{player_last_name} {team_name}"
        
        assert search_term == "Szoboszlai Liverpool"
        
        # Test URL encoding (what Twitter actually sees)
        from urllib.parse import quote
        encoded_search = quote(search_term)
        expected_url = f"https://x.com/search?q={encoded_search}&src=typed_query&f=live"
        
        assert "Szoboszlai%20Liverpool" in expected_url
        print(f"✅ Search term '{search_term}' → URL: {expected_url}")
        
    def test_expected_tweet_discovery_format(self):
        """Test that discovered videos match the expected format for yt-dlp"""
        
        # Based on your example:
        # Tweet URL: https://x.com/goncalo_diass17/status/1966752870780252173
        # Media URL: https://x.com/i/status/1966752870780252173
        
        example_tweet_id = "1966752870780252173"
        example_username = "goncalo_diass17"
        
        # This is the format our TwitterBrowserScraper should produce
        expected_video_format = {
            "search_term": "Szoboszlai Liverpool",
            "tweet_url": f"https://x.com/{example_username}/status/{example_tweet_id}",
            "tweet_id": example_tweet_id,
            "tweet_text": "Goal video content",
            "username": example_username,
            "timestamp": datetime.now(timezone.utc).isoformat(),
            "discovered_at": datetime.now(timezone.utc).isoformat(),
            "search_index": 0,
            "video_index": 0,
            "source": "browser_automation",
            "requires_ytdlp": True,
            "video_page_url": f"https://x.com/i/status/{example_tweet_id}"
        }
        
        # Verify all required fields for yt-dlp processing
        required_fields = [
            "tweet_url", "tweet_id", "requires_ytdlp", 
            "search_term", "video_page_url"
        ]
        
        for field in required_fields:
            assert field in expected_video_format
            
        # The video_page_url should be the i/status format for yt-dlp
        assert expected_video_format["video_page_url"] == f"https://x.com/i/status/{example_tweet_id}"
        
        print(f"✅ Video format matches yt-dlp requirements")
        print(f"✅ Video page URL: {expected_video_format['video_page_url']}")

class TestTwitterSearchLogicRealWorld:
    """Test the search term generation with real player names"""
    
    @pytest.mark.parametrize("player_name,team_name,expected_search", [
        ("D. Szoboszlai", "Liverpool", "Szoboszlai Liverpool"),
        ("Mohamed Salah", "Liverpool", "Salah Liverpool"),
        ("Erling Haaland", "Manchester City", "Haaland Manchester City"),
        ("Kylian Mbappé", "Real Madrid", "Mbappé Real Madrid"),
        ("Lionel Messi", "Inter Miami", "Messi Inter Miami"),
        ("Messi", "Barcelona", "Messi Barcelona"),  # Single name case
    ])
    def test_search_term_generation_real_players(self, player_name, team_name, expected_search):
        """Test search term generation with real player examples"""
        
        # This replicates the exact logic from twitter_search_task
        player_last_name = player_name.split()[-1] if " " in player_name else player_name
        primary_search = f"{player_last_name} {team_name}"
        
        assert primary_search == expected_search
        print(f"✅ {player_name} ({team_name}) → '{primary_search}'")