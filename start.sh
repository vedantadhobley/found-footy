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

debug_twitter() {
  echo "🔍 Debugging Twitter Worker..."
  
  # Quick debug first
  echo "📊 Quick Status Check:"
  ./scripts/quick_debug.sh
  
  echo ""
  echo "🔍 Comprehensive Debug:"
  docker compose exec twitter-worker python /app/scripts/debug_twitter_worker.py
}

debug_logs() {
  local service="${1:-twitter-worker}"
  echo "📝 Showing logs for ${service}..."
  docker compose logs -f --tail=100 "${service}"
}

test_twitter() {
  echo "🧪 Testing Twitter Worker End-to-End..."
  docker compose exec twitter-worker python /app/scripts/test_twitter_content.py
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

test_twitter_debug() {
  echo "🔍 Running Twitter Debug Test..."
  
  if ! docker-compose ps test | grep -q "Up"; then
    echo "🔄 Starting test container..."
    docker-compose up -d test
    sleep 5
  fi
  
  docker-compose exec test python /app/scripts/debug_twitter_worker.py
}

test_shell() {
  echo "🐚 Opening test shell..."
  
  if ! docker-compose ps test | grep -q "Up"; then
    echo "🔄 Starting test container..."
    docker-compose up -d test
    sleep 5
  fi
  
  docker-compose exec test bash
}

case "$cmd" in
  redeploy|"")
    redeploy
    ;;
  # ... existing cases ...
  test-integration)
    test_integration
    ;;
  test-twitter-debug) 
    test_twitter_debug
    ;;
  test-shell)
    test_shell
    ;;
  logs)
    if [ -n "${svc}" ]; then
      docker compose logs -f "${svc}"
    else
      docker compose logs -f
    fi
    ;;
  debug-twitter)
    debug_twitter
    ;;
  debug-logs)
    debug_logs "${svc}"
    ;;
  test-twitter)
    test_twitter
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
    # Update the help text at the end of start.sh:
    echo "Commands:"
    echo "  redeploy         - Full rebuild and redeploy (default)"
    echo "  logs [svc]       - Show logs for service"  
    echo "  debug-twitter    - Debug Twitter worker issues"
    echo "  debug-logs       - Show debug logs for twitter-worker"
    echo "  test-twitter     - Test Twitter functionality end-to-end"
    echo "  test-integration - Run complete pipeline integration test"  # ✅ NEW
    echo "  test-shell       - Open interactive shell in test container"  # ✅ NEW
    echo "  status/ps        - Show container status"
    echo "  down             - Stop all containers"
    exit 1
    ;;
esac