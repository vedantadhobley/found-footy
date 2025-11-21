"""FastAPI application for Twitter scraping service

Provides REST API endpoints for:
- Health checking
- Video search
- Authentication management
- Manual login UI
"""
import os
import pickle
from contextlib import asynccontextmanager
from fastapi import FastAPI, HTTPException
from fastapi.responses import JSONResponse, HTMLResponse
from pydantic import BaseModel
from typing import Optional

from .config import TwitterConfig
from .session import TwitterSessionManager


# Initialize configuration and session manager
config = TwitterConfig()
twitter_session = TwitterSessionManager(config)


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Application lifespan manager - startup and shutdown"""
    # Startup
    twitter_session.startup()  # Not async anymore
    yield
    # Shutdown
    twitter_session.cleanup()


# Create FastAPI app
app = FastAPI(
    title="Twitter Scraper Service",
    description="Independent microservice for scraping Twitter videos using Selenium",
    version="1.0.0",
    lifespan=lifespan
)


# === Request/Response Models ===

class VideoSearchRequest(BaseModel):
    """Request model for video search"""
    search_query: str
    max_results: int = 3


class AuthRequest(BaseModel):
    """Request model for authentication"""
    force_reauth: bool = False
    interactive: bool = False


# === API Endpoints ===

@app.get("/")
async def root():
    """Root endpoint with service info"""
    return {
        "service": "Twitter Scraper",
        "version": "1.0.0",
        "status": "running",
        "authenticated": twitter_session.authenticated,
        "vnc_url": "http://localhost:6080/vnc.html",
        "login_instructions": "Run: docker compose exec twitter python -m twitter.manual_login",
        "endpoints": {
            "health": "/health",
            "search": "/search",
            "authenticate": "/authenticate"
        }
    }


@app.get("/health")
async def health_check():
    """Health check endpoint"""
    if twitter_session.startup_complete and twitter_session.authenticated:
        return {
            "status": "healthy",
            "authenticated": True,
            "session_timeout": twitter_session.config.session_timeout
        }
    else:
        return JSONResponse(
            status_code=503,
            content={
                "status": "unhealthy",
                "authenticated": False,
                "message": "Not authenticated. Run: docker compose exec twitter python -m twitter.manual_login",
                "vnc_url": "http://localhost:6080/vnc.html"
            }
        )


@app.post("/search")
async def search_videos(request: VideoSearchRequest):
    """Search Twitter for videos
    
    Args:
        search_query: Search terms (e.g., "Messi Barcelona goal")
        max_results: Maximum number of videos to return (default: 3)
        
    Returns:
        JSON with discovered videos
    """
    try:
        videos = twitter_session.search_videos(
            request.search_query,
            request.max_results
        )
        return {
            "status": "success",
            "videos": videos,
            "count": len(videos)
        }
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))


@app.post("/authenticate")
async def force_authenticate(request: AuthRequest = AuthRequest()):
    """Force re-authentication
    
    Args:
        force_reauth: Force new authentication even if already authenticated
        interactive: Allow interactive manual login (requires GUI access)
        
    Returns:
        Authentication status
    """
    try:
        if request is None:
            request = AuthRequest()
        
        success = twitter_session.authenticate(
            force_reauth=request.force_reauth,
            allow_interactive=request.interactive
        )
        
        return {
            "status": "success" if success else "failed",
            "authenticated": success
        }
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))


@app.post("/save-auth-token")
async def save_auth_token(request: dict):
    """Save the three critical Twitter cookies manually
    
    This is a simplified cookie import method that requires only 3 cookies:
    - auth_token (HttpOnly cookie)
    - ct0 (CSRF token)
    - twid (Twitter ID)
    """
    try:
        auth_token = request.get('auth_token', '').strip()
        ct0 = request.get('ct0', '').strip()
        twid = request.get('twid', '').strip()
        
        if not auth_token or not ct0 or not twid:
            return {
                "success": False,
                "error": "Missing required cookies (need auth_token, ct0, and twid)"
            }
        
        # Create minimal cookie set
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
        os.makedirs(os.path.dirname(config.cookies_file), exist_ok=True)
        with open(config.cookies_file, 'wb') as f:
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
        success = twitter_session.startup()
        
        return {"success": success, "authenticated": success}
        
    except Exception as e:
        import traceback
        traceback.print_exc()
        return {"success": False, "error": str(e)}


@app.post("/save-cookies")
async def save_cookies(request: dict):
    """Save full cookie set from browser export
    
    Accepts cookies in various formats:
    - JSON array from browser extensions
    - Tab-separated values from DevTools
    """
    try:
        cookies = request.get('cookies', [])
        
        if not cookies:
            return {"success": False, "error": "No cookies provided"}
        
        selenium_cookies = []
        
        # Handle different cookie formats
        if isinstance(cookies, str):
            # Tab-separated from DevTools
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
            # JSON array format
            for cookie in cookies:
                selenium_cookie = {
                    'name': cookie.get('name', ''),
                    'value': cookie.get('value', ''),
                    'domain': cookie.get('domain', '.twitter.com'),
                    'path': cookie.get('path', '/'),
                    'secure': cookie.get('secure', True),
                    'httpOnly': cookie.get('httpOnly', False)
                }
                
                if 'expires' in cookie and cookie['expires']:
                    expiry = cookie['expires']
                    if expiry > 10000000000:  # Milliseconds
                        expiry = expiry / 1000
                    selenium_cookie['expiry'] = int(expiry)
                
                if 'sameSite' in cookie:
                    selenium_cookie['sameSite'] = cookie['sameSite']
                
                selenium_cookies.append(selenium_cookie)
        
        if not selenium_cookies:
            return {"success": False, "error": "No valid cookies found"}
        
        # Check for auth_token
        has_auth_token = any(c['name'] == 'auth_token' for c in selenium_cookies)
        if not has_auth_token:
            return {
                "success": False,
                "error": "Missing 'auth_token' cookie! Copy from Application > Cookies tab in DevTools."
            }
        
        # Save cookies
        os.makedirs(os.path.dirname(config.cookies_file), exist_ok=True)
        with open(config.cookies_file, 'wb') as f:
            pickle.dump(selenium_cookies, f)
        
        print(f"✅ Saved {len(selenium_cookies)} cookies from browser")
        
        # Restart authentication
        twitter_session.authenticated = False
        if twitter_session.driver:
            try:
                twitter_session.driver.quit()
            except:
                pass
        twitter_session.driver = None
        success = twitter_session.startup()
        
        if not success:
            return {
                "success": False,
                "error": "Cookies saved but authentication failed. Cookies may be expired."
            }
        
        return {
            "success": True,
            "cookies_count": len(selenium_cookies),
            "authenticated": success
        }
        
    except Exception as e:
        import traceback
        traceback.print_exc()
        return {"success": False, "error": str(e)}


@app.get("/login", response_class=HTMLResponse)
async def login_page():
    """Interactive login page for manual cookie import"""
    
    html_content = """
    <!DOCTYPE html>
    <html>
    <head>
        <title>Twitter Login - Found Footy</title>
        <meta charset="UTF-8">
        <meta name="viewport" content="width=device-width, initial-scale=1.0">
        <style>
            * { box-sizing: border-box; }
            body {
                margin: 0;
                padding: 20px;
                font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
                background: #15202b;
                color: white;
            }
            .container {
                max-width: 900px;
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
                font-size: 32px;
            }
            .status {
                background: #192734;
                padding: 20px;
                border-radius: 10px;
                margin-bottom: 20px;
                text-align: center;
            }
            .status.authenticated {
                background: #0d4d2d;
                border: 2px solid #1da252;
            }
            .status.checking {
                background: #3d3d0d;
                border: 2px solid #d4aa00;
            }
            .instructions {
                background: #192734;
                padding: 25px;
                border-radius: 10px;
                margin: 20px 0;
            }
            .instructions h3 {
                margin-top: 0;
                color: #1da1f2;
            }
            .instructions ol {
                margin: 10px 0;
                padding-left: 25px;
            }
            .instructions li {
                margin: 10px 0;
                line-height: 1.6;
            }
            .instructions code {
                background: #0d1117;
                padding: 3px 8px;
                border-radius: 3px;
                font-family: monospace;
            }
            button {
                background: #1da1f2;
                color: white;
                border: none;
                padding: 12px 24px;
                border-radius: 25px;
                font-size: 16px;
                font-weight: bold;
                cursor: pointer;
                transition: background 0.3s;
            }
            button:hover {
                background: #0d8ecf;
            }
            textarea, input[type="text"] {
                width: 100%;
                padding: 12px;
                border-radius: 5px;
                background: #0d1117;
                color: white;
                border: 2px solid #30363d;
                font-family: monospace;
                font-size: 14px;
                margin: 10px 0;
            }
            textarea {
                min-height: 150px;
                resize: vertical;
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
            a {
                color: #1da1f2;
                text-decoration: none;
            }
            a:hover {
                text-decoration: underline;
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
                <h3>📋 Quick Setup (Copy 3 Cookies):</h3>
                <ol>
                    <li>Go to <a href="https://twitter.com" target="_blank">twitter.com</a> and login</li>
                    <li>Press <strong>F12</strong> to open DevTools</li>
                    <li>Go to <strong>Application</strong> tab → <strong>Cookies</strong> → <strong>https://twitter.com</strong></li>
                    <li>Find and copy these THREE cookie values:</li>
                </ol>
                
                <div style="margin: 15px 0;">
                    <label style="display: block; margin: 10px 0; font-weight: bold;">1. auth_token (HttpOnly - 40 chars):</label>
                    <input type="text" id="authTokenInput" placeholder="e.g. 71a68c2ef4db94fbd251d38da9be7d7d8ac257f4">
                </div>
                
                <div style="margin: 15px 0;">
                    <label style="display: block; margin: 10px 0; font-weight: bold;">2. ct0 (CSRF token - 32 chars):</label>
                    <input type="text" id="ct0Input" placeholder="e.g. ae7025b6855aee0adb93f627b0c8e45f">
                </div>
                
                <div style="margin: 15px 0;">
                    <label style="display: block; margin: 10px 0; font-weight: bold;">3. twid (Twitter ID - starts with "u="):</label>
                    <input type="text" id="twidInput" placeholder="e.g. u=1234567890123456789">
                </div>
                
                <button onclick="saveAuthToken()" style="margin-top: 15px;">💾 Save Cookies &amp; Authenticate</button>
            </div>
            
            <div class="instructions">
                <h3>⚠️ Troubleshooting:</h3>
                <ul>
                    <li>Make sure you're logged into Twitter first</li>
                    <li>Copy the VALUE column, not the name</li>
                    <li>auth_token is marked as HttpOnly - you MUST copy from DevTools Application tab</li>
                    <li>Cookies expire after ~30 days - you'll need to re-authenticate</li>
                </ul>
            </div>
        </div>
        
        <script>
            // Check authentication status on load
            checkStatus();
            const statusInterval = setInterval(checkStatus, 5000);
            
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
                        messageP.textContent = 'Twitter service is ready! You can close this page.';
                        clearInterval(statusInterval);
                    } else {
                        statusDiv.className = 'status';
                        statusSpan.textContent = '❌ Not Authenticated';
                        messageP.textContent = 'Please copy the 3 cookies below to authenticate.';
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
                    alert('Please paste ALL THREE cookie values!');
                    return;
                }
                
                statusDiv.className = 'status checking';
                statusSpan.innerHTML = '<span class="spinner"></span> Saving...';
                messageP.textContent = 'Please wait...';
                
                try {
                    const response = await fetch('/save-auth-token', {
                        method: 'POST',
                        headers: { 'Content-Type': 'application/json' },
                        body: JSON.stringify({ auth_token: authToken, ct0: ct0, twid: twid })
                    });
                    
                    const data = await response.json();
                    
                    if (data.success && data.authenticated) {
                        statusDiv.className = 'status authenticated';
                        statusSpan.textContent = '✅ Success!';
                        messageP.textContent = 'Authentication successful! Service is ready.';
                        clearInterval(statusInterval);
                    } else {
                        statusDiv.className = 'status';
                        statusSpan.textContent = '❌ Failed';
                        messageP.textContent = data.error || 'Authentication failed. Please try again.';
                    }
                } catch (error) {
                    statusDiv.className = 'status';
                    statusSpan.textContent = '❌ Error';
                    messageP.textContent = 'Network error: ' + error.message;
                }
            }
        </script>
    </body>
    </html>
    """
    
    return HTMLResponse(content=html_content)


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host=config.host, port=config.port)
