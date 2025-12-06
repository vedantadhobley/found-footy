#!/bin/bash
# Start VNC server and Twitter service with GUI access

set -e

echo "🖥️  Starting VNC server for browser GUI access..."

# Clean up any stale X server lock files
rm -f /tmp/.X99-lock
rm -f /tmp/.X11-unix/X99

# Start Xvfb (virtual display) - suppress output
Xvfb :99 -screen 0 1920x1080x24 > /dev/null 2>&1 &
export DISPLAY=:99
sleep 2

# Start lightweight window manager - suppress output
fluxbox > /dev/null 2>&1 &
sleep 1

# Start VNC server - use -q for quiet mode
x11vnc -display :99 -forever -shared -rfbport 5900 -nopw -bg -q
sleep 1

# Start noVNC (web interface to VNC) with auto-redirect to vnc.html
# Redirect websockify logs to /dev/null to suppress 404 spam
websockify --web=/usr/share/novnc 6080 localhost:5900 > /dev/null 2>&1 &
sleep 1

# Create index redirect to vnc.html
mkdir -p /usr/share/novnc
echo '<html><head><meta http-equiv="refresh" content="0;url=/vnc.html"></head></html>' > /usr/share/novnc/index.html

echo "✅ VNC server running!"
echo "   📺 Firefox GUI: http://localhost:4103"
echo ""

# Start Twitter service
echo "🐦 Starting Twitter service..."
exec python -m twitter.app
