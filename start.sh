#!/bin/bash

# Found Footy - Docker Compose Startup Script
# Usage:
#   ./start.sh           - Start in production mode
#   ./start.sh -v        - Start in production mode (wipe volumes)

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

# Shutdown existing containers
if [[ "$WIPE_VOLUMES" == true ]]; then
  echo "🗑️  Stopping containers and wiping DATA volumes (keeping Twitter auth)..."
  # Backup Twitter cookies before wiping
  if docker volume inspect found-footy_twitter_cookies > /dev/null 2>&1; then
    echo "📦 Backing up Twitter cookies..."
    docker run --rm \
      -v found-footy_twitter_cookies:/source:ro \
      -v "$(pwd)/.twitter_backup:/backup" \
      alpine sh -c "mkdir -p /backup && cp -a /source/. /backup/ 2>/dev/null || true"
  fi
  
  docker compose down -v
  
  # Restore Twitter cookies after volume wipe
  if [ -d "$(pwd)/.twitter_backup" ] && [ "$(ls -A $(pwd)/.twitter_backup)" ]; then
    echo "📦 Restoring Twitter cookies..."
    docker volume create found-footy_twitter_cookies
    docker run --rm \
      -v found-footy_twitter_cookies:/dest \
      -v "$(pwd)/.twitter_backup:/backup:ro" \
      alpine sh -c "cp -a /backup/. /dest/ 2>/dev/null || true"
    rm -rf "$(pwd)/.twitter_backup"
    echo "✅ Twitter cookies restored - no need to re-login!"
  fi
else
  echo "🛑 Stopping containers..."
  docker compose down
fi

# Build and start services
echo "� Starting Found Footy..."
docker compose --profile build-only build
docker compose up -d

echo ""
echo "✅ Services started!"
echo "   Dagster UI:      http://localhost:3000"
echo "   MongoDB Express: http://localhost:8081 (ffuser/ffpass)"
echo "   MinIO Console:   http://localhost:9001 (ffuser/ffpass)"
echo "   Twitter Service: http://localhost:8888/health"
if [[ "$WIPE_VOLUMES" == true ]]; then
  echo "   Mode: FRESH START (volumes wiped)"
else
  echo "   Mode: PRODUCTION (rebuild required for changes)"
fi