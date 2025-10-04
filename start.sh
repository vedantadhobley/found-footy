#!/usr/bin/env bash
set -euo pipefail

echo "🚀 Found Footy"
echo "🏗️ Architecture: $(uname -m)"
echo "🐧 Platform: $(uname -s)"

cmd="${1:-redeploy}"
svc="${2:-}"

redeploy() {
  echo "🔄 Redeploying (local rebuild, no pulling)..."
  
  # Check if .env exists
  if [ ! -f .env ]; then
    echo "⚠️ .env file not found!"
    echo "📝 Please copy .env.template to .env and fill in your credentials:"
    echo "   cp .env.template .env"
    echo "   nano .env  # or vim .env"
    exit 1
  fi
  
  echo "✅ .env file found"
  
  export DOCKER_BUILDKIT=1 COMPOSE_DOCKER_CLI_BUILD=1
  docker compose down --remove-orphans || true
  docker compose build
  docker compose up -d --force-recreate
  echo "📦 Applying Prefect deployments..."
  docker compose run --rm app python found_footy/flows/deployments.py --apply || true
  echo "✅ Redeploy complete"
  docker compose ps
}

test_integration() {
  echo "🧪 Running Integration Test..."
  
  # Ensure test container is running
  if ! docker-compose ps test | grep -q "Up"; then
    echo "🔄 Starting test container..."
    docker-compose up -d test
    sleep 5
  fi
  
  echo "🚀 Executing integration test..."
  docker-compose exec test python /app/scripts/test_integration_real.py
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
  test-integration)
    test_integration
    ;;
  status|ps)
    docker compose ps
    ;;
  down)
    docker compose down --volumes
    ;;
  *)
    echo "Usage: ./start.sh [command] [service]"
    echo ""
    echo "Commands:"
    echo "  redeploy         - Full rebuild and redeploy (default)"
    echo "  logs [svc]       - Show logs for service"  
    echo "  test-integration - Run integration test"
    echo "  status/ps        - Show container status"
    echo "  down             - Stop all containers"
    exit 1
    ;;
esac