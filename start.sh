#!/usr/bin/env bash
set -euo pipefail

echo "🚀 Found Footy"
echo "🏗️ Architecture: $(uname -m)"
echo "🐧 Platform: $(uname -s)"

cmd="${1:-redeploy}"
svc="${2:-}"

redeploy() {
  if [ ! -f .env ]; then
    echo "⚠️ .env file not found!"
    echo "📝 Please copy .env.template to .env and fill in your credentials:"
    echo "   cp .env.template .env"
    echo "   nano .env"
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

debug_twitter_session() {
  echo "🔍 Debugging Twitter Session Service..."
  docker compose logs -f --tail=100 twitter-session
  echo ""
  echo "🔍 Health Check:"
  docker compose exec twitter-session curl -sf http://localhost:8888/health || echo "❌ Twitter session not healthy"
}

debug_logs() {
  local service="${1:-app}"
  echo "📝 Showing logs for ${service}..."
  docker compose logs -f --tail=100 "${service}"
}

test_pipeline() {
  echo "🧪 Running full pipeline test (.fn() approach)..."
  docker compose run --rm -e PREFECT_API_URL="http://prefect-server:4200/api" test python scripts/test_real_goal.py
}

test_real_goal() {
  echo "🧪 Running test_real_goal.py script (Prefect deployment call)..."
  docker compose run --rm -e PREFECT_API_URL="http://prefect-server:4200/api" test python scripts/test_real_goal.py
}

test_twitter_flow() {
  echo "🧪 Testing Twitter flow (.fn() approach)..."
  docker compose run --rm test python tests/test_twitter_real_world.py
}

test_generic() {
  local script="${1:-}"
  if [ -z "$script" ]; then
    echo "❌ Please provide a script to run (e.g. test_real_goal.py)"
    exit 1
  fi
  echo "🧪 Running test: $script"
  docker compose run --rm test python "scripts/$script"
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
  debug-twitter-session)
    debug_twitter_session
    ;;
  debug-logs)
    debug_logs "${svc}"
    ;;
  test-pipeline)
    test_pipeline
    ;;
  test-real-goal)
    test_real_goal
    ;;
  test-twitter-flow)
    test_twitter_flow
    ;;
  test)
    test_generic "${svc}"
    ;;
  status|ps)
    docker compose ps
    ;;
  down)
    docker compose down --volumes
    ;;
  *)
    echo "Usage: ./start.sh [command] [service|script]"
    echo ""
    echo "Commands:"
    echo "  redeploy                - Full rebuild and redeploy (default)"
    echo "  logs [svc]              - Show logs for service"
    echo "  debug-twitter-session   - Debug Twitter session service and health"
    echo "  debug-logs [svc]        - Show debug logs for any service"
    echo "  test-pipeline           - Run full pipeline test (.fn() approach)"
    echo "  test-real-goal          - Run scripts/test_real_goal.py (.fn() approach)"
    echo "  test-twitter-flow       - Run Twitter flow test (.fn() approach)"
    echo "  test [script]           - Run any test script in scripts/"
    echo "  status/ps               - Show container status"
    echo "  down                    - Stop all containers"
    echo ""
    echo "Examples:"
    echo "  ./start.sh test-pipeline"
    echo "  ./start.sh test-real-goal"
    echo "  ./start.sh test test_real_goal.py"
    echo "  ./start.sh logs app"
    echo "  ./start.sh debug-twitter-session"
    exit 1
    ;;
esac