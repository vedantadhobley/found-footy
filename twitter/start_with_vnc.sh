#!/bin/bash
# Start VNC server and Twitter service with GUI access

set -e

echo "🖥️  Starting VNC server for browser GUI access..."

# Clean up any stale X server lock files
rm -f /tmp/.X99-lock
rm -f /tmp/.X11-unix/X99

# Start Xvfb (virtual display)
Xvfb :99 -screen 0 1920x1080x24 &
export DISPLAY=:99
sleep 2

# Start lightweight window manager
fluxbox &
sleep 1

# Start VNC server (allows remote control of the display)
x11vnc -display :99 -forever -shared -rfbport 5900 -nopw -bg -quiet
sleep 1

# Start noVNC (web interface to VNC)
websockify --web=/usr/share/novnc 6080 localhost:5900 &
sleep 1

echo "✅ VNC server running!"
echo "   📺 Access browser GUI at: http://localhost:6080/vnc.html"
echo "   (Chrome will launch when you run manual_login or make API requests)"
echo ""

# Start Twitter service
echo "🐦 Starting Twitter service..."
exec python -m twitter.app
