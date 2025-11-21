# Found Footy - Standardized Dev Setup

**Now matches vedanta-systems pattern!** ✅

## Quick Start

### Development Mode (Daily Work):
```bash
./start-dev.sh                    # Start dev environment
# VS Code → Ctrl+Shift+P → "Reopen in Container"
# Edit code → changes reflect immediately
```

### Production Mode (Testing/Deploy):
```bash
./start.sh                        # Start production
./start.sh -v                     # Fresh start (wipe volumes)
```

## File Structure

```
found-footy/
├── docker-compose.yml           # Production config
├── docker-compose.dev.yml       # Development config  
├── Dockerfile                   # Production image
├── Dockerfile.dev               # Development image
├── .devcontainer/
│   └── devcontainer.json        # VS Code config
├── start.sh                     # Production startup
└── start-dev.sh                 # Dev startup
```

## How It Works

### Development Mode:
- **Container keeps running** (restart: unless-stopped)
- **Code volume-mounted** at `/app` (live updates)
- **Dagster webserver runs automatically**
- **VS Code attaches** to running container
- **No errors in IDE** (uses container's Python)

### Why No Python Errors?
VS Code uses the container's Python for everything:
- IntelliSense
- Error checking
- Linting
- Running code

Your local machine needs **ZERO Python installed**!

## Networks

- `luv-dev` - Development (shared across projects)
- `luv-prod` - Production (isolated)

Both are **external networks** (persist across restarts).

## Container Persistence

**Containers stay running!** Even when you:
- Close VS Code
- Disconnect from SSH
- Log out

To stop:
```bash
docker compose -f docker-compose.dev.yml down    # Dev
docker compose down                               # Production
```

## What Updates Live?

✅ **Instant (no restart):**
- Function logic
- Bug fixes
- Algorithm changes

⚠️ **Quick reload (click button in Dagster UI):**
- New `@asset` definitions
- Job/schedule changes

🔨 **Full rebuild:**
- New packages in requirements.txt
- Run: `./start-dev.sh -v`

## Services

| Service | Port | Dev | Prod |
|---------|------|-----|------|
| Dagster UI | 3001 | ✅ | 3002 |
| MongoDB Express | 8081 | ✅ | ✅ |
| MinIO Console | 9001 | ✅ | ✅ |
| MongoDB | 27017 | ✅ | ✅ |

## Matching vedanta-systems Pattern

✅ Two compose files (dev/prod)  
✅ Two Dockerfiles (dev/prod)  
✅ External networks (luv-dev/luv-prod)  
✅ Container persistence (restart: unless-stopped)  
✅ Volume mounting for live code  
✅ Simple startup scripts  
✅ shutdownAction: none (containers stay up)  

## Common Commands

```bash
# Start dev
./start-dev.sh

# Start dev (fresh)
./start-dev.sh -v

# Start production
./start.sh

# Start production (fresh)
./start.sh -v

# Check running containers
docker ps

# View logs
docker compose -f docker-compose.dev.yml logs -f dagster

# Stop dev
docker compose -f docker-compose.dev.yml down

# Stop production
docker compose down
```

## Extended Testing

Container persistence means you can:
1. Start dev environment
2. Attach with VS Code
3. Run long tests
4. Close VS Code
5. Come back later - **container still running**!
6. Reattach and continue

Perfect for your workflow! 🎉
