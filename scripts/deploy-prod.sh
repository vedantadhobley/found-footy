#!/usr/bin/env bash
# Build, roll out, and verify one immutable production application release.
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(git -C "$SCRIPT_DIR/.." rev-parse --show-toplevel)"
readonly COMPOSE_FILE="$REPO_ROOT/docker-compose.prod.yml"
readonly PROD_NETWORK="found-footy-prod"
readonly VERIFY_ATTEMPTS=45
readonly VERIFY_INTERVAL_SECONDS=2

cd "$REPO_ROOT"

fail() {
  printf 'release failed: %s\n' "$*" >&2
  exit 1
}

for command_name in awk curl date docker git stat; do
  command -v "$command_name" >/dev/null || fail "required command not found: $command_name"
done

assert_clean_checkout() {
  local current_sha dirty
  current_sha="$(git rev-parse HEAD)"
  dirty="$(git status --porcelain --untracked-files=normal)"
  [[ "$current_sha" == "$GIT_SHA" ]] || fail "HEAD changed during release: expected $GIT_SHA, found $current_sha"
  if [[ -n "$dirty" ]]; then
    printf 'release failed: working tree changed during release; commit or remove these paths first:\n%s\n' "$dirty" >&2
    exit 1
  fi
}

GIT_SHA="$(git rev-parse HEAD)"
[[ "$GIT_SHA" =~ ^[0-9a-f]{40}$ ]] || fail "could not resolve a full commit SHA"
BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
IMAGE_TAG="$GIT_SHA"
export GIT_SHA BUILT_AT IMAGE_TAG

assert_clean_checkout

compose=(docker compose -f "$COMPOSE_FILE" --profile vnc)

# A name locates one event browser, but the fleet label plus target network
# prove that it belongs to this deployment. This catches both legacy unscoped
# names and FF-001's workspace-conventional scoped names without teaching the
# release script either naming format.
list_active_fleet() {
  docker ps \
    --filter "network=$PROD_NETWORK" \
    --filter "label=found-footy.fleet=firefox" \
    --format '{{.Names}}'
}

assert_no_active_fleet() {
  local active_fleet
  active_fleet="$(list_active_fleet)"
  if [[ -n "$active_fleet" ]]; then
    printf 'release failed: production event browsers are active; wait for their workflows to finish:\n%s\n' "$active_fleet" >&2
    exit 1
  fi
}

assert_no_active_fleet

mapfile -t vnc_before < <("${compose[@]}" ps -q twitter-vnc)
services=(worker api twitter)
if ((${#vnc_before[@]} > 0)); then
  services+=(twitter-vnc)
fi

printf 'release identity\n  git_sha: %s\n  built_at: %s\n  image_tag: %s\n' \
  "$GIT_SHA" "$BUILT_AT" "$IMAGE_TAG"

"${compose[@]}" config --quiet
"${compose[@]}" build worker api twitter twitter-vnc
"$REPO_ROOT/scripts/smoke_prod_perms.sh" "found-footy-worker:$IMAGE_TAG"

# Prevent a concurrent edit or checkout from making the labels disagree with
# the actual build context. Recheck the fleet after the potentially long build
# so a goal detected during that window cannot be stranded on the old image.
# Everything above this line is non-disruptive.
assert_clean_checkout
assert_no_active_fleet

"${compose[@]}" up -d --no-deps --no-build --force-recreate "${services[@]}"

container_ip() {
  docker inspect --format "{{with index .NetworkSettings.Networks \"$PROD_NETWORK\"}}{{.IPAddress}}{{end}}" "$1"
}

verify_metrics() {
  local container_id="$1" binary="$2" attempt ip body line
  for ((attempt = 1; attempt <= VERIFY_ATTEMPTS; attempt++)); do
    ip="$(container_ip "$container_id")"
    if [[ -n "$ip" ]] && body="$(curl --fail --silent --show-error --max-time 2 "http://$ip:8080/metrics" 2>/dev/null)"; then
      line="$(awk '/^found_footy_deploy_git_sha_info[{]/{print; exit}' <<<"$body")"
      if [[ "$line" == *"binary=\"$binary\""* \
        && "$line" == *"git_sha=\"$GIT_SHA\""* \
        && "$line" == *"image_tag=\"$IMAGE_TAG\""* \
        && "$line" == *"built_at=\"$BUILT_AT\""* ]]; then
        printf 'verified %s in %s\n' "$binary" "$container_id"
        return 0
      fi
    fi
    sleep "$VERIFY_INTERVAL_SECONDS"
  done
  fail "$binary container $container_id did not expose the expected release identity"
}

verify_twitter() {
  local container_id="$1" role="$2" attempt ip body
  for ((attempt = 1; attempt <= VERIFY_ATTEMPTS; attempt++)); do
    ip="$(container_ip "$container_id")"
    if [[ -n "$ip" ]] && body="$(curl --fail --silent --show-error --max-time 2 "http://$ip:8888/status" 2>/dev/null)"; then
      if [[ "$body" == *"\"git_sha\":\"$GIT_SHA\""* \
        && "$body" == *"\"built_at\":\"$BUILT_AT\""* \
        && "$body" == *"\"image_tag\":\"$IMAGE_TAG\""* ]]; then
        printf 'verified %s in %s\n' "$role" "$container_id"
        return 0
      fi
    fi
    sleep "$VERIFY_INTERVAL_SECONDS"
  done
  fail "$role container $container_id did not expose the expected release identity"
}

mapfile -t worker_ids < <("${compose[@]}" ps -q worker)
[[ ${#worker_ids[@]} -eq 2 ]] || fail "expected 2 running worker containers, found ${#worker_ids[@]}"
for container_id in "${worker_ids[@]}"; do
  verify_metrics "$container_id" worker
done

mapfile -t api_ids < <("${compose[@]}" ps -q api)
[[ ${#api_ids[@]} -eq 1 ]] || fail "expected 1 running API container, found ${#api_ids[@]}"
verify_metrics "${api_ids[0]}" api

mapfile -t twitter_ids < <("${compose[@]}" ps -q twitter)
[[ ${#twitter_ids[@]} -eq 1 ]] || fail "expected 1 running Twitter container, found ${#twitter_ids[@]}"
verify_twitter "${twitter_ids[0]}" twitter

if ((${#vnc_before[@]} > 0)); then
  mapfile -t vnc_after < <("${compose[@]}" ps -q twitter-vnc)
  [[ ${#vnc_after[@]} -eq 1 ]] || fail "Twitter VNC was running before rollout but is not running now"
  verify_twitter "${vnc_after[0]}" twitter-vnc
fi

printf 'release verified: %s\n' "$GIT_SHA"
