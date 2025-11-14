"""Twitter Session Service with Manual Login Support for Raspberry Pi"""
import os
import time
import pickle
from pathlib import Path
from typing import Optional, List, Dict
from datetime import datetime, timedelta
from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from selenium import webdriver
from selenium.webdriver.chrome.options import Options
from selenium.webdriver.chrome.service import Service
from selenium.webdriver.common.by import By
import logging

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

class TwitterSessionManual:
    """Twitter session with manual login and persistent cookies"""
    
    def __init__(self, headless: bool = False, cookie_dir: str = "/root/.twitter"):
        self.headless = headless
        self.cookie_dir = Path(cookie_dir)
        self.cookie_file = self.cookie_dir / "cookies.pkl"
        self.driver: Optional[webdriver.Chrome] = None
        self.authenticated = False
        self.last_activity = None
        self.session_start = None
        
        # Create cookie directory
        self.cookie_dir.mkdir(parents=True, exist_ok=True)
    
    def setup_browser(self):
        """Setup Chrome browser for manual login support"""
        chrome_options = Options()
        
        # 🎯 HEADLESS vs NON-HEADLESS based on config
        if self.headless:
            chrome_options.add_argument("--headless=new")
            logger.info("🔧 Starting in HEADLESS mode")
        else:
            logger.info("🔧 Starting in NON-HEADLESS mode (manual login enabled)")
        
        # Docker/Pi compatibility
        chrome_options.add_argument("--no-sandbox")
        chrome_options.add_argument("--disable-dev-shm-usage")
        chrome_options.add_argument("--disable-gpu")
        chrome_options.add_argument("--window-size=1920,1080")
        
        # Stealth settings
        chrome_options.add_argument("--disable-blink-features=AutomationControlled")
        chrome_options.add_experimental_option("excludeSwitches", ["enable-automation"])
        chrome_options.add_experimental_option('useAutomationExtension', False)
        
        # User agent
        chrome_options.add_argument(
            "--user-agent=Mozilla/5.0 (X11; Linux aarch64) AppleWebKit/537.36 "
            "(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
        )
        
        try:
            service = Service("/usr/bin/chromedriver")
            self.driver = webdriver.Chrome(service=service, options=chrome_options)
            
            # Execute stealth JavaScript
            stealth_js = """
                Object.defineProperty(navigator, 'webdriver', {get: () => undefined});
                window.chrome = { runtime: {} };
                Object.defineProperty(navigator, 'plugins', {get: () => [1, 2, 3, 4, 5]});
                Object.defineProperty(navigator, 'languages', {get: () => ['en-US', 'en']});
            """
            self.driver.execute_script(stealth_js)
            
            logger.info("✅ Browser session created")
            return True
            
        except Exception as e:
            logger.error(f"❌ Browser setup failed: {e}")
            return False
    
    def save_cookies(self):
        """Save cookies to persistent storage"""
        try:
            cookies = self.driver.get_cookies()
            with open(self.cookie_file, 'wb') as f:
                pickle.dump(cookies, f)
            logger.info(f"✅ Cookies saved to {self.cookie_file}")
            return True
        except Exception as e:
            logger.error(f"❌ Failed to save cookies: {e}")
            return False
    
    def load_cookies(self) -> bool:
        """Load cookies from persistent storage"""
        try:
            if not self.cookie_file.exists():
                logger.info("ℹ️  No saved cookies found")
                return False
            
            # Check cookie age
            cookie_age = datetime.now() - datetime.fromtimestamp(self.cookie_file.stat().st_mtime)
            if cookie_age > timedelta(days=30):
                logger.warning("⚠️  Cookies are older than 30 days - may be expired")
            
            with open(self.cookie_file, 'rb') as f:
                cookies = pickle.load(f)
            
            # Navigate to Twitter first
            self.driver.get("https://x.com")
            time.sleep(2)
            
            # Add cookies
            for cookie in cookies:
                try:
                    self.driver.add_cookie(cookie)
                except Exception as e:
                    logger.warning(f"Failed to add cookie: {e}")
            
            logger.info("✅ Cookies loaded from disk")
            
            # Verify cookies work
            self.driver.get("https://x.com/home")
            time.sleep(3)
            
            current_url = self.driver.current_url
            if "x.com/home" in current_url or "twitter.com/home" in current_url:
                if "login" not in current_url and "flow" not in current_url:
                    logger.info("✅ Authenticated via saved cookies!")
                    self.authenticated = True
                    self.last_activity = datetime.now()
                    self.session_start = datetime.now()
                    return True
            
            logger.warning("❌ Saved cookies didn't work - need fresh login")
            return False
            
        except Exception as e:
            logger.error(f"❌ Failed to load cookies: {e}")
            return False
    
    def manual_login_required(self) -> Dict:
        """Return instructions for manual login"""
        return {
            "status": "manual_login_required",
            "instructions": [
                "1. Browser window is open (or check VNC at port 5900)",
                "2. Navigate to https://x.com in the browser",
                "3. Login manually with your credentials",
                "4. Once logged in, call POST /api/confirm-login",
                "5. Cookies will be saved automatically for future use"
            ],
            "browser_running": self.driver is not None,
            "headless": self.headless,
            "vnc_access": "vnc://localhost:5900" if not self.headless else None
        }
    
    def confirm_manual_login(self) -> bool:
        """Verify user has logged in manually and save cookies"""
        try:
            if not self.driver:
                logger.error("No browser session active")
                return False
            
            # Check current URL
            current_url = self.driver.current_url
            logger.info(f"Current URL: {current_url}")
            
            # Navigate to home to verify
            self.driver.get("https://x.com/home")
            time.sleep(3)
            
            final_url = self.driver.current_url
            
            if ("x.com/home" in final_url or "twitter.com/home" in final_url):
                if "login" not in final_url and "flow" not in final_url:
                    logger.info("✅ Manual login verified!")
                    
                    # Save cookies for future use
                    self.save_cookies()
                    
                    self.authenticated = True
                    self.last_activity = datetime.now()
                    self.session_start = datetime.now()
                    return True
            
            logger.warning(f"❌ Not logged in yet. URL: {final_url}")
            return False
            
        except Exception as e:
            logger.error(f"❌ Login verification failed: {e}")
            return False
    
    def authenticate(self) -> bool:
        """Try cookies first, fallback to manual login"""
        if not self.driver:
            if not self.setup_browser():
                return False
        
        # Try loading saved cookies
        if self.load_cookies():
            return True
        
        # Cookies didn't work - need manual login
        logger.warning("🔐 Manual login required")
        return False
    
    def search_videos(self, search_query: str, max_results: int = 5) -> List[Dict]:
        """Search for videos using authenticated session"""
        if not self.authenticated:
            raise Exception("Not authenticated - manual login required")
        
        try:
            # Navigate to search
            search_url = f"https://x.com/search?q={search_query}&f=video"
            self.driver.get(search_url)
            time.sleep(5)
            
            # Your existing video extraction logic here
            videos = []
            
            # Example: Extract video elements
            video_elements = self.driver.find_elements(By.CSS_SELECTOR, "[data-testid='videoPlayer']")
            
            for i, element in enumerate(video_elements[:max_results]):
                try:
                    # Extract video URL, tweet URL, etc.
                    video_data = {
                        "index": i,
                        "query": search_query,
                        "found_at": datetime.now().isoformat()
                    }
                    videos.append(video_data)
                except Exception as e:
                    logger.warning(f"Failed to extract video {i}: {e}")
            
            self.last_activity = datetime.now()
            logger.info(f"✅ Found {len(videos)} videos for query: {search_query}")
            return videos
            
        except Exception as e:
            logger.error(f"❌ Video search failed: {e}")
            raise
    
    def get_status(self) -> Dict:
        """Get current session status"""
        return {
            "authenticated": self.authenticated,
            "browser_active": self.driver is not None,
            "headless": self.headless,
            "last_activity": self.last_activity.isoformat() if self.last_activity else None,
            "session_start": self.session_start.isoformat() if self.session_start else None,
            "cookies_exist": self.cookie_file.exists(),
            "cookie_age_days": (
                (datetime.now() - datetime.fromtimestamp(self.cookie_file.stat().st_mtime)).days
                if self.cookie_file.exists() else None
            )
        }
    
    def cleanup(self):
        """Cleanup browser session"""
        if self.driver:
            try:
                self.driver.quit()
                logger.info("✅ Browser session cleaned up")
            except Exception as e:
                logger.warning(f"Error during cleanup: {e}")
        
        self.authenticated = False
        self.driver = None


# FastAPI service for managing Twitter session
app = FastAPI(title="Twitter Session Manager", version="2.0.0")

# Global session instance
twitter_session = None

class VideoSearchRequest(BaseModel):
    search_query: str
    max_results: int = 5

class ManualLoginResponse(BaseModel):
    status: str
    instructions: List[str]
    browser_running: bool
    headless: bool
    vnc_access: Optional[str]

@app.on_event("startup")
async def startup():
    """Initialize Twitter session on startup"""
    global twitter_session
    
    headless = os.getenv("TWITTER_HEADLESS", "false").lower() == "true"
    
    logger.info(f"🚀 Starting Twitter Session Service (headless={headless})")
    
    twitter_session = TwitterSessionManual(headless=headless)
    
    # Try to authenticate with saved cookies
    if not twitter_session.authenticate():
        logger.warning("⚠️  Manual login required - cookies not found or expired")
        logger.info("📋 Call POST /api/start-manual-login to begin")

@app.get("/api/status")
async def get_status():
    """Get current session status"""
    if not twitter_session:
        raise HTTPException(status_code=503, detail="Session not initialized")
    
    return twitter_session.get_status()

@app.post("/api/start-manual-login")
async def start_manual_login():
    """Start browser for manual login"""
    if not twitter_session:
        raise HTTPException(status_code=503, detail="Session not initialized")
    
    if not twitter_session.driver:
        if not twitter_session.setup_browser():
            raise HTTPException(status_code=500, detail="Failed to start browser")
    
    # Navigate to Twitter
    twitter_session.driver.get("https://x.com")
    
    return twitter_session.manual_login_required()

@app.post("/api/confirm-login")
async def confirm_login():
    """Confirm manual login and save cookies"""
    if not twitter_session:
        raise HTTPException(status_code=503, detail="Session not initialized")
    
    if twitter_session.confirm_manual_login():
        return {
            "status": "success",
            "message": "Login confirmed and cookies saved!",
            "session_info": twitter_session.get_status()
        }
    else:
        return {
            "status": "failed",
            "message": "Login verification failed - please ensure you're logged in to Twitter"
        }

@app.post("/api/search")
async def search_videos(request: VideoSearchRequest):
    """Search for videos"""
    if not twitter_session:
        raise HTTPException(status_code=503, detail="Session not initialized")
    
    if not twitter_session.authenticated:
        raise HTTPException(
            status_code=401,
            detail="Not authenticated - call POST /api/start-manual-login"
        )
    
    try:
        videos = twitter_session.search_videos(
            request.search_query,
            request.max_results
        )
        return {
            "status": "success",
            "query": request.search_query,
            "videos_found": len(videos),
            "videos": videos
        }
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))

@app.get("/health")
async def health():
    """Health check"""
    return {
        "status": "healthy",
        "service": "twitter-session",
        "authenticated": twitter_session.authenticated if twitter_session else False
    }

if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=8888)
