#!/usr/bin/env bash
# Verify a built headless image's disposable storage with isolated Compose sentinels.
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export FF_STORAGE_IMAGE="${1:?usage: bash scripts/smoke_twitter_storage.sh <built-headless-image>}"

# Inspect the final image, including inherited metadata, not just our Dockerfile.
# Docker may omit or null the Volumes map when the image declares no volumes.
volume_count="$(docker image inspect "$FF_STORAGE_IMAGE" --format '{{with index .Config "Volumes"}}{{len .}}{{else}}0{{end}}')"
[[ "$volume_count" == 0 ]] || { printf 'storage smoke failed: image declares implicit volumes\n' >&2; exit 1; }

FF_STORAGE_COOKIE_DIR="$(mktemp -d /tmp/ff-storage-smoke.XXXXXXXX)"
export FF_STORAGE_COOKIE_DIR
FF_STORAGE_HOST_GID="$(id -g)"
export FF_STORAGE_HOST_GID
# Keep the real image user. A supplemental test-only group grants access to
# synthetic cookies without making the temporary directory world-writable.
chmod 0770 "$FF_STORAGE_COOKIE_DIR"
readonly project="ff-storage-smoke-$(basename "$FF_STORAGE_COOKIE_DIR" | tr '[:upper:].' '[:lower:]-')"
compose=(docker compose --project-name "$project" --file "$SCRIPT_DIR/testdata/twitter-storage.compose.yml")
legacy_volume=""

# Remove only resources created by this test project. Leave failure visible if
# Docker cannot clean them; never use a daemon-wide prune or production Compose.
cleanup() {
  local result=$?
  trap - EXIT
  if ! "${compose[@]}" down --volumes --timeout 2; then
    printf 'storage smoke cleanup failed for project %s\n' "$project" >&2
    result=1
  fi
  if [[ -n "$legacy_volume" ]] && docker volume inspect "$legacy_volume" >/dev/null 2>&1; then
    docker volume rm "$legacy_volume" || result=1
  fi
  rm -f -- "$FF_STORAGE_COOKIE_DIR/twitter_cookies.json"
  rmdir -- "$FF_STORAGE_COOKIE_DIR" || result=1
  exit "$result"
}
trap cleanup EXIT

# First simulate the old mount, then remove that declaration during recreation.
# The explicitly captured test volume is the only orphan this script may delete.
"${compose[@]}" --file "$SCRIPT_DIR/testdata/twitter-storage-legacy.compose.yml" up -d --no-build --pull never
legacy_id="$("${compose[@]}" ps -q search)"
legacy_volume="$(docker inspect "$legacy_id" --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}')"
[[ -n "$legacy_volume" ]] || { printf 'legacy profile mount missing\n' >&2; exit 1; }
"${compose[@]}" exec -T search sh -c 'printf "legacy-profile\n" > /data/legacy-sentinel'
"${compose[@]}" up -d --no-deps --no-build --pull never --force-recreate search
search_id="$("${compose[@]}" ps -q search)"
mount_count="$(docker inspect "$search_id" --format '{{len .Mounts}}')"
[[ "$mount_count" == 1 ]] || { printf 'unexpected search mount count: %s\n' "$mount_count" >&2; exit 1; }
mount="$(docker inspect "$search_id" --format '{{range .Mounts}}{{.Type}} {{.Destination}}{{end}}')"
[[ "$mount" == 'bind /config' ]] || { printf 'unexpected search mount: %s\n' "$mount" >&2; exit 1; }

"${compose[@]}" exec -T search sh -c '
  test ! -e /data/legacy-sentinel
  mkdir -p /data/firefox-profile
  printf "private-profile\n" > /data/firefox-profile/storage-sentinel
  printf "synthetic-cookie\n" > /config/twitter_cookies.json
'
if docker volume inspect "$legacy_volume" >/dev/null 2>&1; then
  docker volume rm "$legacy_volume"
fi
legacy_volume=""
"${compose[@]}" exec -T login sh -c 'printf "manual-profile\n" > /data/storage-sentinel'

# A process/container restart preserves the writable layer; replacement does not.
"${compose[@]}" restart --timeout 1 search
"${compose[@]}" exec -T search sh -c 'test "$(cat /data/firefox-profile/storage-sentinel)" = private-profile'
"${compose[@]}" up -d --no-deps --no-build --pull never --force-recreate search
replacement_id="$("${compose[@]}" ps -q search)"
[[ "$replacement_id" != "$search_id" ]] || { printf 'search was not recreated\n' >&2; exit 1; }
"${compose[@]}" exec -T search sh -c '
  test ! -e /data/firefox-profile/storage-sentinel
  test "$(cat /config/twitter_cookies.json)" = synthetic-cookie
  test -w /data
'
"${compose[@]}" up -d --no-deps --no-build --pull never --force-recreate login
"${compose[@]}" exec -T login sh -c '
  test "$(cat /data/storage-sentinel)" = manual-profile
  test "$(cat /config/twitter_cookies.json)" = synthetic-cookie
'

# Only the explicitly declared manual-profile volume may exist for this project.
volume_names="$(docker volume ls --filter "label=com.docker.compose.project=$project" --format '{{.Name}}')"
[[ "$volume_names" == "${project}_manual-profile" ]] || { printf 'unexpected test volumes: %s\n' "$volume_names" >&2; exit 1; }
printf 'storage smoke verified: writable-layer profile, persistent cookie bind and manual profile\n'
