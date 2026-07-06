#!/usr/bin/env python3
"""
Test Ingest Workflow - Trigger IngestWorkflow for a specific date

This script allows testing the full ingestion pipeline for any date,
which is useful for:
- Testing against completed fixtures (have events to process)
- Validating RAG alias pre-caching
- Testing fixture categorization (staging/active/completed)

Usage:
    # Run for today (default)
    docker exec found-footy-worker python /workspace/tests/workflows/test_ingest.py
    
    # Run for a specific date
    docker exec found-footy-worker python /workspace/tests/workflows/test_ingest.py --date 2025-12-26
    
    # Run with cleanup (clear fixtures_staging/active/completed first)
    docker exec found-footy-worker python /workspace/tests/workflows/test_ingest.py --date 2025-12-26 --clean

Watch progress:
    - Temporal UI: http://localhost:4100
    - Worker logs: docker logs -f found-footy-worker
    - MongoDB:     http://localhost:4101
"""
import argparse
import asyncio
import uuid
from datetime import date

from temporalio.client import Client

from src.workflows import IngestWorkflow, IngestWorkflowInput


async def run_ingest_workflow(target_date: str | None = None, clean: bool = False):
    """Trigger IngestWorkflow and wait for completion"""
    
    # Connect to Temporal
    print("🔌 Connecting to Temporal...", flush=True)
    client = await Client.connect("temporal:7233")
    print("✅ Connected", flush=True)
    
    # Clean existing fixture data if requested
    if clean:
        print("🧹 Cleaning existing fixture data...", flush=True)
        from src.data.mongo_store import FootyMongoStore
        store = FootyMongoStore()
        for coll in ["fixtures_staging", "fixtures_active", "fixtures_completed"]:
            result = store.db[coll].delete_many({})
            print(f"   Deleted {result.deleted_count} from {coll}", flush=True)
        print("✅ Cleanup complete", flush=True)
        print()
    
    # Display what we're doing
    if target_date:
        print(f"📅 Running IngestWorkflow for: {target_date}", flush=True)
    else:
        print(f"📅 Running IngestWorkflow for: today ({date.today().isoformat()})", flush=True)
    
    # Create input (None for today, or specific date)
    workflow_input = IngestWorkflowInput(target_date=target_date) if target_date else None
    
    # Generate unique workflow ID
    workflow_id = f"test-ingest-{target_date or 'today'}-{uuid.uuid4().hex[:8]}"
    
    print(f"🚀 Starting workflow: {workflow_id}", flush=True)
    print()
    print("=" * 60)
    
    # Execute workflow and wait for result
    result = await client.execute_workflow(
        IngestWorkflow.run,
        workflow_input,
        id=workflow_id,
        task_queue="found-footy",
    )
    
    print("=" * 60)
    print()
    print("✅ INGEST WORKFLOW COMPLETE")
    print()
    print(f"📊 Results:")
    print(f"   Total fixtures:    {result.get('total_fixtures', 0)}")
    print(f"   Unique teams:      {result.get('unique_teams', 0)}")
    print(f"   RAG cached:        {result.get('rag_success', 0)}/{result.get('unique_teams', 0)}")
    print(f"   RAG failed:        {result.get('rag_failed', 0)}")
    print()
    print(f"   → fixtures_staging:   {result.get('staging', 0)}")
    print(f"   → fixtures_active:    {result.get('active', 0)}")
    print(f"   → fixtures_completed: {result.get('completed', 0)}")
    print()
    print("🔍 Next steps:")
    print("   - Check Temporal UI: http://localhost:4100")
    print("   - Check MongoDB:     http://localhost:4101")
    print("   - MonitorWorkflow will process fixtures_staging every minute")
    print()
    
    return result


def main():
    parser = argparse.ArgumentParser(
        description="Run IngestWorkflow for a specific date",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__
    )
    
    parser.add_argument(
        "--date", "-d",
        type=str,
        default=None,
        help="Target date in ISO format (YYYY-MM-DD). Default: today"
    )
    
    parser.add_argument(
        "--clean", "-c",
        action="store_true",
        help="Clean existing fixtures from MongoDB before ingesting"
    )
    
    args = parser.parse_args()
    
    # Validate date format if provided
    if args.date:
        try:
            date.fromisoformat(args.date)
        except ValueError:
            print(f"❌ Invalid date format: {args.date}")
            print("   Use ISO format: YYYY-MM-DD (e.g., 2025-12-26)")
            exit(1)
    
    # Run the workflow
    asyncio.run(run_ingest_workflow(args.date, args.clean))


if __name__ == "__main__":
    main()
