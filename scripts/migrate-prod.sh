#!/usr/bin/env bash
# Build and run one immutable, explicit production database migration unit.
set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(git -C "$SCRIPT_DIR/.." rev-parse --show-toplevel)"
readonly PROD_NETWORK="found-footy-prod"

cd "$REPO_ROOT"

fail() {
  printf 'migration failed: %s\n' "$*" >&2
  exit 1
}

for command_name in date docker git; do
  command -v "$command_name" >/dev/null || fail "required command not found: $command_name"
done

GIT_SHA="$(git rev-parse HEAD)"
[[ "$GIT_SHA" =~ ^[0-9a-f]{40}$ ]] || fail "could not resolve a full commit SHA"
[[ -z "$(git status --porcelain --untracked-files=normal)" ]] || fail "working tree must be clean"
BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
IMAGE_TAG="migration-$GIT_SHA"
readonly GIT_SHA BUILT_AT IMAGE_TAG

docker network inspect "$PROD_NETWORK" >/dev/null
docker build \
  --build-arg BINARY=worker \
  --build-arg GIT_SHA="$GIT_SHA" \
  --build-arg BUILT_AT="$BUILT_AT" \
  -t "found-footy-migrate:$IMAGE_TAG" \
  .

[[ "$(git rev-parse HEAD)" == "$GIT_SHA" ]] || fail "HEAD changed during migration build"
[[ -z "$(git status --porcelain --untracked-files=normal)" ]] || fail "working tree changed during migration build"

docker run --rm \
  --name "found-footy-prod-migrate-${GIT_SHA:0:12}" \
  --memory 1g \
  --network "$PROD_NETWORK" \
  --env-file .env \
  --entrypoint /usr/local/bin/migrate \
  "found-footy-migrate:$IMAGE_TAG"

printf 'migration verified: %s\n' "$GIT_SHA"
