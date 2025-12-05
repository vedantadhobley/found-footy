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
    TwitterWorkflow,
    DownloadWorkflow,
)
from src.activities import ingest, monitor, twitter, download


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
                    id="ingest-scheduled",  # Simple ID - Temporal adds timestamp suffix
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
                    id="monitor-scheduled",  # Simple ID - Temporal adds timestamp suffix
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
                TwitterWorkflow,
                DownloadWorkflow,
            ],
            activities=[
                # Ingest activities
                ingest.fetch_todays_fixtures,
                ingest.categorize_and_store_fixtures,
                # Monitor activities (includes inline debounce)
                monitor.activate_fixtures,
                monitor.fetch_active_fixtures,
                monitor.store_and_compare,
                monitor.process_fixture_events,
                monitor.sync_fixture_metadata,
                monitor.complete_fixture_if_ready,
                # Twitter activities (3 granular for retry control)
                twitter.get_twitter_search_data,
                twitter.execute_twitter_search,
                twitter.save_twitter_results,
                # Download activities (5 granular for per-video retry)
                download.fetch_event_data,
                download.download_single_video,
                download.deduplicate_videos,
                download.upload_single_video,
                download.mark_download_complete,
            ],
        )
        
        print("🚀 Worker started - listening on 'found-footy' task queue", flush=True)
        print("📋 Workflows: Ingest, Monitor, Twitter, Download", flush=True)
        print("🔧 Activities: 14 total (2 ingest, 6 monitor, 3 twitter, 5 download)", flush=True)
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
