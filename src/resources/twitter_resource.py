"""Twitter session resource for Dagster"""
import sys
from pathlib import Path
from typing import Any


class TwitterResource:
    """
    Wrapper for Twitter session to use in Dagster ops.
    
    Uses TwitterSessionManager from the twitter/ directory.
    Lazy loads the twitter module to avoid import errors.
    """
    
    def __init__(self):
        self._session = None
        self._session_manager_class = None
        self._config_class = None
    
    def _ensure_imports(self):
        """Ensure twitter session is imported and available"""
        if self._session_manager_class is None:
            # Add twitter directory to path
            twitter_path = str(Path(__file__).parent.parent.parent / "twitter")
            if twitter_path not in sys.path:
                sys.path.insert(0, twitter_path)
            
            from twitter.config import TwitterConfig
            from twitter.session import TwitterSessionManager
            
            self._session_manager_class = TwitterSessionManager
            self._config_class = TwitterConfig
    
    def get_session(self) -> Any:
        """Get or create Twitter session (lazy init)"""
        self._ensure_imports()
        if self._session is None:
            # Verify imports succeeded
            if self._session_manager_class is None or self._config_class is None:
                raise RuntimeError("Failed to import Twitter session dependencies")
            
            # Create config and session manager
            config = self._config_class()
            self._session = self._session_manager_class(config)
            
            # Authenticate the session
            if not self._session.authenticate():
                print("⚠️  Twitter authentication failed")
        return self._session
    
    def search_videos(self, search_query: str, max_results: int = 3):
        """
        Search for videos on Twitter.
        
        Args:
            search_query: The search query
            max_results: Maximum number of results
            
        Returns:
            List of video metadata dicts
        """
        session = self.get_session()
        return session.search_videos(search_query, max_results)
    
    def close(self):
        """Close the session"""
        if self._session and hasattr(self._session, 'driver') and self._session.driver:
            try:
                self._session.driver.quit()
            except Exception:
                pass
            self._session = None


# Singleton instance
_twitter_resource = None


def get_twitter_resource() -> TwitterResource:
    """Get global Twitter resource singleton"""
    global _twitter_resource
    if _twitter_resource is None:
        _twitter_resource = TwitterResource()
    return _twitter_resource
