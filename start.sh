#!/bin/bash

# Found Footy - Docker Compose Startup Script
# Usage:
#   ./start.sh           - Start services
#   ./start.sh -v        - Start with fresh volumes (wipe data)
#   ./start.sh logs      - Show logs
#   ./start.sh down      - Stop everything

# Parse arguments
WIPE_VOLUMES=false

for arg in "$@"; do
  case $arg in
    -v|--volumes)
      WIPE_VOLUMES=true
      shift
      ;;
    *)
      ;;
  esac
done

do_redeploy() {
  echo "🚀 Found Footy"
  echo "🏗️ Architecture: $(uname -m)"
  echo "🐧 Platform: $(uname -s)"
  
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
  
  # Shutdown existing containers
  if [[ "$WIPE_VOLUMES" == true ]]; then
    echo "🗑️  Stopping containers and wiping volumes..."
    docker compose down -v
  else
    echo "🛑 Stopping containers..."
    docker compose down --remove-orphans
  fi
  
  # Remove old image to avoid conflicts
  docker rmi -f found-footy:latest || true
  
  # Build and start services
  export DOCKER_BUILDKIT=1 COMPOSE_DOCKER_CLI_BUILD=1
  
  echo "🔨 Building base image..."
  docker compose build app-base
  
  echo "🚀 Starting services..."
  docker compose up -d
  
  echo ""
  echo "✅ Services started!"
  echo "   Dagster UI:      http://localhost:3000"
  echo "   MongoDB Express: http://localhost:8081 (ffuser/ffpass)"
  echo "   MongoDB Direct:  mongodb://localhost:27017 (ffuser/ffpass)"
  echo "   MinIO Console:   http://localhost:9001 (ffuser/ffpass)"
  echo "   MinIO S3 API:    http://localhost:9000"
  echo "   Twitter Service: http://localhost:8888/health"
  if [[ "$WIPE_VOLUMES" == true ]]; then
    echo "   Mode: FRESH START (volumes wiped)"
  else
    echo "   Mode: NORMAL (data preserved)"
  fi
  echo ""
  
  docker compose ps
}

# Handle commands
cmd="${1:-start}"

case "$cmd" in
  start|"")
    do_redeploy
    ;;
  logs)
    svc="${2:-}"
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
    echo "🛑 Stopping all services..."
    docker compose down
    ;;
  -v|--volumes)
    WIPE_VOLUMES=true
    do_redeploy
    ;;
  *)
    echo "Usage: ./start.sh [command] [options]"
    echo ""
    echo "Commands:"
    echo "  start          - Start services (default)"
    echo "  start -v       - Start with fresh volumes (wipe data)"
    echo "  logs [service] - Show logs"
    echo "  status/ps      - Show status"
    echo "  down           - Stop everything"
    echo ""
    echo "Options:"
    echo "  -v, --volumes  - Wipe volumes on start"
    echo ""
    exit 1
    ;;
esac