do_redeploy() {
  local mode="$1"
  
  echo "🔄 Redeploying ($mode mode)..."
  
  if [ ! -f .env ]; then
    echo "⚠️ .env file not found!"
    exit 1
  fi
  
  echo "✅ .env file found"
  
  # ❌ REMOVE: No more EXTERNAL_HOST management - Nginx handles everything
  # Clean any existing EXTERNAL_HOST entries
  sed -i '/^EXTERNAL_HOST=/d' .env
  
  # Deploy services
  export DOCKER_BUILDKIT=1 COMPOSE_DOCKER_CLI_BUILD=1
  docker compose down --remove-orphans || true
  docker compose build
  docker compose up -d --force-recreate
  
  echo "📦 Applying Prefect deployments..."
  docker compose run --rm app python found_footy/flows/deployments.py --apply || true
  
  if [ "$mode" = "tailscale" ]; then
    # Get Tailscale IP for user display only
    TAILSCALE_IP=$(tailscale ip -4)
    
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