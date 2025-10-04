#!/usr/bin/env bash
set -euo pipefail

echo "🚀 Found Footy"
echo "🏗️ Architecture: $(uname -m)"
echo "🐧 Platform: $(uname -s)"

cmd="${1:-redeploy}"
svc="${2:-}"

do_redeploy() {
  local mode="$1"
  
  echo "🔄 Redeploying ($mode mode)..."
  
  if [ ! -f .env ]; then
    echo "⚠️ .env file not found!"
    exit 1
  fi
  
  echo "✅ .env file found"
  
  # Clean any existing EXTERNAL_HOST entries
  sed -i '/^EXTERNAL_HOST=/d' .env
  
  # ✅ FIX: Set EXTERNAL_HOST BEFORE starting containers
  if [ "$mode" = "tailscale" ]; then
    TAILSCALE_IP=$(tailscale ip -4)
    echo "EXTERNAL_HOST=http://$TAILSCALE_IP" >> .env
    echo "📍 EXTERNAL_HOST set to: http://$TAILSCALE_IP"
  fi
  
  # Deploy services AFTER setting environment
  export DOCKER_BUILDKIT=1 COMPOSE_DOCKER_CLI_BUILD=1
  docker compose down --remove-orphans || true
  docker compose build
  docker compose up -d --force-recreate
  
  echo "📦 Applying Prefect deployments..."
  docker compose run --rm app python found_footy/flows/deployments.py --apply || true
  
  if [ "$mode" = "tailscale" ]; then
    echo ""
    echo "🎯 ============================================"
    echo "🎯 TAILSCALE ACCESS VIA NGINX PROXY"
    echo "🎯 ============================================"
    echo ""
    echo "✅ Access your services via Tailscale:"
    echo "  📊 Prefect UI:      http://$TAILSCALE_IP:5000"
    echo "  🗄️  MongoDB Express: http://$TAILSCALE_IP:3000 (founduser/footypass)"
    echo "  📦 MinIO Console:   http://$TAILSCALE_IP:9001 (founduser/footypass)"
    echo ""
    echo "🔧 All requests routed through Nginx reverse proxy"
    echo ""
  else
    echo ""
    echo "🎯 ======================================"
    echo "🎯 LOCAL DEVELOPMENT VIA NGINX PROXY"
    echo "🎯 ======================================"
    echo ""
    echo "✅ Access your services locally:"
    echo "  📊 Prefect UI:      http://localhost:5000"
    echo "  🗄️  MongoDB Express: http://localhost:3000 (founduser/footypass)"
    echo "  📦 MinIO Console:   http://localhost:9001 (founduser/footypass)"
    echo "  🐦 Twitter Service: http://localhost:8000/health"
    echo ""
  fi
  
  echo "✅ Deploy complete"
  docker compose ps
}

test_integration() {
  echo "🧪 Running Integration Test..."
  
  if ! docker compose ps | grep -q "Up"; then
    echo "🔄 Starting services first..."
    do_redeploy "local"
    sleep 30
  fi
  
  if ! docker compose ps test | grep -q "Up"; then
    echo "🔄 Starting test container..."
    docker compose up -d test
    sleep 10
  fi
  
  echo "🚀 Executing integration test..."
  docker compose exec test python /app/scripts/test_integration_real.py
}

# ✅ ADD: Missing main execution logic
case "$cmd" in
  redeploy|"")
    do_redeploy "local"
    ;;
  tailscale)
    do_redeploy "tailscale"
    ;;
  test-integration-real)
    test_integration
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
    echo "Usage: ./start.sh [command]"
    echo ""
    echo "Commands:"
    echo "  redeploy              - Local development (default)"
    echo "  tailscale             - Tailscale access via Nginx proxy"
    echo "  test-integration-real - Run integration test"
    echo "  logs [svc]            - Show logs"
    echo "  status/ps             - Show status"
    echo "  down                  - Stop everything"
    echo ""
    exit 1
    ;;
esac