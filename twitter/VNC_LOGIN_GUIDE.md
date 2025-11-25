# Twitter Login via Browser GUI 🐦🖥️

## What Just Happened?

Your Twitter service now runs with a **full browser accessible via web interface**!

- ✅ VNC server running inside container
- ✅ noVNC web interface at http://localhost:6080
- ✅ Full Chromium browser visible and controllable
- ✅ Cookies automatically saved after login

## How to Login to Twitter

### Step 1: Open the Browser GUI

Open your regular web browser (Firefox, Chrome, etc.) and go to:

```
http://localhost:6080/vnc.html
```

You'll see a virtual desktop with a simple window manager (Fluxbox).

### Step 2: Start the Twitter Login Process

In your terminal, run:

```bash
sudo docker compose -f docker-compose.dev.yml exec twitter python -m twitter.manual_login
```

This will:
1. Open Chromium browser in the VNC desktop (you'll see it at http://localhost:6080)
2. Navigate to Twitter login page
3. Wait for you to login

### Step 3: Login to Twitter

In the noVNC browser window:
1. You'll see Chromium open to Twitter's login page
2. Enter your credentials manually (or it will try auto-login first)
3. Complete any verification (email, 2FA, etc.)
4. **Wait until you see your Twitter home feed**

### Step 4: Confirm Login

Back in your terminal where you ran `manual_login`, press **ENTER** when you're logged in and on the home page.

The script will:
- ✅ Extract cookies from the browser
- ✅ Save them to `/data/twitter_cookies.pkl` (persistent volume)
- ✅ Future startups will use these cookies automatically

## Using the Service

### Check if Authenticated

```bash
curl http://localhost:3103/health
```

Should return:
```json
{
  "status": "healthy",
  "authenticated": true
}
```

### Search for Videos

```bash
curl -X POST http://localhost:3103/search \
  -H "Content-Type: application/json" \
  -d '{"search_query": "Messi goal", "max_results": 3}'
```

### Test Everything

```bash
python twitter/test_service.py
```

## Environment Variables in .env

Make sure you have:

```bash
# Twitter credentials for automated login (tried first)
TWITTER_USERNAME=YourUsername
TWITTER_PASSWORD=YourPassword  
TWITTER_EMAIL=your.email@example.com

# Cookie storage (persistent across restarts)
TWITTER_COOKIES_FILE=/data/twitter_cookies.pkl

# Headless mode (false = GUI visible via VNC)
TWITTER_HEADLESS=false
```

## How It Works

1. **Container starts** → VNC server + noVNC web interface launch
2. **Twitter service starts** → Tries to authenticate:
   - First: Load saved cookies (if they exist)
   - Second: Try automated login with credentials from .env
   - Third: Wait for manual login via browser
3. **After manual login once** → Cookies saved, future restarts auto-authenticate
4. **Search endpoint** → Uses authenticated browser session to scrape Twitter

## Troubleshooting

### Can't see browser in noVNC?

Check if VNC is running:
```bash
sudo docker compose -f docker-compose.dev.yml logs twitter | grep "VNC server running"
```

Should see:
```
✅ VNC server running!
   📺 Access browser GUI at: http://localhost:6080/vnc.html
```

### Service not starting?

Check logs:
```bash
sudo docker compose -f docker-compose.dev.yml logs -f twitter
```

### Need to re-authenticate?

Delete cookies and restart:
```bash
sudo docker compose -f docker-compose.dev.yml exec twitter rm /data/twitter_cookies.pkl
sudo docker compose -f docker-compose.dev.yml restart twitter
```

Then run manual login again.

## Architecture

```
Your Ubuntu Desktop
    |
    | Open browser to http://localhost:6080
    v
noVNC Web Interface (port 6080)
    |
    | WebSocket connection
    v
VNC Server (port 5900) inside container
    |
    | Controls display :99
    v
Xvfb Virtual Display
    |
    | Shows GUI applications
    v
Chromium Browser (Selenium-controlled)
    |
    | Login to Twitter
    v
Twitter Session with cookies
    |
    | Saved to persistent volume
    v
Future automatic authentication
```

## Benefits of This Approach

✅ **No X11 forwarding complexity** - Just open a web page
✅ **Works from anywhere** - Even SSH without X forwarding
✅ **See what's happening** - Watch the browser in real-time
✅ **Persistent cookies** - Login once, use forever
✅ **Automated fallback** - Tries auto-login first, manual if needed
✅ **Container isolation** - Everything in Docker, clean host
✅ **Professional setup** - Same approach used in CI/CD pipelines

## Next Steps

1. **Login once** via VNC browser (manual_login.py)
2. **Cookies saved** automatically to persistent volume
3. **Restart anytime** - Auto-authenticates with saved cookies
4. **Run searches** - Service stays authenticated
5. **Integrate with Dagster** - Your jobs can call the API

Enjoy your fully automated Twitter scraper! 🚀
