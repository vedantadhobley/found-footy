#!/bin/bash
# Quick Twitter login helper
# Run this if the service shows "authenticated: false"

echo "🐦 Twitter Login Helper"
echo "======================="
echo ""

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
echo "This will:"
echo "  1. Open a browser window inside the Docker container"
echo "  2. Let you manually login to Twitter"
echo "  3. Save cookies for future use"
echo ""
read -p "Press ENTER to continue (or Ctrl+C to cancel)..."

echo ""
echo "🌐 Opening browser for login..."
echo "   (Browser runs inside Docker container with X11 forwarding)"
echo ""

# Run the manual login script inside the container
docker compose exec twitter-session python twitter_manual_login.py

# Check result
echo ""
echo "📊 New status:"
curl -s http://localhost:8888/health | python3 -m json.tool

# If still not authenticated, suggest restart
if ! curl -s http://localhost:8888/health | grep -q '"authenticated": true'; then
    echo ""
    echo "💡 If cookies were saved, restart the service:"
    echo "   docker compose restart twitter-session"
fi
