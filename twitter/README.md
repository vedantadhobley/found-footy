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
| `/verify-and-switch` | POST | Re-verify auth after manual login |

### POST `/search`

```bash
curl -X POST http://localhost:8888/search \
  -H "Content-Type: application/json" \
  -d '{"search_query": "Salah Liverpool goal", "max_results": 5}'
```

**Response:**
```json
{
  "videos": [
    {
      "tweet_url": "https://twitter.com/user/status/123",
      "tweet_text": "What a goal!",
      "video_page_url": "https://x.com/i/status/123"
    }
  ],
  "count": 1
}
```

**503 Error**: Authentication required - login via VNC

## Ports

| Environment | VNC Port | API Port |
|-------------|----------|----------|
| Development | 4103 | 8888 (internal) |
| Production | 3103 | 8888 (internal) |

## Cookie Backup

Cookies are automatically backed up to:
```
~/.config/found-footy/twitter_cookies.json
```

This file survives container rebuilds and is restored on startup.

## Integration

Called by Temporal TwitterWorkflow:

```python
response = requests.post(
    "http://twitter:8888/search",
    json={"search_query": "Liverpool goal", "max_results": 5},
    timeout=120
)
```

## Troubleshooting

### Can't login / bot detection
Use VNC at http://localhost:4103 - it's real Firefox, no Selenium.

### Session expired
Login via VNC again, or restart container (auto-restores cookies).

### Check logs
```bash
docker logs -f found-footy-twitter
```

Key messages:
- `✅ Authenticated via cookie restore!` - Ready
- `🔐 MANUAL TWITTER LOGIN REQUIRED` - Need VNC login
- `✅ Login verified!` - After manual login detected
