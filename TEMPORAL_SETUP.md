# Temporal Migration Setup

## Quick Start

```bash
# 1. Create fresh venv
python3 -m venv .venv
source .venv/bin/activate

# 2. Install dependencies
pip install -r requirements.txt

# 3. Start services (Temporal + MongoDB + MinIO + Twitter)
docker compose -f docker-compose.dev.yml up -d

# 4. Check services
docker ps  # Should see: temporal, worker, mongo, minio, twitter

# 5. Access UIs
- Temporal UI: http://localhost:4105
- MongoDB Express: http://localhost:4101
- MinIO Console: http://localhost:4102
- Twitter noVNC: http://localhost:4104

# 6. Run worker (if not in docker)
python src/worker.py
```

## Development Workflow

1. **Write activity logic**: Implement TODOs in `src/activities/*.py`
2. **Test locally**: Worker runs in Docker, but you can run locally too
3. **Trigger workflows**: Use Temporal CLI or Python scripts
4. **View executions**: Check Temporal UI at localhost:4105

## Port Map (Dev)

- 4100: Dagster UI (deprecated, will remove)
- 4101: MongoDB Express
- 4102: MinIO Console
- 4103: Twitter API
- 4104: Twitter noVNC Browser
- **4105: Temporal UI** (new!)
- 7233: Temporal gRPC (internal)

## Architecture Benefits

### Dagster Issues
- Polling loops for sensors (clutters UI)
- Complex op invocation (context, execute_in_process)
- Nested jobs don't show in UI properly
- Heavy dependencies

### Temporal Advantages
- Event-driven: workflows trigger child workflows immediately
- Each execution shows in UI as separate run
- Durable: auto-persists state, retries, timeouts
- Cleaner code: no decorators hell, just async functions
- Better for our use case: Monitor → Event → Twitter → Download chain

## Next Migration Steps

1. Copy logic from `src-dagster/jobs/*/ops/` to `src/activities/`
2. Copy API client from `src-dagster/api/`
3. Test with sample fixture
4. Delete Dagster services from docker-compose
5. Archive `src-dagster/`
