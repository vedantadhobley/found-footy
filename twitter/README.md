# Twitter Scraper Service 🐦

Independent microservice for scraping Twitter videos using Selenium browser automation.

## 🎯 Overview

This service provides a REST API for searching Twitter videos. It's completely independent from the main Found Footy application and communicates via HTTP API.

**Key Features:**
- ✅ Cookie-based authentication (reliable, no CAPTCHA issues)
- ✅ Automated login with credentials from `.env`
- ✅ Persistent session with automatic cookie refresh
- ✅ Interactive login UI as fallback
- ✅ Runs in Docker with GUI support (via Xvfb)
- ✅ Zero dependencies on Found Footy codebase

## 📁 Structure

```
twitter/
├── __init__.py          # Package initialization
├── app.py               # FastAPI application & endpoints
├── auth.py              # Authentication logic (cookies, login)
├── session.py           # Browser session manager (Selenium)
├── config.py            # Configuration from environment
├── manual_login.py      # Helper script for interactive login
├── test_service.py      # Test suite
├── README.md            # This file
├── QUICKSTART.md        # Quick start guide
└── MIGRATION.md         # Migration notes

Note: Uses main project's Dockerfile and requirements.txt
```

## 🚀 Quick Start

### 1. Build & Start the Service

```bash
# From project root
docker compose up -d twitter-session
```

### 2. Authenticate (First Time)

The service will try to authenticate automatically using credentials from `.env`:

```env
TWITTER_USERNAME=REDACTED_USERNAME
TWITTER_PASSWORD=REDACTED_PASSWORD
TWITTER_EMAIL=REDACTED_EMAIL
```

**If automated login fails** (Twitter may require CAPTCHA/2FA), use the manual login UI:

```bash
# Open in browser
http://localhost:3103/login
```

Follow the instructions to copy 3 cookies from your browser's DevTools.

### 3. Verify Authentication

```bash
curl http://localhost:3103/health
```

Expected response when authenticated:
```json
{
  "status": "healthy",
  "authenticated": true,
  "session_timeout": 3600
}
```

## 🔌 API Endpoints

### GET `/health`

Health check endpoint.

**Response:**
```json
{
  "status": "healthy",
  "authenticated": true,
  "session_timeout": 3600
}
```

### POST `/search`

Search Twitter for videos.

**Request:**
```json
{
  "search_query": "Messi Barcelona goal",
  "max_results": 3
}
```

**Response:**
```json
{
  "status": "success",
  "videos": [
    {
      "search_term": "Messi Barcelona goal",
      "tweet_url": "https://twitter.com/user/status/1234567890",
      "tweet_id": "1234567890",
      "tweet_text": "Amazing goal by Messi!",
      "username": "Unknown",
      "timestamp": "2024-01-01T12:00:00Z",
      "discovered_at": "2024-01-01T12:00:00Z",
      "video_page_url": "https://x.com/i/status/1234567890",
      "requires_ytdlp": true
    }
  ],
  "count": 1
}
```

### POST `/authenticate`

Force re-authentication.

**Request:**
```json
{
  "force_reauth": true,
  "interactive": false
}
```

**Response:**
```json
{
  "status": "success",
  "authenticated": true
}
```

### GET `/login`

Interactive HTML page for manual cookie import. Open in browser: http://localhost:3103/login

### POST `/save-auth-token`

Save the 3 critical Twitter cookies manually.

**Request:**
```json
{
  "auth_token": "71a68c2ef4db94fbd251d38da9be7d7d8ac257f4",
  "ct0": "ae7025b6855aee0adb93f627b0c8e45f",
  "twid": "u=1234567890123456789"
}
```

### POST `/save-cookies`

Save full cookie set from browser export.

**Request:**
```json
{
  "cookies": [
    {
      "name": "auth_token",
      "value": "...",
      "domain": ".twitter.com",
      "path": "/",
      "secure": true,
      "httpOnly": true
    }
  ]
}
```

## 🔐 Authentication Methods

The service tries authentication in this order:

### 1. Cookie Loading (Fastest ⚡)
- Loads saved cookies from `/data/twitter_cookies.pkl`
- Validates by checking if user is logged in
- Used on every startup after initial authentication

### 2. Automated Login
- Uses credentials from environment variables
- Attempts programmatic login via Selenium
- Handles email verification if required
- Saves cookies on success

### 3. Interactive Login (Fallback)
- Opens browser window (requires GUI/X11)
- Waits for manual login completion
- Saves cookies automatically

### 4. Manual Cookie Import (Last Resort)
- Use the `/login` UI to copy cookies from DevTools
- Works even without GUI access
- Most reliable for accounts with 2FA

## ⚙️ Configuration

Set via environment variables:

```env
# Authentication (required)
TWITTER_USERNAME=REDACTED_USERNAME
TWITTER_PASSWORD=REDACTED_PASSWORD
TWITTER_EMAIL=REDACTED_EMAIL

# Session management
TWITTER_COOKIES_FILE=/data/twitter_cookies.pkl
SESSION_TIMEOUT=3600  # 1 hour

# Browser settings
TWITTER_HEADLESS=true  # Run without visible browser
DISPLAY=:99            # X11 display for headless

# Chrome driver
CHROMEDRIVER_PATH=/usr/bin/chromedriver

# Service settings
TWITTER_SERVICE_HOST=0.0.0.0
TWITTER_SERVICE_PORT=8888

# Search defaults
DEFAULT_MAX_RESULTS=3
SEARCH_TIMEOUT=5  # Page load timeout in seconds
```

## 🐳 Docker Integration

### Production (`docker-compose.yml`)

```yaml
twitter-session:
  image: found-footy:latest  # Uses same image as main app
  container_name: found-footy-twitter-session
  command: python -m twitter.app
  ports:
    - "3103:8888"
  env_file:
    - .env
  volumes:
    - twitter_cookies:/data  # Persistent cookies
  networks:
    - found-footy-prod
```

### Development (`docker-compose.dev.yml`)

```yaml
twitter:
  build:
    context: .
    dockerfile: Dockerfile.dev
  ports:
    - "3103:8888"
  command: python -m twitter.app
  volumes:
    - .:/workspace  # Full project for live reload
    - twitter_cookies_dev:/data
```

**Note:** Twitter service runs from the same Docker image as the main application. No separate build needed!

## 🔧 Development

### Run Locally (Outside Docker)

```bash
# From project root (not inside twitter/)

# Install all project dependencies
pip install -r requirements.txt

# Ensure Chrome and ChromeDriver are installed
# On Ubuntu:
sudo apt-get install chromium-browser chromium-chromedriver

# Set environment variables
export TWITTER_USERNAME=your_username
export TWITTER_PASSWORD=your_password
export TWITTER_EMAIL=your_email
export TWITTER_COOKIES_FILE=./cookies.pkl
export CHROMEDRIVER_PATH=/usr/bin/chromedriver
export PYTHONPATH=.

# Run the service
python -m twitter.app
```

### Manual Login Helper

```bash
# Inside Docker container
docker compose exec twitter-session python -m twitter.manual_login

# Or locally
python -m twitter.manual_login
```

### Test API

```bash
# Health check
curl http://localhost:3103/health

# Search for videos
curl -X POST http://localhost:3103/search \
  -H "Content-Type: application/json" \
  -d '{"search_query": "Messi goal", "max_results": 3}'
```

## 🛠️ Troubleshooting

### Service Not Authenticated

**Symptom:** `/health` returns `"authenticated": false`

**Solutions:**
1. Check credentials in `.env` file
2. Try manual login: http://localhost:3103/login
3. Check container logs: `docker compose logs twitter-session`

### ChromeDriver Not Found

**Symptom:** Error about chromedriver path

**Solution:**
```bash
# Rebuild Docker image
docker compose build twitter-session
```

### Cookies Expired

**Symptom:** Was working, now not authenticated

**Solution:**
Twitter cookies expire after ~30 days. Re-authenticate:
```bash
# Visit login UI
http://localhost:3103/login

# Or use manual login script
docker compose exec twitter-session python -m twitter.manual_login
```

### Search Returns No Results

**Symptom:** `/search` returns empty videos array

**Possible causes:**
1. Not authenticated - check `/health`
2. Search query too specific - try broader terms
3. Twitter's search is down/rate-limited

**Debug:**
```bash
# Check logs for detailed error messages
docker compose logs -f twitter-session
```

### GUI Needed but Running Headless

**Symptom:** Interactive login fails with "no display"

**Solution:**
The Docker container runs Xvfb (virtual display). No physical GUI needed.
If you need to see the browser:
1. Set `TWITTER_HEADLESS=false` in `.env`
2. Forward X11: `xhost +local:docker`
3. Mount display: `-v /tmp/.X11-unix:/tmp/.X11-unix`

## 📊 Monitoring

### Check Service Status

```bash
# Container status
docker compose ps twitter-session

# Service logs
docker compose logs -f twitter-session

# Health check
curl http://localhost:3103/health
```

### Metrics

The service logs important events:
- `🚀 Starting Twitter Session Service...` - Startup
- `✅ Loaded N cookies` - Cookie loading
- `🔐 Attempting automated login...` - Login attempt
- `✅ Twitter authentication successful` - Auth success
- `🔍 Searching: [URL]` - Search initiated
- `✅ Found N videos` - Search complete

## 🔄 Integration with Found Footy

The main application (`src/jobs/scrape_twitter.py`) calls this service:

```python
import requests
import os

session_url = os.getenv('TWITTER_SESSION_URL', 'http://twitter-session:8888')

response = requests.post(
    f"{session_url}/search",
    json={"search_query": "Messi Barcelona", "max_results": 10},
    timeout=60
)

videos = response.json()["videos"]
```

The Twitter service is **completely independent** - it doesn't import anything from Found Footy and doesn't access its database.

## 📦 Deployment

### Update to New Version

```bash
# Rebuild main image (includes twitter service)
docker compose build app-base

# Restart service (keeps cookies)
docker compose up -d twitter-session
```

### Reset Authentication

```bash
# Remove saved cookies
docker compose down twitter-session
docker volume rm found-footy_twitter_cookies

# Restart and re-authenticate
docker compose up -d twitter-session
# Then visit http://localhost:3103/login
```

## 🧪 Testing

```bash
# Test authentication
curl http://localhost:3103/health

# Test search
curl -X POST http://localhost:3103/search \
  -H "Content-Type: application/json" \
  -d '{
    "search_query": "football goal highlights",
    "max_results": 2
  }' | jq

# Test re-authentication
curl -X POST http://localhost:3103/authenticate \
  -H "Content-Type: application/json" \
  -d '{"force_reauth": true}'
```

## 📝 Notes

- **Cookie Lifespan:** Twitter cookies last ~30 days. Re-authentication needed after expiry.
- **Rate Limiting:** Twitter may rate-limit searches. The service doesn't handle this - your application should implement retry logic.
- **Session Timeout:** Default 1 hour. Configurable via `SESSION_TIMEOUT` env var.
- **Headless Mode:** Runs in Docker with virtual display (Xvfb). No physical monitor needed.
- **Security:** Cookies are stored in Docker volume. Don't commit `/data/twitter_cookies.pkl` to git.

## 🤝 Contributing

This service is part of the Found Footy project but operates independently.

To modify:
1. Edit files in `twitter/` directory
2. Test locally or rebuild Docker image
3. Ensure backward compatibility with existing API

## 📄 License

Part of Found Footy project.
