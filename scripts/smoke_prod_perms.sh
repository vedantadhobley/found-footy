#!/usr/bin/env bash
# smoke_prod_perms.sh — non-root prod-image permission smoke.
#
# The check that would have caught the 2026-08-15 La Liga cutover perm bugs.
# The prod image runs as non-root `app`; two runtime operations were denied
# because the image/compose weren't fully set up for non-root:
#   1. /var/run/docker.sock  — the #160 Firefox fleet (Provision/Release/Reap)
#   2. /scratch              — video DownloadAndStage scratch dir
# Both are invisible in dev, which runs as root. This runs the ACTUAL prod
# worker image as its non-root user + docker group, exactly like prod, and
# asserts it can do both — so a half-configured non-root image can't ship again.
#
# Usage:  scripts/smoke_prod_perms.sh [image]   (default: found-footy-worker:latest)
#
# TODO (full smoke — the durable deploy gate): go past perms to a real
# goal->clip cycle — provision one Firefox via the fleet, download + probe one
# clip, run vision — and fail if any stage errors. This stub covers the
# permission class that bit us; the full cycle is the follow-up.
set -euo pipefail

IMAGE="${1:-found-footy-worker:latest}"
SOCK=/var/run/docker.sock
GID="$(stat -c '%g' "$SOCK")"

echo "== non-root prod-image perm smoke =="
echo "   image: $IMAGE   host docker gid: $GID"

docker run --rm \
  --user app --group-add "$GID" \
  -v "$SOCK:$SOCK" \
  --entrypoint sh "$IMAGE" -c '
    set -e
    echo "   running as: $(id)"
    if [ -w /var/run/docker.sock ]; then
      echo "  ok    docker.sock writable   (fleet Provision/Release/Reap can reach the daemon)"
    else
      echo "  FAIL  docker.sock NOT writable -> the #160 fleet is dead in the water"; exit 1
    fi
    if mkdir -p /scratch/.smoke && rmdir /scratch/.smoke; then
      echo "  ok    /scratch writable      (video DownloadAndStage can stage clips)"
    else
      echo "  FAIL  /scratch NOT writable  -> every clip download is denied"; exit 1
    fi
  '

echo "== perm smoke PASSED — non-root image is complete =="
