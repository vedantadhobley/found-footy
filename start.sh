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
  
  # Clean any previous EXTERNAL_HOST entries (not needed for network routing)
  sed -i '/^EXTERNAL_HOST=/d' .env
  
  # Deploy services
  export DOCKER_BUILDKIT=1 COMPOSE_DOCKER_CLI_BUILD=1
  docker compose down --remove-orphans || true
  docker compose build
  docker compose up -d --force-recreate
  
  echo "📦 Applying Prefect deployments..."
  docker compose run --rm app python found_footy/flows/deployments.py --apply || true
  
  if [ "$mode" = "tailscale" ]; then
    # Set up Tailscale network routing after containers are up
    echo "🔧 Setting up Tailscale network routing..."
    
    if ! command -v tailscale &> /dev/null; then
      echo "❌ Tailscale not installed or not in PATH"
      exit 1
    fi
    
    # Wait for network to be fully ready
    sleep 10
    
    # Get Docker network subnet
    DOCKER_SUBNET=$(docker network inspect found-footy-network --format='{{range .IPAM.Config}}{{.Subnet}}{{end}}')
    if [ -z "$DOCKER_SUBNET" ]; then
      echo "❌ Could not get Docker network subnet"
      exit 1
    fi
    
    echo "📍 Docker subnet detected: $DOCKER_SUBNET"
    
    # Enable subnet routing in Tailscale
    echo "🚀 Advertising Docker subnet to Tailscale..."
    sudo tailscale up --advertise-routes=$DOCKER_SUBNET --accept-routes
    
    # Verify Tailscale routing is working
    TAILSCALE_IP=$(tailscale ip -4)
    if [ -z "$TAILSCALE_IP" ]; then
      echo "❌ Could not get Tailscale IP"
      exit 1
    fi
    
    # Get container IPs for user info
    echo "🔍 Getting container IP addresses..."
    sleep 5  # Let containers fully start
    
    PREFECT_IP=$(docker inspect prefect-server --format='{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' 2>/dev/null || echo "pending")
    MONGO_EXPRESS_IP=$(docker inspect mongo-express --format='{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' 2>/dev/null || echo "pending")
    MINIO_IP=$(docker inspect minio --format='{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' 2>/dev/null || echo "pending")
    TWITTER_IP=$(docker inspect twitter-session --format='{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' 2>/dev/null || echo "pending")
    
    echo ""
    echo "🎯 ============================================"
    echo "🎯 TAILSCALE NETWORK ROUTING CONFIGURED"
    echo "🎯 ============================================"
    echo ""
    echo "✅ Docker network exposed to Tailscale:"
    echo "  🌐 Network: $DOCKER_SUBNET"
    echo "  📍 Tailscale IP: $TAILSCALE_IP"
    echo ""
    echo "✅ Access services via Tailscale (host ports):"
    echo "  📊 Prefect UI:      http://$TAILSCALE_IP:5000"
    echo "  🗄️  MongoDB Express: http://$TAILSCALE_IP:3000 (founduser/footypass)"
    echo "  📦 MinIO Console:   http://$TAILSCALE_IP:9001 (founduser/footypass)"
    echo "  🐦 Twitter Service: http://$TAILSCALE_IP:8000/health"
    echo ""
    echo "✅ Direct container access (if needed):"
    echo "  📊 Prefect UI:      http://$PREFECT_IP:4200 (internal)"
    echo "  🗄️  MongoDB Express: http://$MONGO_EXPRESS_IP:8081 (internal)"
    echo "  📦 MinIO Console:   http://$MINIO_IP:9001 (internal)"
    echo "  🐦 Twitter Service: http://$TWITTER_IP:8888 (internal)"
    echo ""
    echo "🎯 Network routing allows full Docker network access!"
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
    echo "  tailscale             - Full rebuild with TAILSCALE network routing"
    echo "  test-integration-real - Run integration test"
    echo "  logs [svc]            - Show logs for service"  
    echo "  status/ps             - Show container status"
    echo "  down                  - Stop all containers"
    echo ""
    echo "Examples:"
    echo "  ./start.sh                        # Local development"
    echo "  ./start.sh tailscale              # Network routing via Tailscale"
    echo "  ./start.sh test-integration-real  # Run integration test"
    exit 1
    ;;
esac