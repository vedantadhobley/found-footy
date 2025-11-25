"""Twitter Session Manager - Browser automation for Twitter scraping

Manages persistent Selenium browser session with authentication.
Uses Firefox with saved profile to bypass Twitter's bot detection.
"""
import os
import time
import threading
from typing import List, Dict, Any, Optional
from selenium import webdriver
from selenium.webdriver.firefox.options import Options as FirefoxOptions
from selenium.webdriver.firefox.service import Service as FirefoxService
from selenium.webdriver.common.by import By
from selenium.common.exceptions import WebDriverException
from urllib.parse import quote

from .config import TwitterConfig
from .auth import TwitterAuthenticator


class TwitterSessionManager:
    """Singleton browser session manager for Twitter scraping"""
    
    _instance = None
    _lock = threading.Lock()
    
    def __new__(cls, config: TwitterConfig = None):
        if cls._instance is None:
            with cls._lock:
                if cls._instance is None:
                    cls._instance = super().__new__(cls)
                    cls._instance._initialized = False
        return cls._instance
    
    def __init__(self, config: TwitterConfig = None):
        if self._initialized:
            return
        
        self.config = config or TwitterConfig()
        self.authenticator = TwitterAuthenticator(self.config)
        self.driver: Optional[webdriver.Chrome] = None
        self.authenticated = False
        self.last_activity = 0
        self.startup_complete = False
        self._initialized = True
        
        print("🔧 TwitterSessionManager initialized")
    
    def _setup_browser(self, headless: bool = None) -> bool:
        """Setup Chrome browser with appropriate options
        
        Args:
            headless: Run in headless mode (default: from config)
            
        Returns:
            True if browser setup successful, False otherwise
        """
        if headless is None:
            headless = self.config.headless
        
        try:
            # Use Firefox with saved profile from manual login
            options = FirefoxOptions()
            
            if headless:
                options.add_argument("--headless")
            
            # Use the profile from manual Firefox login
            profile_dir = "/data/firefox_profile"
            if os.path.exists(profile_dir):
                options.add_argument("-profile")
                options.add_argument(profile_dir)
                print(f"   📁 Using saved Firefox profile: {profile_dir}")
            else:
                print(f"   ⚠️  No saved profile found at {profile_dir}")
                print(f"   Run: docker compose -f docker-compose.dev.yml exec twitter python -m twitter.firefox_manual_setup")
                return False
            
            # Create Firefox instance
            service = FirefoxService(executable_path="/usr/local/bin/geckodriver")
            self.driver = webdriver.Firefox(service=service, options=options)
            
            mode = "headless" if headless else "GUI"
            print(f"✅ Browser session created ({mode} mode, Firefox)")
            return True
            
        except Exception as e:
            print(f"❌ Browser setup failed: {e}")
            import traceback
            traceback.print_exc()
            return False
    
    def authenticate(self, force_reauth: bool = False, allow_interactive: bool = False) -> bool:
        """Authenticate with Twitter
        
        Args:
            force_reauth: Force re-authentication even if already authenticated
            allow_interactive: Allow interactive manual login (requires GUI)
            
        Returns:
            True if authenticated, False otherwise
        """
        # Check if already authenticated and session is valid
        if self.authenticated and not force_reauth and self._session_valid():
            print("✅ Already authenticated - session valid")
            return True
        
        # Close existing browser if force reauth
        if force_reauth and self.driver:
            try:
                self.driver.quit()
            except:
                pass
            self.driver = None
        
        # Setup browser if needed
        if not self.driver:
            headless = not allow_interactive  # Use GUI for interactive
            if not self._setup_browser(headless=headless):
                return False
        
        # Authenticate using the authenticator
        success = self.authenticator.authenticate(
            self.driver, 
            force_reauth=force_reauth,
            allow_interactive=allow_interactive
        )
        
        if success:
            self.authenticated = True
            self.last_activity = time.time()
            print("✅ Twitter authentication successful")
        else:
            self.authenticated = False
            print("❌ Twitter authentication failed")
        
        return success
    
    def _session_valid(self) -> bool:
        """Check if current session is still valid
        
        Returns:
            True if session is valid, False otherwise
        """
        if not self.driver or not self.authenticated:
            return False
        
        # Check timeout
        if time.time() - self.last_activity > self.config.session_timeout:
            print("⚠️  Session timeout exceeded")
            return False
        
        # Check if browser is still alive
        try:
            _ = self.driver.current_url
            return True
        except WebDriverException:
            print("⚠️  Browser session lost")
            self.authenticated = False
            return False
    
    def search_videos(self, search_query: str, max_results: int = None) -> List[Dict[str, Any]]:
        """Search Twitter for videos matching query
        
        Args:
            search_query: Search terms (e.g., "Messi Barcelona")
            max_results: Maximum number of videos to return (default: from config)
            
        Returns:
            List of video dictionaries with metadata
        """
        if max_results is None:
            max_results = self.config.default_max_results
        
        # Ensure authenticated
        if not self.authenticate():
            print("❌ Cannot search - authentication failed")
            return []
        
        try:
            # Build search URL with video filter
            video_search_query = f"{search_query} filter:videos"
            search_url = f"https://twitter.com/search?q={quote(video_search_query)}&src=typed_query&f=live"
            
            print(f"🔍 Searching: {search_url}")
            self.driver.get(search_url)
            time.sleep(self.config.search_timeout)
            self.last_activity = time.time()
            
            # Extract tweet elements
            tweet_elements = self.driver.find_elements(By.CSS_SELECTOR, "article[data-testid='tweet']")
            print(f"🔍 Found {len(tweet_elements)} tweets to check")
            
            discovered_videos = []
            
            for i, tweet_element in enumerate(tweet_elements[:20]):  # Check up to 20 tweets
                try:
                    # Extract tweet text
                    tweet_text = "Text not found"
                    try:
                        text_element = tweet_element.find_element(By.CSS_SELECTOR, "[data-testid='tweetText']")
                        if text_element:
                            tweet_text = text_element.text.strip()
                    except:
                        pass
                    
                    # Extract tweet URL
                    tweet_url = None
                    try:
                        status_links = tweet_element.find_elements(By.CSS_SELECTOR, "a[href*='/status/']")
                        for link in status_links:
                            link_href = link.get_attribute("href")
                            if link_href and "/status/" in link_href:
                                tweet_url = link_href
                                break
                    except:
                        pass
                    
                    # Check for video presence
                    has_video = False
                    video_selectors = [
                        "video",
                        "[data-testid='videoPlayer']",
                        "[data-testid='videoComponent']"
                    ]
                    
                    for selector in video_selectors:
                        try:
                            if tweet_element.find_elements(By.CSS_SELECTOR, selector):
                                has_video = True
                                break
                        except:
                            continue
                    
                    # Add video if found
                    if has_video and tweet_url:
                        tweet_id = tweet_url.split("/status/")[-1] if "/status/" in tweet_url else f"unknown_{i}"
                        
                        video_entry = {
                            "search_term": search_query,
                            "tweet_url": tweet_url,
                            "tweet_id": tweet_id,
                            "tweet_text": tweet_text[:200],  # Truncate to 200 chars
                            "username": "Unknown",  # Could extract if needed
                            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ"),
                            "discovered_at": time.strftime("%Y-%m-%dT%H:%M:%SZ"),
                            "search_index": 0,
                            "video_index": len(discovered_videos),
                            "source": "browser_automation",
                            "requires_ytdlp": True,
                            "video_page_url": f"https://x.com/i/status/{tweet_id}"
                        }
                        
                        discovered_videos.append(video_entry)
                        print(f"✅ Found video #{len(discovered_videos)}: {tweet_text[:50]}...")
                        
                        if len(discovered_videos) >= max_results:
                            break
                
                except Exception as e:
                    # Skip problematic tweets
                    continue
            
            print(f"✅ Search complete - found {len(discovered_videos)} videos")
            return discovered_videos
            
        except Exception as e:
            print(f"❌ Search failed: {e}")
            import traceback
            traceback.print_exc()
            return []
    
    def startup(self) -> bool:
        """Startup routine - Launch Firefox with saved profile and verify login
        
        Returns:
            True if authenticated, False otherwise
        """
        print("🚀 Starting Twitter Session Service...")
        print("   📺 VNC GUI available at: http://localhost:6080/vnc.html")
        print()
        
        with self._lock:
            self.startup_complete = True
            
            # Check if Firefox profile exists
            profile_dir = "/data/firefox_profile"
            if not os.path.exists(profile_dir):
                print("   ⚠️  No Firefox profile found")
                print("   Run once: docker compose -f docker-compose.dev.yml exec twitter python -m twitter.firefox_manual_setup")
                print()
                return False
            
            # Launch Firefox with saved profile
            print("🦊 Launching Firefox with saved profile...")
            if not self._setup_browser(headless=self.config.headless):
                print("❌ Failed to launch browser")
                return False
            
            # Verify we're logged in
            print("🔍 Verifying Twitter login...")
            try:
                self.driver.get("https://x.com/home")
                time.sleep(3)
                
                current_url = self.driver.current_url
                if "home" in current_url and "login" not in current_url:
                    self.authenticated = True
                    self.last_activity = time.time()
                    print("✅ Already logged in via saved profile!")
                    print()
                    return True
                else:
                    # Session expired - try automated login
                    print("⚠️  Session expired, attempting automated login...")
                    if self.authenticator.automated_login(self.driver):
                        self.authenticated = True
                        self.last_activity = time.time()
                        print("✅ Automated login successful!")
                        print()
                        return True
                    else:
                        print("❌ Automated login failed")
                        print("   Run: docker compose -f docker-compose.dev.yml exec twitter python -m twitter.firefox_manual_setup")
                        print()
                        return False
                        
            except Exception as e:
                print(f"❌ Startup failed: {e}")
                return False
    
    def cleanup(self):
        """Cleanup browser session"""
        if self.driver:
            try:
                self.driver.quit()
                print("🧹 Browser session closed")
            except:
                pass
        
        self.authenticated = False
        self.startup_complete = False
