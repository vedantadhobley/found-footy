#!/bin/bash
# filepath: /home/vedanta/found-footy/start.sh
echo "🚀 Starting Found Footy - Fully Automated"
echo "=================================="

docker compose down

# Build and start everything
docker compose up --build -d

echo ""
echo "✅ Found Footy started!"
echo ""
echo "📊 Services:"
echo "  - Prefect UI: http://localhost:4200"
echo "  - MongoDB Admin: http://localhost:8083 (admin/admin123)"
echo ""
echo "📋 Check deployment status:"
echo "  docker-compose logs found-footy-init"
echo ""
echo "🔍 Monitor workers:"
echo "  docker-compose logs -f fixtures-worker-1"
echo ""
echo "🧪 Test events:"
echo "  docker-compose exec fixtures-worker-1 python debug_events.py"