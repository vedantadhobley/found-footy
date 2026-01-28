# Twitter Scraper Service 🐦

Independent microservice for scraping Twitter videos using Firefox.

## Overview

This service provides a REST API for searching Twitter videos. It uses a **two-mode approach** to bypass Twitter's bot detection:

1. **Manual Login Mode**: Firefox runs directly (NO Selenium) - zero bot detection
2. **Scraping Mode**: Selenium uses the authenticated profile for automation

**Key Features:**
- ✅ **VNC GUI access** on twitter-vnc only (http://localhost:3103 prod, http://localhost:4103 dev)
- ✅ **Headless mode** for scaled twitter instances (no VNC, better scalability)
- ✅ **No bot detection** - manual Firefox for login
- ✅ Persistent Firefox profile + cookie backup (shared across all instances)
- ✅ Automatic cookie restore on startup
- ✅ Selenium-powered scraping after auth
- ✅ **URL exclusion** - skip already-discovered videos
- ✅ **Time-based scrolling** - returns ALL videos found within max_age_minutes
- ✅ **OR syntax search** - single search with multiple team aliases

## Structure

```
twitter/
├── app.py               # FastAPI endpoints (:8888)
├── session.py           # Browser session manager (dual-mode)
├── auth.py              # Authentication helpers
├── config.py            # Configuration from environment
├── start_with_vnc.sh    # Startup script WITH VNC (twitter-1)
├── start_headless.sh    # Startup script WITHOUT VNC (twitter-2+)
└── README.md            # This file
```

## Quick Start

### 1. Start Services

```bash
docker compose -f docker-compose.dev.yml up -d twitter
```

### 2. Check Status

```bash
curl http://localhost:8888/health
```

If `authenticated: false`, login via VNC.

### 3. First-Time Login

1. Open http://localhost:4103 (VNC)
2. Login to Twitter in Firefox (no bot detection!)
3. Service auto-detects login success
4. Cookies backed up automatically

## API Endpoints

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/health` | GET | Check auth status |
| `/search` | POST | Search for videos |
| `/authenticate` | POST | Force re-authentication |
| `/auth/verify` | POST | Verify after manual login |

### POST `/search`

```bash
curl -X POST http://localhost:8888/search \
  -H "Content-Type: application/json" \
  -d '{
    "search_query": "Salah Liverpool goal",
    "exclude_urls": [],
    "max_age_minutes": 5
  }'
```

**Parameters:**
| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `search_query` | string | required | Search terms (supports OR syntax: `player (team1 OR team2)`) |
| `exclude_urls` | list | [] | URLs to skip during scraping |
| `max_age_minutes` | int | 5 | Stop scrolling when tweet is older than this |

**Returns:** ALL videos found within the time window (no limit).

**Response:**
```json
{
  "status": "success",
  "videos": [
    {
      "tweet_url": "https://twitter.com/user/status/123",
      "tweet_text": "What a goal!",
      "video_page_url": "https://x.com/i/status/123",
      "tweet_id": "123",
      "duration_seconds": 15.0,
      "discovered_at": "2025-12-10T08:00:00Z"
    }
  ],
  "count": 1
}
```

**503 Error**: Authentication required - login via VNC

### URL Exclusion

The `exclude_urls` parameter prevents re-discovering the same videos:

```bash
# First search - returns all videos within 5 min
curl -X POST http://localhost:8888/search \
  -d '{"search_query": "Salah goal", "exclude_urls": []}'
# Returns: video1, video2, video3, ... (all found)

# Second search - skip previously found
curl -X POST http://localhost:8888/search \
  -d '{"search_query": "Salah goal", "exclude_urls": ["https://x.com/i/status/video1", "https://x.com/i/status/video2", ...]}'
# Returns: only NEW videos not in exclude_urls
```

Logs show when URLs are skipped:
```
🔍 Searching: Salah goal (excluding 5 already-discovered URLs)
   ⏭️ Skipping already-discovered URL: https://x.com/user/status/123...
   ✅ Video #1: New goal clip found...
```

## Ports

| Instance | Mode | VNC Port | API Port |
|----------|------|----------|----------|
| twitter-vnc (prod) | VNC | 3103 | 8888 (internal) |
| twitter (prod, scaled) | Headless | None | 8888 (internal) |
| twitter (dev) | VNC | 4103 | 8888 (internal) |

**Why headless for scaled instances?**
VNC is only needed for initial login and debugging. Once cookies are saved to `~/.config/found-footy/twitter_cookies.json`, all instances restore from the same backup. Headless mode:
- Reduces resource usage (no Xvfb, x11vnc, websockify)
- Eliminates port conflicts when scaling
- Faster startup

Use `twitter-vnc` service for manual cookie re-auth when needed.

## Cookie Backup

Cookies are automatically backed up to:
```
/config/twitter_cookies.json  (container)
~/.config/found-footy/twitter_cookies.json  (host)
```

This file survives container rebuilds and is restored on startup.

## Integration with Temporal

Called by TwitterWorkflow's `execute_twitter_search` activity:

```python
response = requests.post(
    "http://twitter:8888/search",
    json={
        "search_query": "Salah (Liverpool OR LFC OR Reds)",  # OR syntax for aliases
        "exclude_urls": existing_video_urls,  # From previous attempts
        "max_age_minutes": 3  # Stop at tweets older than 3 min
    },
    timeout=120
)
```

**Search Flow:**
1. Twitter returns ALL videos found within `max_age_minutes`
2. Workflow selects **top 5 longest videos** for download
3. Workflow saves ONLY selected videos to `_discovered_videos` in MongoDB
4. Each attempt adds downloaded URLs to `exclude_urls` for next attempt

**Why save after selection?** Videos not selected for download should remain discoverable in future attempts. Previously, saving all found videos would permanently exclude them even if never downloaded.

Each event gets **10 search attempts** over ~10 minutes:
- Attempt 1: `exclude_urls=[]`
- Attempt 2: `exclude_urls=[URLs from attempt 1]`
- etc.

This enables continuous discovery as new videos are posted.

## Video Discovery

The service extracts:
- **Tweet URL**: Original tweet link
- **Video Page URL**: Direct video page (for yt-dlp)
- **Tweet Text**: For context/filtering
- **Duration**: When available from player element

Videos are found by:
1. Searching Twitter with `filter:videos`
2. Scrolling through search results
3. Finding tweets with video elements
4. Extracting URLs and metadata

## Troubleshooting

### Can't login / bot detection
Use VNC at http://localhost:4103 - it's real Firefox, no Selenium.

### Session expired
Login via VNC again, or restart container (auto-restores cookies).

### Same videos returned every search
Ensure `exclude_urls` is being passed. Check logs for:
```
(excluding N already-discovered URLs)
```

### Search timeout
Increase timeout or check if Twitter is rate limiting. Default: 120s.

### Check logs
```bash
docker logs -f found-footy-twitter
```

Key messages:
- `✅ Authenticated via cookie restore!` - Ready
- `🔐 MANUAL TWITTER LOGIN REQUIRED` - Need VNC login
- `✅ Login verified!` - After manual login detected
- `🔍 Searching: X (excluding N already-discovered URLs)` - Search with exclusions
- `⏭️ Skipping already-discovered URL` - URL exclusion working

## Environment Variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `TWITTER_SERVICE_PORT` | 8888 | API port |
| `TWITTER_COOKIE_BACKUP_PATH` | `/config/twitter_cookies.json` | Cookie backup |
| `TWITTER_HEADLESS` | false | Headless browser mode |
| `TWITTER_SEARCH_TIMEOUT` | 5 | Seconds to wait for results |
| `MONGODB_URI` | (required) | MongoDB connection for registry |
