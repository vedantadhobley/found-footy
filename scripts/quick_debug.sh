#!/bin/bash
set -euo pipefail

echo "🔍 QUICK TWITTER WORKER DEBUG"
echo "=============================="

# Check if containers are running
echo "📦 Container Status:"
docker compose ps twitter-worker

echo ""
echo "🔧 Environment Variables:"
docker compose exec twitter-worker env | grep -E "(TWITTER|PREFECT|MONGODB)" | sort

echo ""
echo "🌐 Network Connectivity:"
docker compose exec twitter-worker ping -c 2 prefect-server || echo "❌ Can't reach Prefect server"
docker compose exec twitter-worker ping -c 2 mongodb || echo "❌ Can't reach MongoDB"

echo ""
echo "📋 Worker Pool Status:"
docker compose exec app prefect work-pool ls | grep twitter || echo "❌ No twitter pool found"

echo ""
echo "🧪 Comprehensive Debug:"
docker compose exec twitter-worker python /app/scripts/debug_twitter_worker.py

echo ""
echo "📝 Recent Logs (last 50 lines):"
docker compose logs --tail=50 twitter-worker