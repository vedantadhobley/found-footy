"""ISOLATED Twitter Session Service - NO DEPENDENCIES"""
import time
import threading
import os
from fastapi import FastAPI, HTTPException
from fastapi.responses import JSONResponse
from pydantic import BaseModel
from typing import List, Dict, Any
from selenium import webdriver
from selenium.webdriver.chrome.options import Options
from selenium.webdriver.chrome.service import Service
from selenium.webdriver.common.by import By
from selenium.webdriver.support.ui import WebDriverWait
from selenium.webdriver.support import expected_conditions as EC
from selenium.common.exceptions import TimeoutException, NoSuchElementException, WebDriverException
from contextlib import asynccontextmanager

class TwitterSessionManager:
    """ISOLATED Twitter session - ZERO external dependencies"""
    
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
        self.session_timeout = int(os.getenv('SESSION_TIMEOUT', 3600))
        self.startup_complete = False
        self._initialized = True
        
        print("🔧 TwitterSessionManager initialized (ZERO dependencies)")
    
    def _setup_browser(self):
        """Setup persistent browser with production settings"""
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
            chrome_options.add_argument("--user-agent=Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
            chrome_options.add_argument("--user-data-dir=/tmp/chrome_session/profile")
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
            
            # Check if already logged in via persistent session
            self.driver.get("https://twitter.com/home")
            time.sleep(3)
            
            current_url = self.driver.current_url
            if "home" in current_url and "login" not in current_url:
                print("✅ Already authenticated via persistent session")
                self.authenticated = True
                self.last_activity = time.time()
                return True
            
            # Need to perform fresh login
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
                email_input.clear()
                email_input.send_keys(twitter_email)
                next_button = self.driver.find_element(By.XPATH, "//span[text()='Next']/..")
                next_button.click()
                time.sleep(3)
            except NoSuchElementException:
                print("   ✅ No email verification needed")
            
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
                print("❌ Login failed - not redirected to home")
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
        """Search for videos using authenticated session - PURE FUNCTION"""
        if not self.authenticate():
            print("❌ Authentication failed - cannot search")
            return []
        
        try:
            from urllib.parse import quote
            
            # Search specifically for videos with filter
            video_search_query = f"{search_query} filter:videos"
            search_url = f"https://twitter.com/search?q={quote(video_search_query)}&src=typed_query&f=live"
            print(f"🎥 Searching for videos: {search_url}")
            
            self.driver.get(search_url)
            time.sleep(5)
            
            # Update activity timestamp
            self.last_activity = time.time()
            
            # Extract videos
            tweet_elements = self.driver.find_elements(By.CSS_SELECTOR, "article[data-testid='tweet']")
            print(f"🔍 Found {len(tweet_elements)} tweets to check for videos")
            
            discovered_videos = []
            
            for i, tweet_element in enumerate(tweet_elements[:20]):
                try:
                    # Extract tweet text
                    text_selectors = ["[data-testid='tweetText']", ".tweet-content", "[lang]"]
                    tweet_text = "Text not found"
                    for selector in text_selectors:
                        try:
                            text_element = tweet_element.find_element(By.CSS_SELECTOR, selector)
                            if text_element and text_element.text.strip():
                                tweet_text = text_element.text.strip()
                                break
                        except:
                            continue
                    
                    # Extract username and tweet URL
                    username_selectors = ["[data-testid='User-Name'] a", "[href^='/']"]
                    username = "Unknown"
                    tweet_url = None
                    for selector in username_selectors:
                        try:
                            username_element = tweet_element.find_element(By.CSS_SELECTOR, selector)
                            if username_element:
                                href = username_element.get_attribute("href")
                                if href and "/" in href:
                                    username_part = href.split("/")[-1]
                                    username = f"@{username_part}"
                                    
                                    # Try to find tweet ID from nearby status links
                                    try:
                                        status_links = tweet_element.find_elements(By.CSS_SELECTOR, "a[href*='/status/']")
                                        for link in status_links:
                                            link_href = link.get_attribute("href")
                                            if "/status/" in link_href:
                                                tweet_url = link_href
                                                break
                                    except:
                                        pass
                                    break
                        except:
                            continue
                    
                    # Check for video indicators
                    video_indicators = [
                        "video", "[data-testid='videoPlayer']", "[data-testid='videoComponent']",
                        ".PlayableMedia-player", "[aria-label*='video']", "[data-testid='card.layoutLarge.media']",
                        ".twitter-video", "[src*='.mp4']", "[src*='.m3u8']"
                    ]
                    
                    has_video = False
                    for selector in video_indicators:
                        try:
                            elements = tweet_element.find_elements(By.CSS_SELECTOR, selector)
                            if elements:
                                for elem in elements:
                                    if any([
                                        elem.tag_name == 'video',
                                        'video' in (elem.get_attribute('data-testid') or ''),
                                        'video' in (elem.get_attribute('aria-label') or ''),
                                        'mp4' in (elem.get_attribute('src') or ''),
                                        'm3u8' in (elem.get_attribute('src') or ''),
                                        'PlayableMedia' in (elem.get_attribute('class') or '')
                                    ]):
                                        has_video = True
                                        break
                        except:
                            continue
                        
                        if has_video:
                            break
                    
                    # Only add tweets with videos
                    if has_video and tweet_url:
                        tweet_id = tweet_url.split("/status/")[-1] if "/status/" in tweet_url else f"unknown_{i}"
                        
                        video_entry = {
                            "search_term": search_query,
                            "tweet_url": tweet_url,
                            "tweet_id": tweet_id,
                            "tweet_text": tweet_text[:200],
                            "username": username,
                            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ"),
                            "discovered_at": time.strftime("%Y-%m-%dT%H:%M:%SZ"),
                            "search_index": 0,
                            "video_index": len(discovered_videos),
                            "source": "browser_automation",
                            "requires_ytdlp": True,
                            "video_page_url": f"https://x.com/i/status/{tweet_id}"
                        }
                        discovered_videos.append(video_entry)
                        print(f"✅ Found video tweet #{len(discovered_videos)}: {tweet_text[:50]}...")
                        
                        if len(discovered_videos) >= max_results:
                            break
                
                except Exception as e:
                    print(f"⚠️ Error processing tweet {i}: {e}")
                    continue
            
            print(f"✅ Session service found {len(discovered_videos)} video tweets")
            return discovered_videos
            
        except Exception as e:
            print(f"❌ Search failed: {e}")
            return []
    
    async def startup(self):
        """Initialize session on service startup - NO DATABASE CALLS"""
        print("🚀 Starting Twitter Session Service...")
        
        with self._lock:
            if self.authenticate():
                self.startup_complete = True
                print("✅ Twitter Session Service ready")
                return True
                    
        print("❌ Twitter Session Service failed to start")
        return False
    
    def cleanup(self):
        """Clean up browser session"""
        if self.driver:
            try:
                self.driver.quit()
                print("✅ Browser session cleaned up")
            except:
                pass
        self.authenticated = False
        self.startup_complete = False

# Global session instance
twitter_session = TwitterSessionManager()

@asynccontextmanager
async def lifespan(app: FastAPI):
    # Startup - NO DATABASE OPERATIONS
    await twitter_session.startup()
    yield
    # Shutdown
    twitter_session.cleanup()

# FastAPI app - ISOLATED
app = FastAPI(
    title="Twitter Session Service", 
    version="1.0.0",
    lifespan=lifespan
)

class VideoSearchRequest(BaseModel):
    search_query: str
    max_results: int = 3

@app.get("/health")
async def health_check():
    """Health check endpoint - NO DATABASE CALLS"""
    if twitter_session.startup_complete and twitter_session.authenticated:
        return {"status": "healthy", "authenticated": True, "session_timeout": twitter_session.session_timeout}
    else:
        return JSONResponse(
            status_code=503,
            content={"status": "unhealthy", "authenticated": False}
        )

@app.post("/search")
async def search_videos(request: VideoSearchRequest):
    """Search for videos - PURE FUNCTION, NO DATABASE WRITES"""
    try:
        videos = twitter_session.search_videos(request.search_query, request.max_results)
        return {"status": "success", "videos": videos, "count": len(videos)}
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))

@app.post("/authenticate")
async def force_authenticate():
    """Force re-authentication - NO DATABASE CALLS"""
    try:
        success = twitter_session.authenticate(force_reauth=True)
        return {"status": "success" if success else "failed", "authenticated": success}
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))

if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=8888)