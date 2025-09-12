"""Tests for Twitter API functions"""
import pytest
from unittest.mock import patch, Mock
from found_footy.api.twitter_api import twitter_videos_search, twitter_video_metadata

class TestTwitterAPI:
    """Test suite for Twitter API functions"""
    
    @patch('found_footy.api.twitter_api.TwitterAPI._search_nitter_basic')
    def test_twitter_videos_search_success(self, mock_search, sample_twitter_videos):
        """Test successful Twitter video search"""
        mock_search.return_value = sample_twitter_videos
        
        result = twitter_videos_search("Messi goal", max_results=5)
        
        assert len(result) == 1
        assert result[0]["tweet_text"] == "Amazing goal by Messi!"
        assert result[0]["url_validated"] is True
        mock_search.assert_called_once()
    
    @patch('found_footy.api.twitter_api.TwitterAPI._search_nitter_basic')
    @patch('found_footy.api.twitter_api.TwitterAPI._search_twitter_simulation')
    def test_twitter_videos_search_fallback(self, mock_simulation, mock_nitter, sample_twitter_videos):
        """Test Twitter search falls back to simulation when scraping fails"""
        mock_nitter.return_value = []  # No results from real scraping
        mock_simulation.return_value = sample_twitter_videos
        
        result = twitter_videos_search("Messi goal", max_results=5)
        
        assert len(result) == 1
        mock_nitter.assert_called_once()
        mock_simulation.assert_called_once()
    
    def test_twitter_video_metadata(self):
        """Test video metadata extraction"""
        test_url = "https://video.twimg.com/ext_tw_video/test.mp4"
        
        result = twitter_video_metadata(test_url)
        
        assert "resolution" in result
        assert "duration" in result
        assert "format" in result
        assert result["format"] == "mp4"