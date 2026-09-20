#!/usr/bin/env bash
# Run one guarded production operation from the exact image used by both live workers.
set -euo pipefail

if [[ $# -eq 0 ]]; then
  printf 'usage: %s <found-footy-ops arguments...>\n' "$0" >&2
  exit 2
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

worker_one_image="$(docker inspect --format '{{.Config.Image}}' found-footy-prod-worker-1)"
worker_two_image="$(docker inspect --format '{{.Config.Image}}' found-footy-prod-worker-2)"
if [[ "$worker_one_image" != "$worker_two_image" ]]; then
  printf 'production workers disagree: worker-1=%s worker-2=%s\n' \
    "$worker_one_image" "$worker_two_image" >&2
  exit 1
fi

readonly image_prefix='found-footy-worker:'
if [[ "$worker_one_image" != "$image_prefix"* ]]; then
  printf 'unexpected production worker image: %s\n' "$worker_one_image" >&2
  exit 1
fi
image_tag="${worker_one_image#"$image_prefix"}"
if [[ -z "$image_tag" || "$image_tag" == latest ]]; then
  printf 'production worker image is not immutable: %s\n' "$worker_one_image" >&2
  exit 1
fi

export IMAGE_TAG="$image_tag"
exec docker compose -f docker-compose.prod.yml run --rm --no-deps \
  --entrypoint /usr/local/bin/found-footy-ops worker "$@"
