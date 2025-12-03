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


async def setup_schedules(client: Client):
    """Set up workflow schedules (idempotent - safe to call on every startup)"""
    from datetime import timedelta
    from temporalio.client import (
        Schedule,
        ScheduleActionStartWorkflow,
        ScheduleIntervalSpec,
        ScheduleSpec,
        ScheduleState,
    )
    
    print("📅 Setting up workflow schedules...", flush=True)
    
    # Schedule 1: IngestWorkflow - Daily at 00:05 UTC (PAUSED by default)
    ingest_schedule_id = "ingest-daily"
    try:
        ingest_handle = client.get_schedule_handle(ingest_schedule_id)
        await ingest_handle.describe()
        print(f"   ✓ Schedule '{ingest_schedule_id}' exists", flush=True)
    except Exception:
        await client.create_schedule(
            ingest_schedule_id,
            Schedule(
                action=ScheduleActionStartWorkflow(
                    IngestWorkflow.run,
                    id=f"ingest-{ingest_schedule_id}",  # Simple static ID - only runs once per day
                    task_queue="found-footy",
                ),
                spec=ScheduleSpec(cron_expressions=["5 0 * * *"]),  # 00:05 UTC daily
                state=ScheduleState(
                    paused=True,
                    note="Paused: Still in development",
                ),
            ),
        )
        print(f"   ✓ Created '{ingest_schedule_id}' (PAUSED, enable in UI when ready)", flush=True)
    
    # Schedule 2: MonitorWorkflow - Every minute (ENABLED by default)
    monitor_schedule_id = "monitor-every-minute"
    try:
        monitor_handle = client.get_schedule_handle(monitor_schedule_id)
        await monitor_handle.describe()
        print(f"   ✓ Schedule '{monitor_schedule_id}' exists", flush=True)
    except Exception:
        await client.create_schedule(
            monitor_schedule_id,
            Schedule(
                action=ScheduleActionStartWorkflow(
                    MonitorWorkflow.run,
                    id=f"monitor-{monitor_schedule_id}",  # Simple static ID - each run completes quickly
                    task_queue="found-footy",
                ),
                spec=ScheduleSpec(intervals=[ScheduleIntervalSpec(every=timedelta(minutes=1))]),
                state=ScheduleState(
                    paused=False,
                    note="Running every minute",
                ),
            ),
        )
        print(f"   ✓ Created '{monitor_schedule_id}' (ENABLED)", flush=True)


async def main():
    # Connect to Temporal server (use env var for Docker, fallback to localhost)
    temporal_host = os.getenv("TEMPORAL_HOST", "localhost:7233")
    print(f"🔌 Connecting to Temporal at {temporal_host}...", flush=True)
    
    try:
        client = await Client.connect(temporal_host)
        print(f"✅ Connected to Temporal server", flush=True)
        
        # Set up schedules (idempotent - safe on every startup)
        await setup_schedules(client)
        
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
        print("📅 Schedules: IngestWorkflow (paused), MonitorWorkflow (every minute)", flush=True)
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
