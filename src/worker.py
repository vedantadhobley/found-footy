"""
Temporal Worker - Executes workflows and activities
"""
import asyncio
import logging
import os
import re
import sys

# Force unbuffered output
sys.stdout = os.fdopen(sys.stdout.fileno(), 'w', buffering=1)
sys.stderr = os.fdopen(sys.stderr.fileno(), 'w', buffering=1)


class CleanTemporalFormatter(logging.Formatter):
    """
    Custom formatter that strips Temporal's context dict from activity/workflow logs.
    
    Temporal SDK appends context dicts to logs like:
    - Activities: {'activity_id': ..., 'workflow_id': ...}
    - Workflows: {'attempt': ..., 'namespace': ..., 'workflow_id': ...}
    
    This is verbose for development - we strip it and show just the message.
    """
    
    # Regex to match trailing context dicts (both activity and workflow patterns)
    CONTEXT_PATTERN = re.compile(r"\s*\(\{'.+\}\)\s*$")
    
    def format(self, record):
        msg = record.getMessage()
        # Strip the Temporal context dict from the end of the message
        cleaned = self.CONTEXT_PATTERN.sub("", msg)
        return cleaned


# Configure logging BEFORE importing temporalio
# This ensures activity.logger.info() and workflow.logger.info() show up
handler = logging.StreamHandler(sys.stdout)
handler.setFormatter(CleanTemporalFormatter())

logging.basicConfig(
    level=logging.INFO,
    handlers=[handler],
    force=True,  # Override any existing config
)

# Set the specific temporalio loggers to INFO
logging.getLogger("temporalio.activity").setLevel(logging.INFO)
logging.getLogger("temporalio.workflow").setLevel(logging.INFO)

# Reduce noise from other loggers
logging.getLogger("temporalio.worker").setLevel(logging.WARNING)
logging.getLogger("temporalio.client").setLevel(logging.WARNING)
logging.getLogger("urllib3").setLevel(logging.WARNING)
logging.getLogger("pymongo").setLevel(logging.WARNING)

from temporalio.client import Client
from temporalio.worker import Worker

from src.workflows import (
    IngestWorkflow,
    MonitorWorkflow,
    RAGWorkflow,
    TwitterWorkflow,
    DownloadWorkflow,
)
from src.activities import ingest, monitor, rag, twitter, download


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
                RAGWorkflow,
                TwitterWorkflow,
                DownloadWorkflow,
            ],
            activities=[
                # Ingest activities
                ingest.fetch_todays_fixtures,
                ingest.categorize_and_store_fixtures,
                # Monitor activities (staging + active processing)
                monitor.fetch_staging_fixtures,
                monitor.process_staging_fixtures,
                monitor.activate_pending_fixtures,
                monitor.fetch_active_fixtures,
                monitor.store_and_compare,
                monitor.process_fixture_events,
                monitor.sync_fixture_metadata,
                monitor.complete_fixture_if_ready,
                monitor.notify_frontend_refresh,
                # RAG activities (team alias lookup)
                rag.get_team_aliases,
                rag.save_team_aliases,
                rag.get_cached_team_aliases,
                # Twitter activities (5 granular for retry control)
                twitter.check_event_exists,
                twitter.get_twitter_search_data,
                twitter.execute_twitter_search,
                twitter.save_discovered_videos,
                twitter.mark_event_twitter_complete,
                twitter.update_twitter_attempt,
                # Download activities (8 granular for per-video retry + quality replacement)
                download.fetch_event_data,
                download.download_single_video,
                download.deduplicate_videos,
                download.validate_video_is_soccer,  # AI vision validation
                download.upload_single_video,
                download.mark_download_complete,
                download.replace_s3_video,
                download.bump_video_popularity,
            ],
        )
        
        print("🚀 Worker started - listening on 'found-footy' task queue", flush=True)
        print("📋 Workflows: Ingest, Monitor, RAG, Twitter, Download", flush=True)
        print("🔧 Activities: 27 total (2 ingest, 9 monitor, 4 rag, 6 twitter, 8 download)", flush=True)
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
