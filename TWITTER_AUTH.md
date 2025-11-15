# Twitter Authentication

The Twitter service uses persistent cookie-based authentication.

## How It Works

1. **On Startup**: Service tries to load cookies from `/data/twitter_cookies.pkl`
2. **If cookies exist and are valid**: ✅ Service starts authenticated
3. **If no cookies or invalid**: ⚠️ Service starts but shows `authenticated: false`

## First-Time Setup

When you first run the service (or after clearing volumes), you need to login once:

```bash
./scripts/login_twitter.sh
```

This will:
- Check if you're already authenticated
- Guide you through the login process
- Save cookies to a persistent Docker volume
- Cookies persist across restarts

## Check Authentication Status

```bash
curl http://localhost:8888/health
```

Should return:
```json
{
  "status": "healthy",
  "authenticated": true
}
```

## When to Re-authenticate

You only need to login again if:
- First time running the service
- After running `./start.sh -v` (wipes volumes)
- Twitter logged you out (cookies expired)
- Health check shows `authenticated: false`

## Architecture

### Cookie Storage
- Location: `/data/twitter_cookies.pkl` inside container
- Volume: `twitter_cookies` (Docker volume, persists)
- Format: Python pickle file with Selenium cookies

### Interactive Login
The service can open a browser for manual login, but this requires:
- X11 forwarding configured
- Setting `TWITTER_INTERACTIVE_LOGIN=true` in environment

For simplicity, use `./scripts/login_twitter.sh` instead, which handles the Docker setup.

## Troubleshooting

### "authenticated: false" on startup

Run the login script:
```bash
./scripts/login_twitter.sh
```

### Cookies keep expiring

Twitter may have logged you out. Re-run login script to get fresh cookies.

### Can't login interactively

The Docker container runs headless by default. Use the provided scripts which handle the browser setup:
- `./scripts/login_twitter.sh` - Quick helper (recommended)
- `./scripts/setup_twitter_docker.sh` - Full setup (advanced)

## Environment Variables

- `TWITTER_COOKIES_FILE`: Path to cookies file (default: `/data/twitter_cookies.pkl`)
- `TWITTER_INTERACTIVE_LOGIN`: Enable auto-login on startup (default: `false`)
- `DISPLAY`: X11 display for browser (default: `:99` for Xvfb)

## API Endpoints

### GET /health
Check authentication status

### POST /search
Search for videos (requires authentication)

### POST /authenticate
Force re-authentication:
```bash
curl -X POST http://localhost:8888/authenticate \
  -H "Content-Type: application/json" \
  -d '{"force_reauth": true, "interactive": false}'
```

Parameters:
- `force_reauth`: Force new authentication even if authenticated (default: `true`)
- `interactive`: Open browser for manual login (default: `false`, requires X11)
