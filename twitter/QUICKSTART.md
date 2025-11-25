# Twitter Service - Quick Start Guide 🚀

## Step 1: Start the Service

```bash
# From project root
docker compose up -d twitter-session
```

## Step 2: Check Status

```bash
curl http://localhost:3103/health
```

If you see `"authenticated": true`, you're done! ✅

If you see `"authenticated": false`, continue to Step 3.

## Step 3: Authenticate

### Option A: Automated Login (Easiest)

The service will try to login automatically using credentials from your `.env` file:

```env
TWITTER_USERNAME=REDACTED_USERNAME
TWITTER_PASSWORD=REDACTED_PASSWORD
TWITTER_EMAIL=REDACTED_EMAIL
```

Check logs to see if it worked:
```bash
docker compose logs twitter-session
```

### Option B: Manual Cookie Import (Most Reliable)

If automated login fails (Twitter requires CAPTCHA or 2FA):

1. **Open the login UI in your browser:**
   ```
   http://localhost:3103/login
   ```

2. **Login to Twitter in a separate tab:**
   - Go to https://twitter.com
   - Login with your account
   
3. **Copy 3 cookies from DevTools:**
   - Press `F12` to open DevTools
   - Go to **Application** tab
   - Expand **Cookies** → Click **https://twitter.com**
   - Find and copy these THREE cookie VALUES:
     - `auth_token` (40 characters, HttpOnly)
     - `ct0` (32 characters)
     - `twid` (starts with "u=")
   
4. **Paste into the form on the login UI**
   - The service will save cookies and authenticate

5. **Verify authentication:**
   ```bash
   curl http://localhost:3103/health
   ```

## Step 4: Test Search

```bash
curl -X POST http://localhost:3103/search \
  -H "Content-Type: application/json" \
  -d '{"search_query": "Messi goal", "max_results": 3}'
```

You should get a JSON response with video results!

## Step 5: Integration

Your application is already configured to use this service via `TWITTER_SESSION_URL`:

```python
# src/jobs/scrape_twitter.py
session_url = os.getenv('TWITTER_SESSION_URL', 'http://twitter-session:8888')
response = requests.post(f"{session_url}/search", json={...})
```

## Common Issues

### "Not authenticated" after automated login
- Twitter may require CAPTCHA or 2FA
- Use Option B (manual cookie import)

### "ChromeDriver not found"
- Rebuild: `docker compose build twitter-session`

### "Cookies expired"
- Cookies last ~30 days
- Re-authenticate using http://localhost:3103/login

### Search returns empty results
- Check `/health` - must be authenticated
- Try broader search terms
- Check logs: `docker compose logs -f twitter-session`

## That's It! 🎉

Your Twitter scraper is now running and ready to search for videos.

For more details, see the full [README.md](README.md).
