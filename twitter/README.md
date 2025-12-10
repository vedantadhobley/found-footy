# Twitter Scraper Service 🐦

Independent microservice for scraping Twitter videos using Firefox with VNC access.

## Overview

This service provides a REST API for searching Twitter videos. It uses a **two-mode approach** to bypass Twitter's bot detection:

1. **Manual Login Mode**: Firefox runs directly (NO Selenium) - zero bot detection
2. **Scraping Mode**: Selenium uses the authenticated profile for automation

**Key Features:**
- ✅ VNC GUI access at http://localhost:4103 (dev) or http://localhost:3103 (prod)
- ✅ **No bot detection** - manual Firefox for login
- ✅ Persistent Firefox profile + cookie backup
- ✅ Automatic cookie restore on startup
- ✅ Selenium-powered scraping after auth
- ✅ **URL exclusion** - skip already-discovered videos
- ✅ **5 videos per search** (configurable via `max_results`)

## Structure

```
twitter/
├── app.py               # FastAPI endpoints (:8888)
├── session.py           # Browser session manager (dual-mode)
├── auth.py              # Authentication helpers
├── config.py            # Configuration from environment
├── start_with_vnc.sh    # Startup script with VNC server
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
    "max_results": 5,
    "exclude_urls": []
  }'
```

**Parameters:**
| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `search_query` | string | required | Search terms |
| `max_results` | int | 5 | Maximum videos to return |
| `exclude_urls` | list | [] | URLs to skip during scraping |

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
# First search
curl -X POST http://localhost:8888/search \
  -d '{"search_query": "Salah goal", "max_results": 5, "exclude_urls": []}'
# Returns: video1, video2, video3, video4, video5

# Second search - skip previously found
curl -X POST http://localhost:8888/search \
  -d '{"search_query": "Salah goal", "max_results": 5, "exclude_urls": ["https://x.com/i/status/video1", "https://x.com/i/status/video2", ...]}'
# Returns: video6, video7, video8, video9, video10 (NEW videos)
```

Logs show when URLs are skipped:
```
🔍 Searching: Salah goal (excluding 5 already-discovered URLs)
   ⏭️ Skipping already-discovered URL: https://x.com/user/status/123...
   ✅ Video #1: New goal clip found...
```

## Ports

| Environment | VNC Port | API Port |
|-------------|----------|----------|
| Development | 4103 | 8888 (internal) |
| Production | 3103 | 8888 (internal) |

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
        "search_query": "Liverpool goal",
        "max_results": 5,
        "exclude_urls": existing_video_urls  # From previous attempts
    },
    timeout=120
)
```

Each event gets **3 Twitter searches** with cumulative `exclude_urls`:
- Attempt 1: `exclude_urls=[]`
- Attempt 2: `exclude_urls=[URLs from attempt 1]`
- Attempt 3: `exclude_urls=[URLs from attempts 1 & 2]`

This enables finding up to **15 unique videos per event** (5 × 3 attempts).

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
| `TWITTER_DEFAULT_MAX_RESULTS` | 5 | Default max videos |
