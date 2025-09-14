"""Persistent Twitter Browser Session Manager"""
import os
import time
import threading
from selenium import webdriver
from selenium.webdriver.chrome.options import Options
from selenium.webdriver.chrome.service import Service
from selenium.webdriver.common.by import By
from selenium.webdriver.support.ui import WebDriverWait
from selenium.webdriver.support import expected_conditions as EC
from selenium.common.exceptions import TimeoutException, NoSuchElementException, WebDriverException

class TwitterSessionManager:
    """Singleton browser session manager for Twitter authentication"""
    
    _instance = None
    _lock = threading.Lock()
    
    def __new__(cls):
        if cls._instance is None:
            with cls._lock:
                if cls._instance is None:
                    cls._instance = super().__new__(cls)
                    cls._instance._initialized = False
        return cls._instance
    
    def __init__(self):
        if self._initialized:
            return
            
        self.driver = None
        self.authenticated = False
        self.last_activity = 0
        self.session_timeout = 3600  # 1 hour session timeout
        self._initialized = True
        print("🔧 TwitterSessionManager initialized")
    
    def _setup_browser(self):
        """Setup persistent browser with optimized settings"""
        try:
            chrome_options = Options()
            chrome_options.add_argument("--headless")
            chrome_options.add_argument("--no-sandbox")
            chrome_options.add_argument("--disable-dev-shm-usage")
            chrome_options.add_argument("--disable-gpu")
            chrome_options.add_argument("--window-size=1920,1080")
            chrome_options.add_argument("--disable-web-security")
            chrome_options.add_argument("--disable-features=VizDisplayCompositor")
            chrome_options.add_argument("--disable-extensions")
            chrome_options.add_argument("--disable-plugins")
            # Keep JavaScript enabled for Twitter functionality
            chrome_options.add_argument("--user-agent=Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
            
            # ✅ PERSISTENCE: Use user data directory to maintain session
            chrome_options.add_argument("--user-data-dir=/tmp/twitter_session")
            chrome_options.add_argument("--disable-background-timer-throttling")
            chrome_options.add_argument("--disable-renderer-backgrounding")
            
            service = Service("/usr/bin/chromedriver")
            self.driver = webdriver.Chrome(service=service, options=chrome_options)
            
            print("✅ Persistent browser session created")
            return True
            
        except Exception as e:
            print(f"❌ Browser setup failed: {e}")
            return False
    
    def authenticate(self, force_reauth=False):
        """Authenticate with Twitter (only if needed)"""
        if self.authenticated and not force_reauth and self._session_valid():
            print("✅ Already authenticated - using existing session")
            return True
        
        if not self.driver:
            if not self._setup_browser():
                return False
        
        try:
            print("🔐 Authenticating with Twitter...")
            
            # Check if already logged in
            self.driver.get("https://twitter.com/home")
            time.sleep(3)
            
            # Check if we're already on the home page (logged in)
            current_url = self.driver.current_url
            if "home" in current_url and "login" not in current_url:
                print("✅ Already logged in from previous session!")
                self.authenticated = True
                self.last_activity = time.time()
                return True
            
            # Need to log in
            print("🔐 Performing fresh login...")
            return self._perform_login()
            
        except Exception as e:
            print(f"❌ Authentication failed: {e}")
            self.authenticated = False
            return False
    
    def _perform_login(self):
        """Perform actual login process"""
        try:
            twitter_username = os.getenv('TWITTER_USERNAME', 'REDACTED_USERNAME')
            twitter_password = os.getenv('TWITTER_PASSWORD', 'REDACTED_PASSWORD')
            twitter_email = os.getenv('TWITTER_EMAIL', 'REDACTED_EMAIL')
            
            # Navigate to login
            self.driver.get("https://twitter.com/login")
            
            # Enter username
            username_input = WebDriverWait(self.driver, 10).until(
                EC.presence_of_element_located((By.NAME, "text"))
            )
            username_input.clear()
            username_input.send_keys(twitter_username)
            
            next_button = self.driver.find_element(By.XPATH, "//span[text()='Next']/..")
            next_button.click()
            time.sleep(3)
            
            # Handle email verification if needed
            try:
                email_input = self.driver.find_element(By.NAME, "text")
                print("📧 Email verification required")
                email_input.clear()
                email_input.send_keys(twitter_email)
                
                next_button = self.driver.find_element(By.XPATH, "//span[text()='Next']/..")
                next_button.click()
                time.sleep(3)
            except NoSuchElementException:
                print("✅ No email verification needed")
            
            # Enter password
            password_input = WebDriverWait(self.driver, 10).until(
                EC.presence_of_element_located((By.NAME, "password"))
            )
            password_input.clear()
            password_input.send_keys(twitter_password)
            
            login_button = self.driver.find_element(By.XPATH, "//span[text()='Log in']/..")
            login_button.click()
            
            time.sleep(5)
            
            # Verify login success
            current_url = self.driver.current_url
            if "home" in current_url or ("twitter.com" in current_url and "login" not in current_url):
                print("✅ Login successful!")
                self.authenticated = True
                self.last_activity = time.time()
                return True
            else:
                print(f"⚠️ Login may have failed. Current URL: {current_url}")
                return False
                
        except Exception as e:
            print(f"❌ Login process failed: {e}")
            return False
    
    def _session_valid(self):
        """Check if current session is still valid"""
        if not self.driver or not self.authenticated:
            return False
        
        # Check session timeout
        if time.time() - self.last_activity > self.session_timeout:
            print("⏰ Session timeout - need to re-authenticate")
            return False
        
        # Quick browser health check
        try:
            self.driver.current_url
            return True
        except WebDriverException:
            print("❌ Browser session is dead")
            self.authenticated = False
            return False
    
    def search_videos(self, search_query, max_results=3):
        """Search for videos using authenticated session"""
        if not self.authenticate():
            print("❌ Authentication failed - cannot search")
            return []
        
        try:
            search_url = f"https://twitter.com/search?q={search_query.replace(' ', '%20')}%20filter%3Avideos&src=typed_query&f=live"
            print(f"🔍 Searching: {search_url}")
            
            self.driver.get(search_url)
            time.sleep(5)
            
            # Update activity timestamp
            self.last_activity = time.time()
            
            # Extract videos (use your existing logic)
            tweet_elements = self.driver.find_elements(By.CSS_SELECTOR, "article[data-testid='tweet']")
            print(f"🔍 Found {len(tweet_elements)} tweets")
            
            discovered_videos = []
            
            for i, tweet_element in enumerate(tweet_elements[:max_results]):
                try:
                    # Look for video indicators (your existing logic)
                    video_selectors = [
                        "[data-testid='videoPlayer']",
                        "video",
                        "[aria-label*='video']",
                        "[aria-label*='Video']"
                    ]
                    
                    has_video = False
                    for selector in video_selectors:
                        if tweet_element.find_elements(By.CSS_SELECTOR, selector):
                            has_video = True
                            break
                    
                    if has_video:
                        # Extract tweet data (your existing logic)
                        tweet_url = ""
                        link_elements = tweet_element.find_elements(By.CSS_SELECTOR, "a[href*='/status/']")
                        if link_elements:
                            href = link_elements[0].get_attribute("href")
                            if href and "/status/" in href:
                                tweet_url = href
                        
                        if tweet_url:
                            tweet_id = tweet_url.split("/status/")[1].split("?")[0]
                            
                            # Extract text
                            tweet_text = ""
                            text_elements = tweet_element.find_elements(By.CSS_SELECTOR, "[data-testid='tweetText']")
                            if text_elements:
                                tweet_text = " ".join([elem.text.strip() for elem in text_elements if elem.text.strip()])
                            
                            # Extract username
                            username = "Unknown"
                            username_elements = tweet_element.find_elements(By.CSS_SELECTOR, "[data-testid='User-Name']")
                            if username_elements:
                                username_text = username_elements[0].text
                                if "@" in username_text:
                                    username = username_text.split("@")[1].split()[0]
                                    username = f"@{username}"
                            
                            video_entry = {
                                "search_term": search_query,
                                "tweet_url": tweet_url,
                                "tweet_id": tweet_id,
                                "tweet_text": tweet_text,
                                "username": username,
                                "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ"),
                                "discovered_at": time.strftime("%Y-%m-%dT%H:%M:%SZ"),
                                "search_index": 0,
                                "video_index": len(discovered_videos),
                                "source": "persistent_browser",
                                "requires_ytdlp": True,
                                "video_page_url": f"https://x.com/i/status/{tweet_id}"
                            }
                            discovered_videos.append(video_entry)
                            print(f"   ✅ Added video #{len(discovered_videos)}: {tweet_url}")
                
                except Exception as e:
                    print(f"   ⚠️ Error processing tweet #{i+1}: {e}")
                    continue
            
            print(f"✅ Session search completed: {len(discovered_videos)} videos")
            return discovered_videos
            
        except Exception as e:
            print(f"❌ Search failed: {e}")
            return []
    
    def cleanup(self):
        """Clean up browser session"""
        if self.driver:
            try:
                self.driver.quit()
                print("✅ Browser session cleaned up")
            except:
                pass
            self.driver = None
        self.authenticated = False

# Global session instance
twitter_session = TwitterSessionManager()