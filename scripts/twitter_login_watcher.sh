#!/bin/bash
# Twitter Login Watcher
# Run this in the background to auto-open browser when Twitter needs login
#
# Usage: ./scripts/twitter_login_watcher.sh &
#
# Or add to your shell startup for always-on monitoring

SIGNAL_FILE=".open_vnc_signal"
VNC_URL="http://localhost:4103"
CHECK_INTERVAL=2

echo "👀 Watching for Twitter login requests..."
echo "   Signal file: $SIGNAL_FILE"
echo "   Press Ctrl+C to stop"
echo ""

last_mtime=0

while true; do
    if [ -f "$SIGNAL_FILE" ]; then
        current_mtime=$(stat -c %Y "$SIGNAL_FILE" 2>/dev/null || echo "0")
        
        if [ "$current_mtime" != "$last_mtime" ] && [ "$current_mtime" != "0" ]; then
            echo ""
            echo "🔔 Twitter login required!"
            echo "   Opening browser..."
            
            # Try to open browser (works on Linux with xdg-open, macOS with open)
            if command -v xdg-open &> /dev/null; then
                xdg-open "$VNC_URL" 2>/dev/null &
            elif command -v open &> /dev/null; then
                open "$VNC_URL" 2>/dev/null &
            elif command -v sensible-browser &> /dev/null; then
                sensible-browser "$VNC_URL" 2>/dev/null &
            else
                echo "   ⚠️  Could not auto-open browser"
                echo "   Please open manually: $VNC_URL"
            fi
            
            last_mtime="$current_mtime"
            
            # Also show a desktop notification if possible
            if command -v notify-send &> /dev/null; then
                notify-send "Twitter Login Required" "Click to open VNC: $VNC_URL" --urgency=critical 2>/dev/null
            fi
        fi
    fi
    
    sleep $CHECK_INTERVAL
done
