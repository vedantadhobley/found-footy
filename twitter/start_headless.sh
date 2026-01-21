#!/bin/bash
# Start Twitter service in headless mode (no VNC)
# Used for scaled instances that don't need GUI access

set -e

echo "🐦 Starting Twitter service (headless mode)..."
echo "   Instance: ${TWITTER_INSTANCE_ID:-unknown}"
echo ""

# Start Twitter service directly - no Xvfb, no VNC
exec python -m twitter.app
