"""COMPLETELY ISOLATED Twitter Session Service - ZERO found_footy imports"""
import time
import threading
import os
import sys
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

# ❌ ABSOLUTELY NO found_footy imports - they're causing the MongoDB connections
# from found_footy.* (NOTHING!)

class TwitterSessionManager:
    """COMPLETELY ISOLATED Twitter session"""
    
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
        
        print("🔧 TwitterSessionManager initialized (COMPLETELY ISOLATED)")
    
    def _setup_browser(self):
        """Setup persistent browser"""
        try:
            chrome_options = Options()
            chrome_options.add_argument("--headless")
            chrome_options.add_argument("--no-sandbox")
            chrome_options.add_argument("--disable-dev-shm-usage")
            chrome_options.add_argument("--disable-gpu")
            chrome_options.add_argument("--window-size=1920,1080") 
            chrome_options.add_argument("--disable-web-security")
            chrome_options.add_argument("--user-agent=Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36")
            chrome_options.add_argument("--user-data-dir=/tmp/chrome_session/profile")
            
            service = Service("/usr/bin/chromedriver")
            self.driver = webdriver.Chrome(service=service, options=chrome_options)
            
            print("✅ Browser session created")
            return True
            
        except Exception as e:
            print(f"❌ Browser setup failed: {e}")
            return False
    
    def authenticate(self, force_reauth=False):
        """Authenticate with Twitter"""
        if self.authenticated and not force_reauth and self._session_valid():
            print("✅ Already authenticated")
            return True
        
        if not self.driver:
            if not self._setup_browser():
                return False
        
        try:
            print("🔐 Authenticating with Twitter...")
            
            # Check if already logged in
            self.driver.get("https://twitter.com/home")
            time.sleep(3)
            
            current_url = self.driver.current_url
            if "home" in current_url and "login" not in current_url:
                print("✅ Already authenticated via session")
                self.authenticated = True
                self.last_activity = time.time()
                return True
            
            # Perform login
            return self._perform_login()
            
        except Exception as e:
            print(f"❌ Authentication failed: {e}")
            self.authenticated = False
            return False
    
    def _perform_login(self):
        """Login process"""
        try:
            twitter_username = os.getenv('TWITTER_USERNAME', 'REDACTED_USERNAME')
            twitter_password = os.getenv('TWITTER_PASSWORD', 'REDACTED_PASSWORD')
            twitter_email = os.getenv('TWITTER_EMAIL', 'REDACTED_EMAIL')
            
            self.driver.get("https://twitter.com/login")
            
            # Username
            username_input = WebDriverWait(self.driver, 10).until(
                EC.presence_of_element_located((By.NAME, "text"))
            )
            username_input.clear()
            username_input.send_keys(twitter_username)
            
            next_button = self.driver.find_element(By.XPATH, "//span[text()='Next']/..")
            next_button.click()
            time.sleep(3)
            
            # Handle email verification
            try:
                email_input = self.driver.find_element(By.NAME, "text")
                email_input.clear()
                email_input.send_keys(twitter_email)
                next_button = self.driver.find_element(By.XPATH, "//span[text()='Next']/..")
                next_button.click()
                time.sleep(3)
            except NoSuchElementException:
                print("   ✅ No email verification needed")
            
            # Password
            password_input = WebDriverWait(self.driver, 10).until(
                EC.presence_of_element_located((By.NAME, "password"))
            )
            password_input.clear()
            password_input.send_keys(twitter_password)
            
            login_button = self.driver.find_element(By.XPATH, "//span[text()='Log in']/..")
            login_button.click()
            time.sleep(5)
            
            # Verify success
            current_url = self.driver.current_url
            if "home" in current_url:
                print("✅ Login successful!")
                self.authenticated = True
                self.last_activity = time.time()
                return True
            else:
                print("❌ Login failed")
                return False
                
        except Exception as e:
            print(f"❌ Login process failed: {e}")
            return False
    
    def _session_valid(self):
        """Check session validity"""
        if not self.driver or not self.authenticated:
            return False
        
        if time.time() - self.last_activity > self.session_timeout:
            return False
        
        try:
            self.driver.current_url
            return True
        except WebDriverException:
            self.authenticated = False
            return False
    
    def search_videos(self, search_query, max_results=3):
        """Search for videos"""
        if not self.authenticate():
            return []
        
        try:
            from urllib.parse import quote
            
            video_search_query = f"{search_query} filter:videos"
            search_url = f"https://twitter.com/search?q={quote(video_search_query)}&src=typed_query&f=live"
            
            self.driver.get(search_url)
            time.sleep(5)
            self.last_activity = time.time()
            
            tweet_elements = self.driver.find_elements(By.CSS_SELECTOR, "article[data-testid='tweet']")
            discovered_videos = []
            
            for i, tweet_element in enumerate(tweet_elements[:20]):
                try:
                    # Extract text
                    tweet_text = "Text not found"
                    try:
                        text_element = tweet_element.find_element(By.CSS_SELECTOR, "[data-testid='tweetText']")
                        if text_element:
                            tweet_text = text_element.text.strip()
                    except:
                        pass
                    
                    # Extract URL
                    tweet_url = None
                    try:
                        status_links = tweet_element.find_elements(By.CSS_SELECTOR, "a[href*='/status/']")
                        for link in status_links:
                            link_href = link.get_attribute("href")
                            if "/status/" in link_href:
                                tweet_url = link_href
                                break
                    except:
                        pass
                    
                    # Check for video
                    has_video = False
                    video_selectors = ["video", "[data-testid='videoPlayer']", "[data-testid='videoComponent']"]
                    for selector in video_selectors:
                        try:
                            if tweet_element.find_elements(By.CSS_SELECTOR, selector):
                                has_video = True
                                break
                        except:
                            continue
                    
                    if has_video and tweet_url:
                        tweet_id = tweet_url.split("/status/")[-1] if "/status/" in tweet_url else f"unknown_{i}"
                        
                        video_entry = {
                            "search_term": search_query,
                            "tweet_url": tweet_url,
                            "tweet_id": tweet_id,
                            "tweet_text": tweet_text[:200],
                            "username": "Unknown",
                            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ"),
                            "discovered_at": time.strftime("%Y-%m-%dT%H:%M:%SZ"),
                            "search_index": 0,
                            "video_index": len(discovered_videos),
                            "source": "browser_automation",
                            "requires_ytdlp": True,
                            "video_page_url": f"https://x.com/i/status/{tweet_id}"
                        }
                        discovered_videos.append(video_entry)
                        
                        if len(discovered_videos) >= max_results:
                            break
                
                except Exception:
                    continue
            
            print(f"✅ Found {len(discovered_videos)} video tweets")
            return discovered_videos
            
        except Exception as e:
            print(f"❌ Search failed: {e}")
            return []
    
    async def startup(self):
        """FAST startup - no external dependencies"""
        print("🚀 Starting Twitter Session Service...")
        
        with self._lock:
            if self.authenticate():
                self.startup_complete = True
                print("✅ Twitter Session Service ready")
                return True
                    
        print("❌ Twitter Session Service failed to start")
        return False
    
    def cleanup(self):
        """Cleanup"""
        if self.driver:
            try:
                self.driver.quit()
            except:
                pass
        self.authenticated = False
        self.startup_complete = False

# Create instance
twitter_session = TwitterSessionManager()

@asynccontextmanager
async def lifespan(app: FastAPI):
    # FAST startup
    await twitter_session.startup()
    yield
    # Cleanup
    twitter_session.cleanup()

# FastAPI app
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
    """Health check"""
    if twitter_session.startup_complete and twitter_session.authenticated:
        return {"status": "healthy", "authenticated": True}
    else:
        return JSONResponse(
            status_code=503,
            content={"status": "unhealthy", "authenticated": False}
        )

@app.post("/search")
async def search_videos(request: VideoSearchRequest):
    """Search videos"""
    try:
        videos = twitter_session.search_videos(request.search_query, request.max_results)
        return {"status": "success", "videos": videos, "count": len(videos)}
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))

@app.post("/authenticate")
async def force_authenticate():
    """Force re-auth"""
    try:
        success = twitter_session.authenticate(force_reauth=True)
        return {"status": "success" if success else "failed", "authenticated": success}
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))

if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=8888)