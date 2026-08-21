#!/usr/bin/env bash
# Start the raw Firefox login terminal, VNC bridge, and cookie-capture service.
set -euo pipefail

readonly display_number=:99
readonly profile_dir="${TWITTER_AUTH_PROFILE_DIR:-/data/firefox-profile}"
readonly login_url="${TWITTER_AUTH_LOGIN_URL:-https://x.com/i/flow/login}"

mkdir -p "$profile_dir"
rm -f /tmp/.X99-lock /tmp/.X11-unix/X99 2>/dev/null || true

Xvfb "$display_number" -screen 0 1920x1080x24 >/dev/null 2>&1 &
for _ in {1..50}; do
    [[ -S /tmp/.X11-unix/X99 ]] && break
    sleep 0.1
done
[[ -S /tmp/.X11-unix/X99 ]] || {
    printf 'raw Firefox auth failed: Xvfb did not become ready\n' >&2
    exit 1
}

export DISPLAY="$display_number"
export MOZ_CRASHREPORTER_DISABLE=1
export NO_AT_BRIDGE=1

fluxbox >/dev/null 2>&1 &
x11vnc -display "$display_number" -forever -shared -rfbport 5900 -nopw -bg -q
websockify --web=/usr/share/novnc 6080 localhost:5900 >/dev/null 2>&1 &

firefox-esr --no-remote --new-instance --profile "$profile_dir" "$login_url" \
    >/dev/null 2>&1 &
firefox_pid=$!
/usr/local/bin/twitter-auth &
auth_pid=$!

# If the capture service exits, the container is incomplete and Firefox stops.
# If Firefox exits normally after login, keep capture alive: closing Firefox is
# what releases its exclusive cookies.sqlite lock for the atomic export.
set +e
wait -n "$firefox_pid" "$auth_pid"
status=$?
if kill -0 "$auth_pid" 2>/dev/null; then
    wait "$auth_pid"
    exit $?
fi
kill "$firefox_pid" 2>/dev/null || true
wait "$firefox_pid" 2>/dev/null || true
exit "$status"
