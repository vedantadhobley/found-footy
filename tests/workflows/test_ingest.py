"""
Test: Ingest Workflow

Tests the daily ingestion of fixtures from API-Football.
Run: docker exec -it found-footy-worker python /workspace/tests/workflows/test_ingest.py
"""
import asyncio
import os
from temporalio.client import Client
from src.workflows.ingest_workflow import IngestWorkflow


async def main():
    temporal_host = os.getenv("TEMPORAL_HOST", "localhost:7233")
    client = await Client.connect(temporal_host)
    
    print("=" * 60)
    print("TEST: Ingest Workflow")
    print("=" * 60)
    print(f"🔌 Connected to Temporal at {temporal_host}")
    print("🎯 Starting IngestWorkflow...")
    print()
    
    # Execute workflow
    result = await client.execute_workflow(
        IngestWorkflow.run,
        id=f"test-ingest",
        task_queue="found-footy",
    )
    
    print()
    print("=" * 60)
    print("✅ WORKFLOW COMPLETED")
    print("=" * 60)
    print(f"📊 Results: {result}")
    print()
    print("Next steps:")
    print("  1. Check Temporal UI: http://localhost:4100")
    print("  2. Check MongoDB: docker exec found-footy-mongo mongosh ...")
    print("  3. Verify fixture counts match today's games")
    print()


if __name__ == "__main__":
    asyncio.run(main())
