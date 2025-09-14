#!/usr/bin/env bash
set -euo pipefail

echo "🚀 Found Footy"

cmd="${1:-redeploy}"
svc="${2:-}"

redeploy() {
  echo "🔄 Redeploying (local rebuild, no pulling)..."
  export DOCKER_BUILDKIT=1 COMPOSE_DOCKER_CLI_BUILD=1
  docker compose down --remove-orphans || true
  docker compose build              # rebuild images from local changes (cached)
  docker compose up -d --force-recreate  # ensure fresh containers
  echo "📦 Applying Prefect deployments..."
  docker compose run --rm app python found_footy/flows/deployments.py --apply || true
  echo "✅ Redeploy complete"
  docker compose ps
}

case "$cmd" in
  redeploy|"")
    redeploy
    ;;
  logs)
    if [ -n "${svc}" ]; then
      docker compose logs -f "${svc}"
    else
      docker compose logs -f
    fi
    ;;
  status|ps)
    docker compose ps
    ;;
  down)
    docker compose down --volumes
    ;;
  *)
    echo "Usage: ./start.sh [redeploy|logs [service]|status|ps|down]"
    exit 1
    ;;
esac