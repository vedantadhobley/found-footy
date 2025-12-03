"""
Temporal Worker - Executes workflows and activities
"""
import asyncio
import os
import sys

# Force unbuffered output
sys.stdout = os.fdopen(sys.stdout.fileno(), 'w', buffering=1)
sys.stderr = os.fdopen(sys.stderr.fileno(), 'w', buffering=1)

from temporalio.client import Client
from temporalio.worker import Worker

from src.workflows import (
    IngestWorkflow,
    MonitorWorkflow,
    EventWorkflow,
    TwitterWorkflow,
    DownloadWorkflow,
)
from src.activities import ingest, monitor, event, twitter, download


async def main():
    # Connect to Temporal server (use env var for Docker, fallback to localhost)
    temporal_host = os.getenv("TEMPORAL_HOST", "localhost:7233")
    print(f"🔌 Connecting to Temporal at {temporal_host}...", flush=True)
    
    try:
        client = await Client.connect(temporal_host)
        print(f"✅ Connected to Temporal server", flush=True)
        
        # Create worker that listens on task queue
        worker = Worker(
            client,
            task_queue="found-footy",
            workflows=[
                IngestWorkflow,
                MonitorWorkflow,
                EventWorkflow,
                TwitterWorkflow,
                DownloadWorkflow,
            ],
            activities=[
                # Ingest activities
                ingest.fetch_todays_fixtures,
                ingest.categorize_and_store_fixtures,
                # Monitor activities
                monitor.activate_fixtures,
                monitor.fetch_active_fixtures,
                monitor.store_and_compare,
                monitor.complete_fixture_if_ready,
                # Event (debounce) activities
                event.debounce_fixture_events,
                # Twitter activities
                twitter.search_event_videos,
                # Download activities
                download.download_and_upload_videos,
            ],
        )
        
        print("🚀 Worker started - listening for workflows on 'found-footy' task queue...", flush=True)
        print("📋 Registered workflows: Ingest, Monitor, Event, Twitter, Download", flush=True)
        await worker.run()
    except Exception as e:
        print(f"❌ Worker failed: {e}", file=sys.stderr, flush=True)
        import traceback
        traceback.print_exc()
        sys.exit(1)


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        print("\n👋 Worker stopped")
