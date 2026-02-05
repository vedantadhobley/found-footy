"""FastAPI application for Twitter scraping service

Provides REST API endpoints for:
- Health checking
- Video search
- Authentication via VNC
"""
import threading
import sys
from contextlib import asynccontextmanager
from typing import List
from fastapi import FastAPI, HTTPException
from fastapi.responses import JSONResponse, RedirectResponse
from pydantic import BaseModel

from .config import TwitterConfig
from .session import TwitterSessionManager, TwitterAuthError
from .twitter_logging import log

MODULE = "twitter_app"

# Initialize configuration and session manager
config = TwitterConfig()
twitter_session = TwitterSessionManager(config)

# Instance identification for registry
import os
import socket
INSTANCE_ID = os.getenv("TWITTER_INSTANCE_ID", socket.gethostname())
SERVICE_PORT = os.getenv("TWITTER_SERVICE_PORT", "8888")
CONTAINER_NAME = os.getenv("HOSTNAME", f"twitter-{INSTANCE_ID}")


def get_instance_url() -> str:
    """Get the URL other services should use to reach this instance."""
    # In Docker, use container name for internal communication
    return f"http://{CONTAINER_NAME}:{SERVICE_PORT}"


def register_with_registry():
    """Register this instance with the service registry."""
    try:
        from src.scaler.registry import registry
        url = get_instance_url()
        registry.register(INSTANCE_ID, url)
        log.info(MODULE, "registry_registered", "Registered with registry",
                 instance_id=INSTANCE_ID, url=url)
    except Exception as e:
        log.warning(MODULE, "registry_failed", "Could not register with registry (non-fatal)",
                    error=str(e))


def heartbeat_loop():
    """Send periodic heartbeat to registry."""
    import time
    while True:
        try:
            from src.scaler.registry import registry
            registry.heartbeat(INSTANCE_ID)
        except Exception:
            pass
        time.sleep(10)  # Heartbeat every 10 seconds


def background_startup():
    """Run startup in background thread so FastAPI can start immediately"""
    twitter_session.startup()
    # Register with registry after startup
    register_with_registry()
    # Start heartbeat in another thread
    heartbeat_thread = threading.Thread(target=heartbeat_loop, daemon=True)
    heartbeat_thread.start()


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Application lifespan manager - startup and shutdown"""
    # Start browser auth in background thread
    startup_thread = threading.Thread(target=background_startup, daemon=True)
    startup_thread.start()
    log.info(MODULE, "fastapi_started", "FastAPI started - browser auth running in background",
             instance_id=INSTANCE_ID)
    yield
    # Shutdown - unregister from registry
    try:
        from src.scaler.registry import registry
        registry.unregister(INSTANCE_ID)
    except Exception:
        pass
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
    exclude_urls: List[str] = []
    max_age_minutes: int = 5  # Stop scrolling when tweet is older than this


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


@app.get("/status")
async def get_status():
    """Get detailed status including whether instance is busy.
    
    Used by scaler to determine if safe to scale down.
    """
    return {
        "instance_id": INSTANCE_ID,
        "healthy": twitter_session.startup_complete and twitter_session.authenticated,
        "authenticated": twitter_session.authenticated,
        "busy": getattr(twitter_session, 'busy', False),
        "search_in_progress": getattr(twitter_session, 'busy', False),
    }


@app.post("/search")
async def search_videos(request: VideoSearchRequest):
    """Search Twitter for videos
    
    Uses time-based scrolling: scrolls through "Latest" results until finding
    a tweet older than max_age_minutes, then stops. Returns ALL videos found.
    
    Args:
        search_query: Search terms (e.g., "Messi Barcelona goal")
        exclude_urls: List of URLs to skip (already processed)
        max_age_minutes: Stop scrolling when tweet is older than this (default: 5)
        
    Returns:
        JSON with discovered videos (all videos found, no limit)
        
    Raises:
        503 Service Unavailable if not authenticated (manual login required)
        500 Internal Server Error for other failures
    """
    try:
        videos = twitter_session.search_videos(
            search_query=request.search_query,
            exclude_urls=request.exclude_urls,
            max_age_minutes=request.max_age_minutes
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


class VideoDownloadRequest(BaseModel):
    video_url: str
    output_path: str


@app.post("/download_video")
async def download_video(request: VideoDownloadRequest):
    """Download video using browser - bypasses yt-dlp rate limits
    
    Navigates to the tweet in the authenticated browser session,
    extracts the video CDN URL (video.twimg.com), and downloads directly.
    
    This avoids yt-dlp's 403 rate limit issues since we use the same
    authenticated browser session that Twitter recognizes.
    
    Returns:
        Download status with path and CDN URL used
    """
    try:
        if not twitter_session.authenticated:
            return JSONResponse(
                status_code=401,
                content={"status": "error", "error": "Not authenticated"}
            )
        
        result = twitter_session.download_video_direct(
            request.video_url,
            request.output_path
        )
        
        if result.get("status") == "success":
            return result
        else:
            return JSONResponse(
                status_code=500,
                content=result
            )
        
    except TwitterAuthError as e:
        return JSONResponse(
            status_code=401,
            content={"status": "error", "error": str(e), "auth_required": True}
        )
    except Exception as e:
        return JSONResponse(
            status_code=500,
            content={"status": "error", "error": str(e)}
        )


if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host=config.host, port=config.port)
