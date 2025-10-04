#!/usr/bin/env bash
set -euo pipefail

echo "🚀 Found Footy"
echo "🏗️ Architecture: $(uname -m)"
echo "🐧 Platform: $(uname -s)"

cmd="${1:-redeploy}"
svc="${2:-}"

# Common redeploy function
do_redeploy() {
  local mode="$1"
  
  echo "🔄 Redeploying ($mode mode)..."
  
  # Check if .env exists
  if [ ! -f .env ]; then
    echo "⚠️ .env file not found!"
    echo "📝 Please copy .env.template to .env and fill in your credentials:"
    echo "   cp .env.template .env"
    echo "   nano .env  # or vim .env"
    exit 1
  fi
  
  echo "✅ .env file found"
  
  # Handle Tailscale configuration if needed
  if [ "$mode" = "tailscale" ]; then
    # Get Tailscale IP automatically
    if ! command -v tailscale &> /dev/null; then
      echo "❌ Tailscale not installed or not in PATH"
      exit 1
    fi
    
    TAILSCALE_IP=$(tailscale ip -4)
    if [ -z "$TAILSCALE_IP" ]; then
      echo "❌ Could not get Tailscale IP. Is Tailscale running?"
      exit 1
    fi
    
    echo "📍 Tailscale IP detected: $TAILSCALE_IP"
    
    # Update .env with Tailscale IP
    echo "🔧 Configuring for Tailscale access..."
    sed -i '/^EXTERNAL_HOST=/d' .env
    echo "EXTERNAL_HOST=http://$TAILSCALE_IP" >> .env
    
    # Ensure ports bind to all interfaces (one-time fix)
    if ! grep -q "0.0.0.0:5000:4200" docker-compose.yml; then
      echo "🔧 Updating port bindings for external access..."
      sed -i 's|"5000:4200"|"0.0.0.0:5000:4200"|g' docker-compose.yml
      sed -i 's|"3000:8081"|"0.0.0.0:3000:8081"|g' docker-compose.yml
      sed -i 's|"9000:9000"|"0.0.0.0:9000:9000"|g' docker-compose.yml
      sed -i 's|"9001:9001"|"0.0.0.0:9001:9001"|g' docker-compose.yml
      sed -i 's|"8000:8888"|"0.0.0.0:8000:8888"|g' docker-compose.yml
    fi
  else
    # Local mode - remove EXTERNAL_HOST to use localhost fallback
    echo "🏠 Configuring for local development..."
    sed -i '/^EXTERNAL_HOST=/d' .env
  fi
  
  # Deploy
  export DOCKER_BUILDKIT=1 COMPOSE_DOCKER_CLI_BUILD=1
  docker compose down --remove-orphans || true
  docker compose build
  docker compose up -d --force-recreate
  
  echo "📦 Applying Prefect deployments..."
  docker compose run --rm app python found_footy/flows/deployments.py --apply || true
  
  if [ "$mode" = "tailscale" ]; then
    echo ""
    echo "🎯 ============================================"
    echo "🎯 TAILSCALE ACCESS CONFIGURED SUCCESSFULLY"
    echo "🎯 ============================================"
    echo ""
    echo "✅ Access your services via Tailscale:"
    echo "  📊 Prefect UI:      http://$TAILSCALE_IP:5000"
    echo "  🗄️  MongoDB Express: http://$TAILSCALE_IP:3000 (founduser/footypass)"
    echo "  📦 MinIO Console:   http://$TAILSCALE_IP:9001 (founduser/footypass)"
    echo "  🐦 Twitter Service: http://$TAILSCALE_IP:8000/health"
    echo ""
  else
    echo ""
    echo "🎯 ======================================"
    echo "🎯 LOCAL DEVELOPMENT MODE ACTIVE"
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
  
  # Ensure services are running
  if ! docker compose ps | grep -q "Up"; then
    echo "🔄 Starting services first..."
    do_redeploy "local"
    sleep 30
  fi
  
  # Ensure test container is running
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
    echo "Usage: ./start.sh [command] [service]"
    echo ""
    echo "Commands:"
    echo "  redeploy              - Full rebuild for LOCAL development (default)"
    echo "  tailscale             - Full rebuild with TAILSCALE configuration"
    echo "  test-integration-real - Run integration test"
    echo "  logs [svc]            - Show logs for service"  
    echo "  status/ps             - Show container status"
    echo "  down                  - Stop all containers"
    echo ""
    echo "Examples:"
    echo "  ./start.sh                        # Local development"
    echo "  ./start.sh tailscale              # Remote access via Tailscale"
    echo "  ./start.sh test-integration-real  # Run integration test"
    exit 1
    ;;
esac