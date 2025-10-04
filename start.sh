#!/usr/bin/env bash
set -euo pipefail

echo "🚀 Found Footy"
echo "🏗️ Architecture: $(uname -m)"
echo "🐧 Platform: $(uname -s)"

cmd="${1:-redeploy}"
svc="${2:-}"

do_redeploy() {
  echo "🔄 Redeploying..."
  
  if [ ! -f .env ]; then
    echo "⚠️ .env file not found!"
    exit 1
  fi
  
  echo "✅ .env file found"
  
  # ✅ SIMPLE: Always use localhost
  sed -i '/^EXTERNAL_HOST=/d' .env
  sed -i '/^MINIO_BROWSER_REDIRECT_URL=/d' .env
  sed -i '/^MINIO_SERVER_URL=/d' .env
  sed -i '/^ME_CONFIG_SITE_BASEURL=/d' .env
  
  echo "EXTERNAL_HOST=http://localhost" >> .env
  echo "📍 EXTERNAL_HOST set to: http://localhost"
  
  # ✅ SIMPLE: Deploy without complexity
  export DOCKER_BUILDKIT=1 COMPOSE_DOCKER_CLI_BUILD=1
  docker compose down --remove-orphans || true
  docker compose build
  docker compose up -d --force-recreate
  
  echo "📦 Applying Prefect deployments..."
  docker compose run --rm app python found_footy/flows/deployments.py --apply || true
  
  echo ""
  echo "🎯 ============================================"
  echo "🎯 LOCAL ACCESS - ALL SERVICES"
  echo "🎯 ============================================"
  echo ""
  echo "✅ Access your services locally:"
  echo "  📊 Prefect UI:       http://localhost:5000"
  echo "  🗄️  MongoDB Express:  http://localhost:3000 (founduser/footypass)"
  echo "  📦 MinIO Console:    http://localhost:9001 (founduser/footypass)"
  echo "  📁 MinIO S3 API:     http://localhost:9000"
  echo "  🐦 Twitter Service:  http://localhost:8000/health"
  echo ""
  echo "📱 For remote access, consider:"
  echo "  • SSH port forwarding"
  echo "  • VPN setup"
  echo "  • Cloud deployment"
  echo ""
  
  echo "✅ Deploy complete"
  docker compose ps
}

test_integration() {
  echo "🧪 Running Integration Test..."
  
  if ! docker compose ps | grep -q "Up"; then
    echo "🔄 Starting services first..."
    do_redeploy
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

case "$cmd" in
  redeploy|"")
    do_redeploy
    ;;
  # ❌ REMOVE: tailscale option completely
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
    echo "  test-integration-real - Run integration test"
    echo "  logs [svc]            - Show logs"
    echo "  status/ps             - Show status"
    echo "  down                  - Stop everything"
    echo ""
    exit 1
    ;;
esac