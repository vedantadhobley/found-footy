#!/usr/bin/env bash
# Idempotent Garage bootstrap for found-footy (dev or prod).
#
# Provisions a fresh Garage volume in ONE command — layout assign+apply,
# bucket create, S3 key IMPORT (from .env, so worker/api authenticate with
# the key value they already hold), bucket grant. Every step tolerates a
# re-run, so this is safe to run repeatedly and safe after a partial/aborted
# provision. Replaces the 7-step copy-paste in docs/deployment.md whose
# manual, non-idempotent steps are easy to fumble mid-cutover.
#
# WHY it's load-bearing: s3.New() HeadBucket-probes at worker/api startup and
# exits(1) if the bucket/key are missing — and that probe runs BEFORE Temporal
# schedule creation, so an unprovisioned Garage leaves the WHOLE pipeline dark
# (no ingest, no polling), not just blob storage. Run this between `up` and
# expecting green workers on any fresh reset.
#
# Usage:
#   scripts/garage-bootstrap.sh <garage-container> [capacity] [tag]
# Examples:
#   scripts/garage-bootstrap.sh found-footy-prod-garage 50GB prod
#   scripts/garage-bootstrap.sh found-footy-dev-garage  10GB dev
set -euo pipefail

CONTAINER="${1:?usage: garage-bootstrap.sh <garage-container> [capacity] [tag]}"
CAPACITY="${2:-50GB}"
TAG="${3:-prod}"
ZONE="dc1"
BUCKET="found-footy"
KEYNAME="found-footy-key"

# Load the S3 key so the imported Garage key matches worker/api creds. Each
# Garage is a separate single-node instance; sharing the key *value* grants no
# cross-instance access (both are internal), it just lets one .env authenticate
# against either.
ENV_FILE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/.env"
[ -f "$ENV_FILE" ] || { echo "FATAL: $ENV_FILE not found" >&2; exit 1; }
# shellcheck disable=SC1090
set -a; source "$ENV_FILE"; set +a
: "${S3_ACCESS_KEY_ID:?S3_ACCESS_KEY_ID missing from .env}"
: "${S3_SECRET_ACCESS_KEY:?S3_SECRET_ACCESS_KEY missing from .env}"

G() { docker exec "$CONTAINER" /garage "$@"; }

# Run a garage step; succeed if it works OR if it already-exists/already-applied
# (idempotent re-run). Any other stderr is a real failure and aborts (set -e).
step() {
  local desc="$1"; shift
  local out
  if out=$(G "$@" 2>&1); then
    echo "✓ $desc"
    [ -n "$out" ] && echo "$out" | sed 's/^/    /'
  elif grep -qiE 'already|exist|committed|no changes|not staged' <<<"$out"; then
    echo "✓ $desc (already done)"
  else
    echo "✗ $desc" >&2; echo "$out" | sed 's/^/    /' >&2; return 1
  fi
}

echo "== Garage bootstrap: $CONTAINER (capacity=$CAPACITY tag=$TAG) =="

NODE=$(G status | grep -oE '^[0-9a-f]{16}' | head -1 || true)
[ -n "$NODE" ] || { echo "FATAL: could not read Garage node id from '$CONTAINER' — is it up?" >&2; exit 1; }
echo "  node=$NODE"

# Only assign+apply if the node has no committed role yet — avoids leaving a
# stray staged layout version on re-runs.
if G layout show 2>&1 | grep -qE "$NODE"; then
  echo "✓ layout already configured for $NODE — skipping assign/apply"
else
  step "layout assign" layout assign "$NODE" -z "$ZONE" -c "$CAPACITY" -t "$TAG"
  step "layout apply"  layout apply --version 1
fi

step "bucket create" bucket create "$BUCKET"
step "key import"    key import --yes -n "$KEYNAME" "$S3_ACCESS_KEY_ID" "$S3_SECRET_ACCESS_KEY"
step "bucket allow"  bucket allow --read --write --owner "$BUCKET" --key "$KEYNAME"

echo "== verify =="
G bucket list | sed 's/^/    /'
echo "done — worker + api should now log s3_connected on (re)start."
