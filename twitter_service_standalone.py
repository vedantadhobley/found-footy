"""STANDALONE Twitter Session Service - Cookie-Based Auth

This is a completely isolated microservice with ZERO package dependencies.
Runs independently, only needs Chrome/Selenium.
"""
import time
import threading
import os
import pickle
from fastapi import FastAPI, HTTPException
from fastapi.responses import JSONResponse, HTMLResponse
from pydantic import BaseModel
from selenium import webdriver
from selenium.webdriver.chrome.options import Options
from selenium.webdriver.chrome.service import Service
from selenium.webdriver.common.by import By
from selenium.webdriver.support.ui import WebDriverWait
from selenium.webdriver.support import expected_conditions as EC
from selenium.common.exceptions import WebDriverException, NoSuchElementException
from contextlib import asynccontextmanager

# ❌ ABSOLUTELY NO found_footy imports - this is an isolated microservice!

class TwitterSessionManager:
    """STANDALONE Twitter session - ZERO package dependencies"""
    
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
        self.cookies_file = os.getenv('TWITTER_COOKIES_FILE', '/data/twitter_cookies.pkl')
        self._initialized = True
        
        print("🔧 TwitterSessionManager initialized (STANDALONE - Cookie-based auth)")
    
    def _setup_browser(self, headless=True):
        """Setup persistent browser"""
        try:
            chrome_options = Options()
            if headless:
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
            
            print(f"✅ Browser session created ({'headless' if headless else 'interactive'})")
            return True
            
        except Exception as e:
            print(f"❌ Browser setup failed: {e}")
            return False
    
    def _load_cookies(self):
        """Load cookies from file if available"""
        if not os.path.exists(self.cookies_file):
            print(f"⚠️ No cookies file at {self.cookies_file}")
            return False
        
        try:
            with open(self.cookies_file, 'rb') as f:
                cookies = pickle.load(f)
            
            # Navigate to x.com first (cookies need matching domain)
            self.driver.get("https://x.com")
            time.sleep(2)
            
            # Add cookies
            for cookie in cookies:
                try:
                    self.driver.add_cookie(cookie)
                except Exception:
                    pass  # Some cookies might be invalid
            
            print(f"✅ Loaded {len(cookies)} cookies")
            return True
        except Exception as e:
            print(f"❌ Failed to load cookies: {e}")
            return False
    
    def _save_cookies(self):
        """Save current cookies to file"""
        try:
            cookies = self.driver.get_cookies()
            
            # Ensure directory exists
            os.makedirs(os.path.dirname(self.cookies_file), exist_ok=True)
            
            with open(self.cookies_file, 'wb') as f:
                pickle.dump(cookies, f)
            
            print(f"✅ Saved {len(cookies)} cookies to {self.cookies_file}")
            return True
        except Exception as e:
            print(f"❌ Failed to save cookies: {e}")
            return False
    
    def authenticate(self, force_reauth=False, interactive=True):
        """Authenticate with Twitter using cookies or interactive login
        
        Args:
            force_reauth: Force re-authentication even if already authenticated
            interactive: If True, open browser for manual login when cookies fail
        """
        if self.authenticated and not force_reauth and self._session_valid():
            print("✅ Already authenticated")
            return True
        
        # Close existing browser if force reauth
        if force_reauth and self.driver:
            try:
                self.driver.quit()
            except:
                pass
            self.driver = None
        
        if not self.driver:
            if not self._setup_browser(headless=not interactive):
                return False
        
        try:
            print("🔐 Authenticating with Twitter...")
            
            # Try to load cookies first
            if not force_reauth and self._load_cookies():
                # Verify cookies work - use x.com since that's where cookies are for
                self.driver.get("https://x.com/home")
                time.sleep(5)  # Give it more time to load
                
                current_url = self.driver.current_url
                print(f"🔍 After loading cookies, URL is: {current_url}")
                
                if "home" in current_url and "login" not in current_url and "flow" not in current_url:
                    print("✅ Authenticated via cookies!")
                    self.authenticated = True
                    self.last_activity = time.time()
                    return True
                else:
                    print(f"⚠️ Cookies invalid, need fresh login (redirected to: {current_url})")
            
            # No cookies or cookies failed - need manual login
            print("❌ No valid cookies found")
            print()
            print("📋 To login manually:")
            print("   docker compose exec twitter-session python twitter_manual_login.py")
            print()
            print("   This will open a browser where you can login,")
            print("   then save cookies for future use.")
            return False
            
        except Exception as e:
            print(f"❌ Authentication failed: {e}")
            import traceback
            traceback.print_exc()
            self.authenticated = False
            return False
    
    def _interactive_login(self):
        """Open browser and wait for user to manually login
        
        This method opens the Twitter login page and waits for the user
        to complete authentication. Once the user is on the home page,
        it saves cookies and returns.
        """
        try:
            print("🌐 Navigating to Twitter login page...")
            self.driver.get("https://twitter.com/login")
            
            print("⏳ Waiting for you to login manually...")
            print("   -> Complete login in the browser window")
            print("   -> This script will continue once you reach the home page")
            
            # Wait for user to complete login (check every 5 seconds)
            max_wait = 300  # 5 minutes
            start_time = time.time()
            
            while time.time() - start_time < max_wait:
                try:
                    current_url = self.driver.current_url
                    
                    # Check if we're on home page (successful login)
                    if "home" in current_url:
                        print("✅ Login detected! Saving cookies...")
                        
                        # Save cookies for next time
                        if self._save_cookies():
                            print("✅ Cookies saved successfully")
                        
                        self.authenticated = True
                        self.last_activity = time.time()
                        return True
                    
                    time.sleep(5)
                    
                except Exception as e:
                    print(f"⚠️ Error checking login status: {e}")
                    time.sleep(5)
            
            print("❌ Login timeout - took too long")
            return False
            
        except Exception as e:
            print(f"❌ Interactive login failed: {e}")
            import traceback
            traceback.print_exc()
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
                            if link_href and "/status/" in link_href:
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
        """FAST startup - authenticates with saved cookies"""
        print("🚀 Starting STANDALONE Twitter Session Service...")
        
        with self._lock:
            # Try to authenticate with saved cookies
            if self.authenticate(interactive=False):
                self.startup_complete = True
                print("✅ STANDALONE Twitter Session Service ready")
                return True
                    
        print("⚠️  Twitter service running but NOT authenticated")
        print("   (Service will work once you login manually)")
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
    title="Standalone Twitter Session Service", 
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

class AuthRequest(BaseModel):
    force_reauth: bool = True
    interactive: bool = False

@app.post("/authenticate")
async def force_authenticate(request: AuthRequest = AuthRequest()):
    """Force re-authentication with optional interactive login
    
    Args:
        force_reauth: Force new authentication even if already authenticated
        interactive: Open browser for manual login (requires DISPLAY access)
    """
    try:
        if request is None:
            request = AuthRequest()
        
        success = twitter_session.authenticate(
            force_reauth=request.force_reauth,
            interactive=request.interactive
        )
        return {"status": "success" if success else "failed", "authenticated": success}
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))

@app.get("/login", response_class=HTMLResponse)
async def login_page():
    """Interactive login page - opens popup that triggers Selenium to capture cookies"""
    
    html_content = """
    <!DOCTYPE html>
    <html>
    <head>
        <title>Twitter Login - Found Footy</title>
        <style>
            body {
                margin: 0;
                padding: 20px;
                font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
                background: #15202b;
                color: white;
            }
            .container {
                max-width: 800px;
                margin: 0 auto;
            }
            .header {
                background: linear-gradient(135deg, #1da1f2, #0084b4);
                padding: 30px;
                border-radius: 15px;
                margin-bottom: 30px;
                text-align: center;
            }
            .header h1 {
                margin: 0 0 10px 0;
            }
            .status {
                background: #192734;
                padding: 20px;
                border-radius: 10px;
                margin-bottom: 30px;
                border-left: 4px solid #657786;
            }
            .status.authenticated {
                border-left-color: #00ba7c;
                background: #0d3625;
            }
            .status.checking {
                border-left-color: #ffad1f;
            }
            button {
                background: #1da1f2;
                color: white;
                border: none;
                padding: 15px 30px;
                border-radius: 25px;
                cursor: pointer;
                font-size: 16px;
                font-weight: 600;
                margin: 10px 10px 10px 0;
                transition: all 0.2s;
            }
            button:hover {
                background: #1a8cd8;
                transform: translateY(-2px);
            }
            button:active {
                transform: translateY(0);
            }
            .instructions {
                background: #192734;
                padding: 25px;
                border-radius: 10px;
                margin: 20px 0;
            }
            .instructions ol {
                margin: 10px 0;
                padding-left: 25px;
            }
            .instructions li {
                margin: 10px 0;
                line-height: 1.6;
            }
            .spinner {
                display: inline-block;
                width: 20px;
                height: 20px;
                border: 3px solid rgba(255,255,255,0.3);
                border-top-color: white;
                border-radius: 50%;
                animation: spin 1s linear infinite;
            }
            @keyframes spin {
                to { transform: rotate(360deg); }
            }
        </style>
    </head>
    <body>
        <div class="container">
            <div class="header">
                <h1>🐦 Twitter Authentication</h1>
                <p>One-time setup to enable video search</p>
            </div>
            
            <div class="status" id="status">
                <h3><span id="auth-status">Checking...</span></h3>
                <p id="auth-message"></p>
            </div>
            
            <div class="instructions">
                <h3>📋 Export Cookies (Gets ALL cookies including HttpOnly):</h3>
                <ol>
                    <li>Go to <a href="https://twitter.com" target="_blank" style="color: #1da1f2;">twitter.com</a> and make sure you're logged in</li>
                    <li>Open DevTools: Press <strong>F12</strong></li>
                    <li>Go to <strong>Application</strong> tab (or <strong>Storage</strong> in Firefox)</li>
                    <li>In the left sidebar, expand <strong>Cookies</strong> → Click <strong>https://twitter.com</strong></li>
                    <li>You'll see a table of all cookies. Right-click anywhere in the cookie list</li>
                    <li><strong>Chrome:</strong> Select all cookies (Ctrl+A), right-click, choose "Copy" or "Copy as JSON"</li>
                    <li><strong>Firefox:</strong> Right-click → "Select All" → "Copy"</li>
                </ol>
                <p style="background: #192734; padding: 15px; border-radius: 5px; margin: 15px 0;">
                    <strong>⚠️ Important:</strong> Make sure you see a cookie named <code style="background: #0d1117; padding: 3px 8px; border-radius: 3px;">auth_token</code> in the list!<br>
                    Without it, authentication won't work.
                </p>
                <p style="font-size: 14px; color: #8899a6; margin: 15px 0;">
                    <strong>Alternative (if copy doesn't work):</strong> Install the <a href="https://chrome.google.com/webstore/detail/cookie-editor/" target="_blank" style="color: #1da1f2;">Cookie-Editor extension</a>,
                    click it, then click "Export" → "JSON" to get all cookies.
                </p>
                <ol start="8">
                    <li>Paste the cookies in the box below</li>
                    <li>Click <strong>"Save Cookies"</strong></li>
                </ol>
            </div>
            
            <div style="background: #00ba7c; padding: 20px; border-radius: 10px; margin: 20px 0;">
                <h3 style="margin-top: 0;">⚡ QUICK METHOD - Paste Three Cookies:</h3>
                <p>In DevTools → Application → Cookies → twitter.com, find these THREE cookies and paste their values:</p>
                <div style="margin: 15px 0;">
                    <label style="display: block; margin: 10px 0; font-weight: bold;">1. auth_token (HttpOnly - 40 chars):</label>
                    <input type="text" id="authTokenInput" placeholder="e.g. 71a68c2ef4db94fbd251d38da9be7d7d8ac257f4" 
                        style="width: 100%; padding: 12px; border-radius: 5px; background: #192734; color: white; border: 2px solid white; font-family: monospace; font-size: 14px;">
                </div>
                <div style="margin: 15px 0;">
                    <label style="display: block; margin: 10px 0; font-weight: bold;">2. ct0 (CSRF token - 32 chars):</label>
                    <input type="text" id="ct0Input" placeholder="e.g. ae7025b6855aee0adb93f627b0c8e45f" 
                        style="width: 100%; padding: 12px; border-radius: 5px; background: #192734; color: white; border: 2px solid white; font-family: monospace; font-size: 14px;">
                </div>
                <div style="margin: 15px 0;">
                    <label style="display: block; margin: 10px 0; font-weight: bold;">3. twid (Twitter ID - starts with "u="):</label>
                    <input type="text" id="twidInput" placeholder="e.g. u=1234567890" 
                        style="width: 100%; padding: 12px; border-radius: 5px; background: #192734; color: white; border: 2px solid white; font-family: monospace; font-size: 14px;">
                </div>
                <button onclick="saveAuthToken()" style="background: white; color: #00ba7c; margin-top: 10px;">🚀 Save All Cookies</button>
            </div>
            
            <div style="margin: 30px 0;">
                <textarea id="cookieInput" placeholder="OR paste full cookie JSON here (from Cookie-Editor extension)" 
                    style="width: 100%; height: 150px; padding: 15px; border-radius: 10px; background: #192734; color: white; border: 2px solid #1da1f2; font-family: monospace; font-size: 14px;"></textarea>
            </div>
            
            <div style="text-align: center; margin: 30px 0;">
                <button onclick="saveCookies()" id="saveBtn">💾 Save Full Cookies</button>
                <button onclick="checkStatus()">🔄 Check Status</button>
            </div>
        </div>
        
        <script>
            let statusInterval = null;
            
            async function checkStatus() {
                try {
                    const response = await fetch('/health');
                    const data = await response.json();
                    const statusDiv = document.getElementById('status');
                    const statusSpan = document.getElementById('auth-status');
                    const messageP = document.getElementById('auth-message');
                    
                    if (data.authenticated) {
                        statusDiv.className = 'status authenticated';
                        statusSpan.textContent = '✅ Authenticated';
                        messageP.textContent = 'Twitter service is ready! Cookies are saved. You can close this page.';
                        clearInterval(statusInterval);
                    } else {
                        statusDiv.className = 'status';
                        statusSpan.textContent = '❌ Not Authenticated';
                        messageP.textContent = 'Please paste your Twitter cookies below to authenticate.';
                    }
                } catch (error) {
                    console.error('Error checking status:', error);
                }
            }
            
            async function saveAuthToken() {
                const statusDiv = document.getElementById('status');
                const statusSpan = document.getElementById('auth-status');
                const messageP = document.getElementById('auth-message');
                const authTokenInput = document.getElementById('authTokenInput');
                const ct0Input = document.getElementById('ct0Input');
                const twidInput = document.getElementById('twidInput');
                
                const authToken = authTokenInput.value.trim();
                const ct0 = ct0Input.value.trim();
                const twid = twidInput.value.trim();
                
                if (!authToken || !ct0 || !twid) {
                    alert('Please paste ALL THREE cookie values (auth_token, ct0, and twid)!');
                    return;
                }
                
                statusDiv.className = 'status checking';
                statusSpan.innerHTML = '<span class="spinner"></span> Saving cookies...';
                messageP.textContent = 'Please wait...';
                
                try {
                    const response = await fetch('/save-auth-token', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ auth_token: authToken, ct0: ct0, twid: twid })
                    });
                    
                    const data = await response.json();
                    
                    if (data.success) {
                        statusDiv.className = 'status authenticated';
                        statusSpan.textContent = '✅ Success!';
                        messageP.textContent = 'Cookies saved! Service is now authenticated.';
                        authTokenInput.value = '';
                        ct0Input.value = '';
                        twidInput.value = '';
                        setTimeout(checkStatus, 2000);
                    } else {
                        statusDiv.className = 'status';
                        statusSpan.textContent = '❌ Failed';
                        messageP.textContent = data.error || 'Could not save auth token.';
                    }
                } catch (error) {
                    statusDiv.className = 'status';
                    statusSpan.textContent = '❌ Error';
                    messageP.textContent = 'Error: ' + error.message;
                }
            }
            
            async function saveCookies() {
                const statusDiv = document.getElementById('status');
                const statusSpan = document.getElementById('auth-status');
                const messageP = document.getElementById('auth-message');
                const cookieInput = document.getElementById('cookieInput');
                
                const cookieText = cookieInput.value.trim();
                
                if (!cookieText) {
                    alert('Please paste your cookies first!');
                    return;
                }
                
                statusDiv.className = 'status checking';
                statusSpan.innerHTML = '<span class="spinner"></span> Saving cookies...';
                messageP.textContent = 'Please wait while we save your login...';
                
                try {
                    let cookies;
                    try {
                        cookies = JSON.parse(cookieText);
                    } catch (e) {
                        alert('Invalid cookie format. Please use the copy() command from DevTools.');
                        statusDiv.className = 'status';
                        statusSpan.textContent = '❌ Not Authenticated';
                        messageP.textContent = 'Invalid cookie format.';
                        return;
                    }
                    
                    const response = await fetch('/save-cookies', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ cookies: cookies })
                    });
                    
                    const data = await response.json();
                    
                    if (data.success) {
                        statusDiv.className = 'status authenticated';
                        statusSpan.textContent = '✅ Success!';
                        messageP.textContent = 'Cookies saved! Service is now authenticated.';
                        cookieInput.value = '';
                        setTimeout(checkStatus, 2000);
                    } else {
                        statusDiv.className = 'status';
                        statusSpan.textContent = '❌ Failed';
                        messageP.textContent = data.error || 'Could not save cookies.';
                    }
                } catch (error) {
                    statusDiv.className = 'status';
                    statusSpan.textContent = '❌ Error';
                    messageP.textContent = 'Error: ' + error.message;
                }
            }
            
            // Check status on load
            checkStatus();
            statusInterval = setInterval(checkStatus, 10000);
        </script>
    </body>
    </html>
    """
    return HTMLResponse(content=html_content)

@app.post("/save-auth-token")
async def save_auth_token(request: dict):
    """Save the three critical Twitter cookies"""
    try:
        auth_token = request.get('auth_token', '').strip()
        ct0 = request.get('ct0', '').strip()
        twid = request.get('twid', '').strip()
        
        if not auth_token or not ct0 or not twid:
            return {"success": False, "error": "Missing required cookies (need auth_token, ct0, and twid)"}
        
        # Create minimal cookie set with the three critical cookies
        selenium_cookies = [
            {
                'name': 'auth_token',
                'value': auth_token,
                'domain': '.twitter.com',
                'path': '/',
                'secure': True,
                'httpOnly': True
            },
            {
                'name': 'ct0',
                'value': ct0,
                'domain': '.twitter.com',
                'path': '/',
                'secure': True,
                'httpOnly': False
            },
            {
                'name': 'twid',
                'value': twid,
                'domain': '.twitter.com',
                'path': '/',
                'secure': True,
                'httpOnly': False
            }
        ]
        
        # Save cookies
        cookies_file = os.getenv('TWITTER_COOKIES_FILE', '/data/twitter_cookies.pkl')
        os.makedirs(os.path.dirname(cookies_file), exist_ok=True)
        
        with open(cookies_file, 'wb') as f:
            pickle.dump(selenium_cookies, f)
        
        print(f"✅ Saved 3 cookies: auth_token={auth_token[:20]}..., ct0={ct0[:10]}..., twid={twid}")
        
        # Restart authentication
        twitter_session.authenticated = False
        if twitter_session.driver:
            try:
                twitter_session.driver.quit()
            except:
                pass
        twitter_session.driver = None
        success = await twitter_session.startup()
        
        return {"success": success, "authenticated": success}
        
    except Exception as e:
        import traceback
        traceback.print_exc()
        return {"success": False, "error": str(e)}

@app.post("/save-cookies")
async def save_cookies(request: dict):
    """Save cookies pasted from user's browser"""
    try:
        cookies = request.get('cookies', [])
        
        if not cookies:
            return {"success": False, "error": "No cookies provided"}
        
        # Convert to Selenium cookie format
        # Handle different cookie export formats
        selenium_cookies = []
        
        # Check if it's a simple string (tab-separated values from DevTools)
        if isinstance(cookies, str):
            # Parse tab-separated cookie data from Chrome DevTools copy
            lines = cookies.strip().split('\n')
            for line in lines:
                parts = line.split('\t')
                if len(parts) >= 2:
                    selenium_cookies.append({
                        'name': parts[0],
                        'value': parts[1],
                        'domain': '.twitter.com',
                        'path': '/',
                        'secure': True,
                        'httpOnly': False
                    })
        else:
            # Handle JSON array format
            for cookie in cookies:
                # Handle both cookieStore format and document.cookie format
                selenium_cookie = {
                    'name': cookie.get('name', ''),
                    'value': cookie.get('value', ''),
                    'domain': cookie.get('domain', '.twitter.com'),
                    'path': cookie.get('path', '/'),
                    'secure': cookie.get('secure', True),
                    'httpOnly': cookie.get('httpOnly', False)
                }
                
                # Add expiry if present
                if 'expires' in cookie and cookie['expires']:
                    # Convert from milliseconds timestamp if needed
                    expiry = cookie['expires']
                    if expiry > 10000000000:  # Likely in milliseconds
                        expiry = expiry / 1000
                    selenium_cookie['expiry'] = int(expiry)
                
                # Add sameSite if present
                if 'sameSite' in cookie:
                    selenium_cookie['sameSite'] = cookie['sameSite']
                
                selenium_cookies.append(selenium_cookie)
        
        if not selenium_cookies:
            return {"success": False, "error": "No valid cookies found"}
        
        # Save cookies
        cookies_file = os.getenv('TWITTER_COOKIES_FILE', '/data/twitter_cookies.pkl')
        os.makedirs(os.path.dirname(cookies_file), exist_ok=True)
        
        with open(cookies_file, 'wb') as f:
            pickle.dump(selenium_cookies, f)
        
        print(f"✅ Saved {len(selenium_cookies)} cookies from browser")
        print(f"   Cookie names: {[c['name'] for c in selenium_cookies[:5]]}...")
        
        # Check for critical auth_token cookie
        has_auth_token = any(c['name'] == 'auth_token' for c in selenium_cookies)
        print(f"   Has auth_token: {has_auth_token}")
        
        if not has_auth_token:
            return {"success": False, "error": "Missing 'auth_token' cookie! This is required for authentication. Make sure you're copying from the Application > Cookies tab in DevTools, not from Console. The auth_token is HttpOnly and can't be accessed via JavaScript."}
        
        # Restart authentication with new cookies
        twitter_session.authenticated = False
        if twitter_session.driver:
            try:
                twitter_session.driver.quit()
            except:
                pass
        twitter_session.driver = None
        success = await twitter_session.startup()
        
        if not success:
            return {"success": False, "error": "Cookies saved but authentication failed. The cookies may be expired. Try logging out and back in to Twitter, then export cookies again."}
        
        return {"success": True, "cookies_count": len(selenium_cookies), "authenticated": success}
        
    except Exception as e:
        import traceback
        traceback.print_exc()
        return {"success": False, "error": str(e)}

if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host="0.0.0.0", port=8888)