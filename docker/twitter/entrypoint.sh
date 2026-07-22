#!/bin/bash
# Twitter service entrypoint. Conditionally boots the VNC stack
# (Xvfb + fluxbox + x11vnc + websockify+noVNC) before exec'ing the
# Go binary, driven by TWITTER_VNC_MODE.
#
# TWITTER_VNC_MODE=true  → VNC container: start display server +
#                          window manager + VNC server + noVNC web
#                          bridge, set DISPLAY=:99, then exec the
#                          twitter binary with TWITTER_HEADLESS=false
#                          so Playwright uses the visible display.
# TWITTER_VNC_MODE unset  → Headless container: skip all VNC daemons,
#                          exec the binary. Playwright runs headless.
#
# Runtime-gate rather than build-gate keeps ONE image for both roles;
# compose sets the env var per service. Matches Python's shape (one
# image, two entrypoint scripts) except we collapse to one script
# that branches on env, which is one fewer file.

set -e

if [ "$TWITTER_VNC_MODE" = "true" ]; then
    # Force headless=false so Playwright renders into the Xvfb display
    # we're about to start. Compose CAN set this itself but doing it
    # here means the invariant is guaranteed regardless of .env.
    export TWITTER_HEADLESS=false

    # Clean up any stale X server lock files from a prior crashed run.
    # Not sudoing — pwuser writes to /tmp fine; if these were owned by
    # root from an earlier misconfig, the rm may fail silently and Xvfb
    # will fail loudly on the port.
    rm -f /tmp/.X99-lock /tmp/.X11-unix/X99 2>/dev/null || true

    # Xvfb :99 — headless X display. 1920x1080x24 matches Python and
    # gives a reasonable canvas for the operator viewing via noVNC.
    Xvfb :99 -screen 0 1920x1080x24 > /dev/null 2>&1 &
    export DISPLAY=:99
    # Poll for the socket rather than blindly sleeping — sleep 2 in
    # Python was fine, but this is more resilient to slow container
    # cold starts.
    for _ in {1..20}; do
        [ -S /tmp/.X11-unix/X99 ] && break
        sleep 0.1
    done

    # fluxbox — minimal window manager. Firefox launched under bare
    # Xvfb renders windows without decorations or WM interaction; the
    # operator needs a normal-looking browser window.
    fluxbox > /dev/null 2>&1 &
    sleep 0.5

    # x11vnc — expose the Xvfb display over VNC protocol.
    #   -forever   keep serving after client disconnects
    #   -shared    allow multiple concurrent viewers (rare but fine)
    #   -nopw      no VNC password; auth is via Caddy + tailnet
    #              (external access already gated at the network layer)
    #   -rfbport   fixed port so websockify can find it
    #   -bg -q     background + quiet (still logs errors to stderr)
    x11vnc -display :99 -forever -shared -rfbport 5900 -nopw -bg -q

    # websockify + noVNC — HTML/JS VNC client accessible via HTTP.
    # noVNC ships as static files at /usr/share/novnc; websockify
    # serves them AND proxies the VNC WebSocket to x11vnc's TCP port.
    # Access via http://<host>:6080/vnc.html.
    websockify --web=/usr/share/novnc 6080 localhost:5900 > /dev/null 2>&1 &

    # Redirect / to /vnc.html so the operator lands on the client
    # without typing the path. Same trick Python uses.
    mkdir -p /usr/share/novnc 2>/dev/null || true
    if [ -w /usr/share/novnc ]; then
        echo '<html><head><meta http-equiv="refresh" content="0;url=/vnc.html"></head></html>' \
            > /usr/share/novnc/index.html 2>/dev/null || true
    fi

    sleep 0.5
fi

# Exec the Go binary — becomes PID 1 so signals propagate cleanly
# (docker stop → SIGTERM reaches the app, not this script).
exec /usr/local/bin/twitter "$@"
