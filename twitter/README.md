# Twitter Scraper Service 🐦

Independent microservice for scraping Twitter videos using Firefox with VNC access.

## 🎯 Overview

This service provides a REST API for searching Twitter videos. It uses a **two-mode approach** to bypass Twitter's bot detection:

1. **Manual Login Mode**: Firefox runs directly (NO Selenium) - zero bot detection
2. **Scraping Mode**: Selenium uses the authenticated profile for automation

**Key Features:**
- ✅ VNC GUI access - http://localhost:4103
- ✅ **No bot detection** - manual Firefox for login bypasses `navigator.webdriver`
- ✅ Persistent Firefox profile (stay logged in across restarts)
- ✅ Selenium-powered scraping after manual auth

## 📁 Structure

```
twitter/
├── __init__.py          # Package initialization
├── app.py               # FastAPI application & endpoints
├── auth.py              # Authentication helpers
├── session.py           # Browser session manager (dual-mode)
├── config.py            # Configuration from environment
├── start_with_vnc.sh    # Startup script with VNC server
└── README.md            # This file
```

## 🚀 Quick Start

### 1. Build & Start

```bash
docker compose -f docker-compose.dev.yml up -d twitter
```

### 2. Authenticate (First Time)

1. **Open VNC:** http://localhost:4103
2. **Login manually:** Type your credentials in Firefox (it's REAL Firefox, no bot detection!)
3. **Verify:** Call the verify endpoint or restart the container

```bash
# After logging in via VNC:
curl -X POST http://localhost:8888/auth/verify
```

### 3. Scraping Enabled!

Once verified, Selenium uses the authenticated profile for scraping.

```bash
curl http://localhost:8888/health
# Returns: {"status": "healthy", "authenticated": true, ...}
```

## 🔌 API Endpoints

All endpoints are on the **internal API port** (8888 inside Docker). VNC is on port 4103.

### GET `/health`

```bash
curl http://localhost:8888/health
```

### POST `/search`

```bash
curl -X POST http://localhost:8888/search \
  -H "Content-Type: application/json" \
  -d '{"search_query": "Messi goal", "max_results": 3}'
```

### POST `/auth/verify`

Call this after logging in via VNC to switch to Selenium scraping mode:

```bash
curl -X POST http://localhost:8888/auth/verify
```

### POST `/auth/launch-browser`

Re-launch Firefox if it was closed:

```bash
curl -X POST http://localhost:8888/auth/launch-browser
```

## 🔐 Authentication Flow

### Why Two Modes?

Twitter detects Selenium via `navigator.webdriver = true`. When Selenium controls Firefox:
- `navigator.webdriver` → `true` (detected!)
- Login blocked by Twitter's bot protection

When Firefox runs directly (no Selenium):
- `navigator.webdriver` → `undefined` (normal browser!)
- Login works normally

### The Flow

1. **Container starts** → checks if profile is authenticated
2. **If not authenticated:**
   - Launches Firefox directly (no Selenium)
   - You login via VNC at http://localhost:4103
   - Call `/auth/verify` when done
3. **After verification:**
   - Manual Firefox closes
   - Selenium starts with same profile (already has cookies)
   - Scraping enabled!

### On Restart

If the profile has valid cookies, Selenium works immediately - no manual login needed.

## 🐳 Docker Ports

| Port | Service | Access |
|------|---------|--------|
| 4103 | VNC (noVNC) | http://localhost:4103 |
| 8888 | API (internal) | `http://twitter:8888` from other containers |

## 🛠️ Troubleshooting

### Bot detection / Can't login

1. Make sure you're using VNC: http://localhost:4103
2. The Firefox there should be **manual Firefox** (not Selenium-controlled)
3. Login normally like a regular browser
4. Call `/auth/verify` after

### Browser closed / need to re-login

```bash
curl -X POST http://localhost:8888/auth/launch-browser
```

### Session expired

Just restart and login again via VNC:

```bash
docker compose -f docker-compose.dev.yml restart twitter
```

### Check if authenticated

```bash
curl http://localhost:8888/health
```

## 📊 Logs

```bash
docker compose -f docker-compose.dev.yml logs -f twitter
```

Key messages:
- `✅ Already logged in via saved profile!` - Ready to scrape
- `🦊 Manual Firefox launched` - Need to login via VNC
- `✅ Login verified! Selenium ready for scraping` - After `/auth/verify`

## 🔄 Integration

The Temporal workflow calls this service:

```python
import requests

response = requests.post(
    "http://twitter:8888/search",
    json={"search_query": "Liverpool goal", "max_results": 5},
    timeout=60
)
videos = response.json()["videos"]
```
