#!/bin/bash
# Start Twitter service in headless mode (no VNC)
# Used for scaled instances that don't need GUI access

set -e

echo "🐦 Starting Twitter service (headless mode)..."
echo "   Instance: ${TWITTER_INSTANCE_ID:-unknown}"
echo ""

# Firefox still needs a virtual display even in headless mode
# Clean up any stale X server lock files
rm -f /tmp/.X99-lock
rm -f /tmp/.X11-unix/X99

# Start Xvfb (virtual display) - suppress output
echo "🖥️  Starting Xvfb virtual display..."
Xvfb :99 -screen 0 1920x1080x24 > /dev/null 2>&1 &
export DISPLAY=:99
sleep 2
echo "✅ Xvfb ready"
echo ""

# Start Twitter service directly - no VNC, no fluxbox (not needed)
exec python -m twitter.app
