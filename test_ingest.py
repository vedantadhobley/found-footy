"""
Quick test script to trigger IngestWorkflow manually
Run inside worker container: docker exec -it found-footy-worker python /workspace/test_ingest.py
"""
import asyncio
import os
from temporalio.client import Client
from src.workflows.ingest_workflow import IngestWorkflow


async def main():
    # Connect to Temporal (use Docker hostname if in container)
    temporal_host = os.getenv("TEMPORAL_HOST", "localhost:7233")
    client = await Client.connect(temporal_host)
    
    print(f"🔌 Connected to Temporal at {temporal_host}")
    print("🎯 Starting IngestWorkflow...")
    
    # Execute workflow
    result = await client.execute_workflow(
        IngestWorkflow.run,
        id=f"ingest-manual-test",
        task_queue="found-footy",
    )
    
    print(f"✅ Workflow completed!")
    print(f"📊 Results: {result}")


if __name__ == "__main__":
    asyncio.run(main())
