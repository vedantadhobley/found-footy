"""FastAPI application for Twitter scraping service

Provides REST API endpoints for:
- Health checking
- Video search
- Authentication via VNC
"""
import threading
import sys
from contextlib import asynccontextmanager
from fastapi import FastAPI, HTTPException
from fastapi.responses import JSONResponse, RedirectResponse
from pydantic import BaseModel

from .config import TwitterConfig
from .session import TwitterSessionManager, TwitterAuthError

# Force unbuffered output so logs show immediately
sys.stdout.reconfigure(line_buffering=True)
sys.stderr.reconfigure(line_buffering=True)

# Initialize configuration and session manager
config = TwitterConfig()
twitter_session = TwitterSessionManager(config)


def background_startup():
    """Run startup in background thread so FastAPI can start immediately"""
    twitter_session.startup()


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Application lifespan manager - startup and shutdown"""
    # Start browser auth in background thread
    startup_thread = threading.Thread(target=background_startup, daemon=True)
    startup_thread.start()
    print("🚀 FastAPI started - browser auth running in background", flush=True)
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


# === API Endpoints ===

@app.get("/")
async def root():
    """Root endpoint - redirects to VNC for auth if needed"""
    if not twitter_session.authenticated:
        # Redirect to VNC for authentication
        return RedirectResponse(url="/vnc.html")
    
    return {
        "service": "Twitter Scraper",
        "version": "1.0.0",
        "status": "ready",
        "authenticated": True,
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
                "message": "Not authenticated - open http://localhost:4103 to login via VNC"
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
        
    Raises:
        503 Service Unavailable if not authenticated (manual login required)
        500 Internal Server Error for other failures
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
    except TwitterAuthError as e:
        # Authentication failed - this is a service unavailable situation
        # The caller should retry later after manual login
        raise HTTPException(
            status_code=503,
            detail={
                "error": "authentication_required",
                "message": str(e),
                "vnc_url": "http://localhost:4103",
                "action": "Manual login required via VNC"
            }
        )
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))


@app.post("/authenticate")
async def force_authenticate(request: AuthRequest = AuthRequest()):
    """Force re-authentication
    
    Args:
        force_reauth: Force new authentication even if already authenticated
        
    Returns:
        Authentication status
    """
    try:
        if request is None:
            request = AuthRequest()
        
        success = twitter_session.authenticate(force_reauth=request.force_reauth)
        
        return {
            "status": "success" if success else "pending",
            "authenticated": success,
            "message": "Authenticated!" if success else "Open http://localhost:4103 to login via VNC"
        }
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))


@app.post("/auth/verify")
async def verify_login():
    """Verify manual login and switch to Selenium mode
    
    Call this after logging in via VNC to confirm authentication
    and enable scraping.
    
    Returns:
        Authentication status
    """
    try:
        success = twitter_session.verify_and_switch_to_selenium()
        
        return {
            "status": "success" if success else "failed",
            "authenticated": success,
            "message": "Ready for scraping!" if success else "Login not detected - please login via VNC"
        }
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))


@app.post("/auth/launch-browser")
async def launch_browser():
    """Launch Firefox for manual login (no Selenium/bot detection)
    
    Use this to re-launch the browser if it was closed.
    
    Returns:
        Status
    """
    try:
        twitter_session._launch_manual_firefox()
        
        return {
            "status": "success",
            "message": "Firefox launched - open http://localhost:4103 to login"
        }
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host=config.host, port=config.port)
