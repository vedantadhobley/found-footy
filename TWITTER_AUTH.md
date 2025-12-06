# Twitter Authentication

The Twitter service uses **browser automation with VNC access** for authentication and video searching.

## Architecture

```
┌─────────────────────────────────────────────────────┐
│ found-footy-twitter container                       │
│                                                     │
│  ┌─────────────────┐   ┌──────────────────────────┐ │
│  │ FastAPI :8888   │   │ Firefox Browser          │ │
│  │  /search        │──▶│ (Selenium + Profile)     │ │
│  │  /health        │   └──────────────────────────┘ │
│  └─────────────────┘              │                 │
│                                   │                 │
│  ┌─────────────────┐   ┌──────────┴───────────────┐ │
│  │ noVNC :6080     │──▶│ Xvfb Virtual Display :99 │ │
│  │  (Web access)   │   └──────────────────────────┘ │
│  └─────────────────┘                                │
└─────────────────────────────────────────────────────┘
        │
        │ Port 4103
        ▼
    http://localhost:4103 → VNC browser GUI
```

## How It Works

### Automatic Authentication Flow

On startup and before each search, the service:

1. **Check existing session** - Is browser alive and logged in?
2. **Try cookie restore** - Load from `~/.config/found-footy/twitter_cookies.json`
3. **Verify login** - Navigate to twitter.com/home and check for redirect
4. **If all fail** → Launch Firefox for manual login, send email notification

### Cookie Backup

Cookies are automatically:
- **Exported** after successful login to `~/.config/found-footy/twitter_cookies.json`
- **Restored** on container restart before authentication check
- **Refreshed** after each successful search

## Quick Start

### 1. Start Services
```bash
docker compose -f docker-compose.dev.yml up -d
```

### 2. Check Status
```bash
# Health check
curl http://localhost:8888/health

# Should return:
# {"status": "ready", "authenticated": true}
```

### 3. First-Time Login (if needed)

If `authenticated: false`, open VNC:

```
http://localhost:4103
```

This opens a browser showing Firefox. Log into Twitter:
1. Navigate to twitter.com
2. Enter credentials
3. Complete any 2FA
4. Service auto-detects login success
5. Cookies backed up automatically

## API Endpoints

### GET /health
```bash
curl http://localhost:8888/health
```

Returns:
```json
{"status": "ready", "authenticated": true}
```

Or if login needed:
```json
{"status": "login_required", "authenticated": false, "vnc_url": "http://localhost:4103"}
```

### POST /search
```bash
curl -X POST http://localhost:8888/search \
  -H "Content-Type: application/json" \
  -d '{"search_query": "Salah Liverpool goal", "max_results": 5}'
```

Returns:
```json
{
  "videos": [
    {
      "tweet_url": "https://twitter.com/user/status/123",
      "tweet_text": "What a goal by Salah!",
      "video_page_url": "https://x.com/i/status/123"
    }
  ],
  "count": 1
}
```

**Error (503)**: Authentication required - open VNC to login

### POST /verify-and-switch
Force re-verify authentication after manual login:
```bash
curl -X POST http://localhost:8888/verify-and-switch
```

## Cookie Backup Location

```
~/.config/found-footy/twitter_cookies.json
```

This file is:
- **Host-mounted** - Survives container rebuilds
- **Auto-created** - After first successful login
- **JSON format** - Human-readable for debugging

## Troubleshooting

### "authenticated: false" or 503 errors

1. Open VNC: http://localhost:4103
2. Log into Twitter manually
3. Wait for auto-detection (service logs show success)

### VNC Not Accessible

```bash
# Check container is running
docker ps | grep twitter

# Check logs
docker logs found-footy-twitter

# Restart if needed
docker compose -f docker-compose.dev.yml restart twitter
```

### Cookies Not Restoring

Check the backup file exists:
```bash
ls -la ~/.config/found-footy/twitter_cookies.json
```

If missing, login via VNC - it will be created automatically.

### Twitter Login Loop

Twitter may require additional verification. Use VNC to:
1. Complete any CAPTCHA
2. Verify phone/email
3. Accept new terms

## Environment Variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `TWITTER_SERVICE_PORT` | 8888 | API port |
| `TWITTER_COOKIE_BACKUP_PATH` | `/config/twitter_cookies.json` | Cookie backup location |
| `TWITTER_HEADLESS` | false | Run browser headless (no GUI) |
| `VNC_PUBLIC_URL` | http://localhost:4103 | VNC URL in notifications |

## Session Lifecycle

```mermaid
stateDiagram-v2
    [*] --> Startup
    Startup --> CheckSession: Service starts
    CheckSession --> Authenticated: Session valid
    CheckSession --> TryCookies: Session invalid
    TryCookies --> Authenticated: Cookies restored
    TryCookies --> NeedLogin: Cookies failed/missing
    NeedLogin --> WaitingForLogin: Launch Firefox, send notification
    WaitingForLogin --> Authenticated: Manual login detected
    Authenticated --> Search: /search called
    Search --> Authenticated: Success (backup cookies)
    Search --> NeedLogin: Session expired
```

## Email Notifications (Production)

When login is needed, the service can send email notifications. Configure:

```bash
TWITTER_NOTIFY_EMAIL=you@example.com
SMTP_HOST=smtp.gmail.com
SMTP_PORT=587
SMTP_USER=your-email@gmail.com
SMTP_PASS=app-password
```
