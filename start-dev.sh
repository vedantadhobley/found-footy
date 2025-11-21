#!/bin/bash
# Simple development startup - matches vedanta-systems pattern
# Usage: ./start-dev.sh [-v to wipe volumes]

if [[ "$1" == "-v" ]]; then
  echo "🗑️  Stopping and wiping volumes..."
  docker compose -f docker-compose.dev.yml down -v
else
  echo "🛑 Stopping containers..."
  docker compose -f docker-compose.dev.yml down
fi

echo "🚀 Starting Found Footy (Development)..."
docker compose -f docker-compose.dev.yml up -d

echo ""
echo "✅ Development environment started!"
echo "   Dagster UI:      http://localhost:3001"
echo "   MongoDB Express: http://localhost:8081"
echo "   MinIO Console:   http://localhost:9001"
echo ""
echo "   💡 VS Code: Press Ctrl+Shift+P → 'Reopen in Container'"
