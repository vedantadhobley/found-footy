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
from temporalio.runtime import Runtime, TelemetryConfig
from temporalio.worker import Worker
from datetime import timedelta

from src.workflows import (
    IngestWorkflow,
    MonitorWorkflow,
    RAGWorkflow,
    TwitterWorkflow,
    DownloadWorkflow,
    UploadWorkflow,
)
from src.activities import ingest, monitor, rag, twitter, download, upload


async def setup_schedules(client: Client):
    """Set up workflow schedules (idempotent - safe to call on every startup)"""
    from datetime import timedelta
    from temporalio.client import (
        Schedule,
        ScheduleActionStartWorkflow,
        ScheduleIntervalSpec,
        ScheduleOverlapPolicy,
        SchedulePolicy,
        ScheduleSpec,
        ScheduleState,
        ScheduleUpdate,
    )
    
    print("📅 Setting up workflow schedules...", flush=True)
    
    # Schedule 1: IngestWorkflow - Daily at 00:05 UTC
    # Fetches today's fixtures from all top-5 European leagues (96 teams)
    # and pre-caches RAG aliases for Twitter search
    ingest_schedule_id = "ingest-daily"
    
    # Define the schedule config (used for both create and update)
    ingest_schedule = Schedule(
        action=ScheduleActionStartWorkflow(
            IngestWorkflow.run,
            id="ingest-scheduled",  # Simple ID - Temporal adds timestamp suffix
            task_queue="found-footy",
        ),
        spec=ScheduleSpec(cron_expressions=["5 0 * * *"]),  # 00:05 UTC daily
        state=ScheduleState(
            paused=False,
            note="Daily fixture ingestion for top-5 leagues",
        ),
    )
    
    try:
        ingest_handle = client.get_schedule_handle(ingest_schedule_id)
        await ingest_handle.describe()
        # Schedule exists - update it to ensure config is current
        await ingest_handle.update(lambda _: ScheduleUpdate(schedule=ingest_schedule))
        print(f"   ✓ Schedule '{ingest_schedule_id}' updated (ENABLED)", flush=True)
    except Exception:
        await client.create_schedule(ingest_schedule_id, ingest_schedule)
        print(f"   ✓ Created '{ingest_schedule_id}' (ENABLED)", flush=True)
    
    # Schedule 2: MonitorWorkflow - Every 30 seconds (ENABLED by default)
    # 30s gives faster debounce (1.5 min vs 3 min at 60s) while staying within API limits:
    # - 2 API calls per cycle (staging batch + active batch)
    # - 2880 cycles/day × 2 = 5,760 calls/day (Pro plan allows 7,500)
    #
    # OVERLAP POLICY: SKIP
    # If a monitor takes longer than 30s (e.g., slow API response), the next
    # scheduled trigger is skipped. This prevents race conditions from timeouts
    # and ensures each monitor runs to completion. A 45s monitor means one skipped
    # cycle, then the next starts at 60s - no big deal.
    #
    # IMPORTANT: We always UPDATE existing schedules to ensure config changes take effect!
    # Previously, we only created schedules if they didn't exist, which meant:
    # - Old schedule with 25s execution_timeout persisted even after code removed it
    # - Workflows kept timing out because Temporal used the old schedule config
    monitor_schedule_id = "monitor-every-30s"
    
    # Define the schedule config (used for both create and update)
    monitor_schedule = Schedule(
        action=ScheduleActionStartWorkflow(
            MonitorWorkflow.run,
            id="monitor-scheduled",  # Temporal adds timestamp suffix for unique IDs
            task_queue="found-footy",
            # NO execution_timeout - let monitor run to completion
            # If it takes 90s due to slow API, the 30s scheduled one skips (SKIP overlap policy)
            # This prevents race conditions from timeouts killing workflows mid-execution
            #
            # task_timeout: How long a workflow task (replay + new work) can take
            # During CL peak, API can take 20-35s per call, monitor can take 90-120s total
            # Set to 180s to avoid spurious "Task not found" warnings from premature retries
            task_timeout=timedelta(seconds=180),
        ),
        spec=ScheduleSpec(intervals=[ScheduleIntervalSpec(every=timedelta(seconds=30))]),
        state=ScheduleState(
            paused=False,
            note="Running every 30 seconds",
        ),
        policy=SchedulePolicy(
            overlap=ScheduleOverlapPolicy.SKIP,  # Skip if previous still running
        ),
    )
    
    try:
        monitor_handle = client.get_schedule_handle(monitor_schedule_id)
        await monitor_handle.describe()
        # Schedule exists - update it to ensure config is current
        await monitor_handle.update(lambda _: ScheduleUpdate(schedule=monitor_schedule))
        print(f"   ✓ Schedule '{monitor_schedule_id}' updated", flush=True)
    except Exception:
        await client.create_schedule(monitor_schedule_id, monitor_schedule)
        print(f"   ✓ Created '{monitor_schedule_id}' (ENABLED)", flush=True)


async def main():
    # Connect to Temporal server (use env var for Docker, fallback to localhost)
    temporal_host = os.getenv("TEMPORAL_HOST", "localhost:7233")
    print(f"🔌 Connecting to Temporal at {temporal_host}...", flush=True)
    
    # Create runtime with worker heartbeat disabled (server doesn't support it)
    # This suppresses "Worker heartbeating configured for runtime, but server does not support it"
    runtime = Runtime(
        telemetry=TelemetryConfig(),
        worker_heartbeat_interval=None,  # Disable worker-level heartbeats
    )
    
    try:
        client = await Client.connect(temporal_host, runtime=runtime)
        print(f"✅ Connected to Temporal server", flush=True)
        
        # Set up schedules (idempotent - safe on every startup)
        await setup_schedules(client)
        
        # Create worker that listens on task queue
        # 
        # MULTI-WORKER DEPLOYMENT:
        # With 4 worker replicas (docker-compose deploy.replicas: 4), each worker
        # handles a portion of the workload. Temporal distributes tasks automatically.
        # 
        # Per-worker limits (multiplied by replica count for total capacity):
        # - 4 workers × 10 workflow tasks = 40 concurrent workflow executions
        # - 4 workers × 30 activities = 120 concurrent activities
        #
        worker = Worker(
            client,
            task_queue="found-footy",
            # Activities are I/O bound (MongoDB, S3, HTTP), can run more per worker
            max_concurrent_activities=30,
            # Workflow tasks are CPU bound (replay/execution), keep lower per worker
            # With 4 workers: 4 × 10 = 40 concurrent workflow tasks total
            max_concurrent_workflow_tasks=10,
            # Sticky queue: try to keep workflow tasks on the same worker to avoid replay
            # Default 10s is fine - with 4 workers and lower concurrency per worker,
            # there's less contention so sticky tasks get picked up quickly
            sticky_queue_schedule_to_start_timeout=timedelta(seconds=10),
            workflows=[
                IngestWorkflow,
                MonitorWorkflow,
                RAGWorkflow,
                TwitterWorkflow,
                DownloadWorkflow,
                UploadWorkflow,
            ],
            activities=[
                # Ingest activities
                ingest.fetch_todays_fixtures,
                ingest.fetch_fixtures_by_ids,
                ingest.categorize_and_store_fixtures,
                ingest.cleanup_old_fixtures,  # Delete fixtures older than 14 days
                # Monitor activities (staging + active processing)
                monitor.fetch_staging_fixtures,
                monitor.pre_activate_upcoming_fixtures,  # NEW: Time-based pre-activation
                monitor.process_staging_fixtures,  # DEPRECATED: Kept for compatibility
                monitor.activate_pending_fixtures,  # DEPRECATED: Kept for compatibility
                monitor.fetch_active_fixtures,
                monitor.store_and_compare,
                monitor.process_fixture_events,
                monitor.sync_fixture_metadata,
                monitor.confirm_twitter_workflow_started,  # DEPRECATED: Use set_monitor_complete
                monitor.check_twitter_workflow_running,
                monitor.complete_fixture_if_ready,
                monitor.notify_frontend_refresh,
                monitor.register_monitor_workflow,  # Workflow-ID-based tracking
                # RAG activities (team alias lookup)
                rag.get_team_aliases,
                rag.save_team_aliases,
                rag.get_cached_team_aliases,
                # Twitter activities (4 granular for retry control)
                twitter.check_event_exists,
                twitter.get_twitter_search_data,
                twitter.execute_twitter_search,
                twitter.save_discovered_videos,
                twitter.set_monitor_complete,  # NEW: Called at start of TwitterWorkflow
                twitter.get_download_workflow_count,  # NEW: For while loop condition
                # Download activities (download, validate, hash, cleanup, queue)
                download.download_single_video,
                download.validate_video_is_soccer,  # AI vision validation
                download.generate_video_hash,  # Perceptual hash with heartbeat
                download.increment_twitter_count,  # DEPRECATED: Will be removed after refactor
                download.cleanup_download_temp,  # Cleanup on failure
                download.queue_videos_for_upload,  # Signal-with-start to queue videos for upload
                download.register_download_workflow,  # NEW: Called at start of DownloadWorkflow
                download.check_and_mark_download_complete,  # NEW: Check count and mark complete
                # Upload activities (S3 dedup/upload - serialized per event)
                upload.fetch_event_data,  # Get existing S3 videos
                upload.deduplicate_by_md5,  # Fast MD5 dedup against S3
                upload.deduplicate_videos,  # Perceptual hash dedup against S3
                upload.upload_single_video,
                upload.update_video_in_place,  # Atomic in-place update for replacements
                upload.replace_s3_video,
                upload.bump_video_popularity,
                upload.save_video_objects,
                upload.recalculate_video_ranks,
                upload.cleanup_individual_files,  # Cleanup individual files after successful upload
                upload.cleanup_fixture_temp_dirs,  # Cleanup all temp dirs when fixture completes
                upload.cleanup_upload_temp,  # Cleanup single temp dir
            ],
        )
        
        print("🚀 Worker started - listening on 'found-footy' task queue", flush=True)
        print("📋 Workflows: Ingest, Monitor, RAG, Twitter, Download, Upload", flush=True)
        print("🔧 Activities: 33 total (4 ingest, 9 monitor, 3 rag, 4 twitter, 6 download, 10 upload)", flush=True)
        print("📅 Schedules: IngestWorkflow (paused), MonitorWorkflow (every 30s)", flush=True)
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
