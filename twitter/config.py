"""Configuration for Twitter service"""
import os
from dataclasses import dataclass


@dataclass
class TwitterConfig:
    """Twitter service configuration loaded from environment variables"""
    
    # Authentication
    username: str = os.getenv('TWITTER_USERNAME', 'REDACTED_USERNAME')
    password: str = os.getenv('TWITTER_PASSWORD', 'REDACTED_PASSWORD')
    email: str = os.getenv('TWITTER_EMAIL', 'REDACTED_EMAIL')
    
    # Session management
    cookies_file: str = os.getenv('TWITTER_COOKIES_FILE', '/data/twitter_cookies.pkl')
    session_timeout: int = int(os.getenv('SESSION_TIMEOUT', 3600))  # 1 hour
    
    # Browser settings
    headless: bool = os.getenv('TWITTER_HEADLESS', 'true').lower() == 'true'
    display: str = os.getenv('DISPLAY', ':99')
    
    # Chrome driver
    chromedriver_path: str = os.getenv('CHROMEDRIVER_PATH', '/usr/bin/chromedriver')
    
    # Service settings
    host: str = os.getenv('TWITTER_SERVICE_HOST', '0.0.0.0')
    port: int = int(os.getenv('TWITTER_SERVICE_PORT', 8888))
    
    # Search defaults
    default_max_results: int = 3
    search_timeout: int = 5  # seconds to wait for page load
    
    def __post_init__(self):
        """Validate configuration"""
        if not self.username or not self.password:
            raise ValueError("TWITTER_USERNAME and TWITTER_PASSWORD must be set")
