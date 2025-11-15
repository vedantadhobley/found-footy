#!/bin/bash
# Twitter Login - Works on both WSL and Linux with GUI

echo "🐦 Twitter Login"
echo "================"
echo ""

# Check if we're in WSL
if grep -qi microsoft /proc/version 2>/dev/null; then
    echo "📍 Detected: WSL (Windows Subsystem for Linux)"
    echo ""
    echo "Since WSL doesn't have a GUI, here's what to do:"
    echo ""
    echo "1. Open Chrome/Firefox on Windows"
    echo "2. Go to: https://twitter.com and login"
    echo "3. Once logged in, export cookies using a browser extension:"
    echo "   - Chrome: 'Cookie-Editor' or 'EditThisCookie'"
    echo "   - Firefox: 'Cookie-Editor'"
    echo "4. Export cookies as JSON file"
    echo "5. Save to: $(pwd)/.twitter_cookies.json"
    echo ""
    echo "Then run: docker compose exec twitter-session python twitter_import_cookies.py"
    echo ""
    exit 0
fi

# Check if DISPLAY is available
if [ -z "$DISPLAY" ]; then
    echo "❌ No DISPLAY found - cannot open GUI"
    echo ""
    echo "Try: export DISPLAY=:0"
    echo "Or use the manual cookie import method above"
    exit 1
fi

# Allow Docker to access X server
echo "🔓 Allowing Docker to access X server..."
xhost +local:docker > /dev/null 2>&1

# Check if service is running
if ! docker ps | grep -q found-footy-twitter-session; then
    echo "❌ Twitter service not running"
    echo "   Start with: ./start.sh"
    exit 1
fi

# Check current status
echo "📊 Current status:"
curl -s http://localhost:8888/health | python3 -m json.tool
echo ""

# Check if already authenticated
if curl -s http://localhost:8888/health | grep -q '"authenticated": true'; then
    echo "✅ Already authenticated! No action needed."
    exit 0
fi

echo "🔐 Starting manual login process..."
echo ""
echo "This will open a browser window where you can login to Twitter."
echo "Once logged in, cookies will be saved automatically."
echo ""
read -p "Press ENTER to continue (or Ctrl+C to cancel)..."

# Update docker-compose to use host DISPLAY
export DISPLAY_TO_USE="$DISPLAY"

# Restart twitter service with host DISPLAY
echo ""
echo "🔄 Restarting Twitter service with GUI access..."
docker compose stop twitter-session

# Run with host display
docker compose run --rm \
  -e DISPLAY="$DISPLAY_TO_USE" \
  -v /tmp/.X11-unix:/tmp/.X11-unix:rw \
  twitter-session python twitter_manual_login.py

echo ""
echo "📊 Final status:"
curl -s http://localhost:8888/health | python3 -m json.tool

echo ""
echo "🔄 Restarting service..."
docker compose up -d twitter-session

sleep 3
echo ""
echo "📊 Service status after restart:"
curl -s http://localhost:8888/health | python3 -m json.tool
