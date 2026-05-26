"""
Temporal Worker - Executes workflows and activities

Logging: Uses structured JSON logging for Grafana Loki.
Set LOG_FORMAT=pretty for development-friendly output.
"""
import asyncio
import os
import sys

# Force unbuffered output
sys.stdout = os.fdopen(sys.stdout.fileno(), 'w', buffering=1)
sys.stderr = os.fdopen(sys.stderr.fileno(), 'w', buffering=1)

# Configure structured logging BEFORE importing temporalio
from src.utils.footy_logging import configure_logging, get_fallback_logger, log
configure_logging()

MODULE = "worker"

def _log_info(action: str, msg: str, **kwargs):
    """Log info using fallback logger."""
    log.info(get_fallback_logger(), MODULE, action, msg, **kwargs)

def _log_error(action: str, msg: str, **kwargs):
    """Log error using fallback logger."""
    log.error(get_fallback_logger(), MODULE, action, msg, error=kwargs.get('error', ''), **{k: v for k, v in kwargs.items() if k != 'error'})

from temporalio.client import Client
from temporalio.runtime import Runtime, TelemetryConfig
from temporalio.worker import Worker
from datetime import timedelta

from src.workflows import (
    IngestWorkflow,
    MonitorWorkflow,
    TwitterWorkflow,
    DownloadWorkflow,
    UploadWorkflow,
)
from src.activities import ingest, monitor, rag, twitter, download, upload


async def setup_schedules(client: Client):
    """Set up workflow schedules (idempotent - safe to call on every startup)"""
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
    
    _log_info("setup_schedules", "Setting up workflow schedules")
    
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
        _log_info("schedule_updated", f"Schedule '{ingest_schedule_id}' updated (ENABLED)", schedule_id=ingest_schedule_id)
    except Exception:
        await client.create_schedule(ingest_schedule_id, ingest_schedule)
        _log_info("schedule_created", f"Created '{ingest_schedule_id}' (ENABLED)", schedule_id=ingest_schedule_id)
    
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
        _log_info("schedule_updated", f"Schedule '{monitor_schedule_id}' updated", schedule_id=monitor_schedule_id)
    except Exception:
        await client.create_schedule(monitor_schedule_id, monitor_schedule)
        _log_info("schedule_created", f"Created '{monitor_schedule_id}' (ENABLED)", schedule_id=monitor_schedule_id)


async def main():
    # Connect to Temporal server (use env var for Docker, fallback to localhost)
    temporal_host = os.getenv("TEMPORAL_HOST", "localhost:7233")
    _log_info("connecting", f"Connecting to Temporal at {temporal_host}", temporal_host=temporal_host)
    
    # Create runtime with worker heartbeat disabled (server doesn't support it)
    # This suppresses "Worker heartbeating configured for runtime, but server does not support it"
    runtime = Runtime(
        telemetry=TelemetryConfig(),
        worker_heartbeat_interval=None,  # Disable worker-level heartbeats
    )
    
    try:
        client = await Client.connect(temporal_host, runtime=runtime)
        _log_info("connected", "Connected to Temporal server", temporal_host=temporal_host)
        
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
        # Build workflow + activity lists explicitly so we can introspect
        # them for the startup banner (no more hand-maintained counts).
        registered_workflows = [
            IngestWorkflow,
            MonitorWorkflow,
            TwitterWorkflow,
            DownloadWorkflow,
            UploadWorkflow,
        ]
        registered_activities = [
            # Ingest
            ingest.fetch_todays_fixtures,
            ingest.fetch_fixtures_by_ids,
            ingest.categorize_and_store_fixtures,
            ingest.cleanup_old_fixtures,            # 14-day retention sweep
            # Monitor
            monitor.pre_activate_upcoming_fixtures, # Time-based pre-activation
            monitor.fetch_active_fixtures,
            monitor.store_and_compare,
            monitor.process_fixture_events,
            monitor.complete_fixture_if_ready,
            monitor.notify_frontend_refresh,
            # RAG (team alias lookup)
            rag.get_team_aliases,
            rag.save_team_aliases,
            rag.get_cached_team_aliases,
            # Twitter (granular for retry control)
            twitter.check_event_exists,
            twitter.get_twitter_search_data,
            twitter.execute_twitter_search,
            twitter.save_discovered_videos,
            twitter.set_monitor_complete,
            twitter.get_download_workflow_count,
            # Download (download / validate / hash / cleanup / queue)
            download.download_single_video,
            download.validate_video_is_soccer,
            download.generate_video_hash,
            download.cleanup_download_temp,
            download.queue_videos_for_upload,
            download.register_download_workflow,
            download.check_and_mark_download_complete,
            # Upload (S3 dedup/upload — serialized per event)
            upload.fetch_event_data,
            upload.deduplicate_by_md5,
            upload.deduplicate_videos,
            upload.upload_single_video,
            upload.update_video_in_place,
            upload.bump_video_popularity,
            upload.save_video_objects,
            upload.recalculate_video_ranks,
            upload.cleanup_individual_files,
            upload.cleanup_fixture_temp_dirs,
            upload.cleanup_upload_temp,
        ]

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
            workflows=registered_workflows,
            activities=registered_activities,
        )

        # Auto-derive startup banner counts from what we actually registered.
        # Replaces the hand-maintained "Workflows: ...RAG..." and "42 total
        # (4 ingest, 10 monitor, 3 rag, ...)" strings, which drifted every
        # time we changed the registration list. Audit's canonical example
        # of the "static counts in docs" anti-pattern.
        from collections import Counter

        def _activity_package_label(fn) -> str:
            """Group activities by their `src/activities/<PKG>` segment.

            For a function defined at `src.activities.foo`, returns "foo".
            For a function defined at `src.activities.upload.core` (package
            with sub-modules, post-Phase-3), still returns "upload" — so
            the banner aggregates the package as one unit instead of
            splitting it across its sub-files.
            """
            parts = fn.__module__.split(".")
            try:
                # Find the "activities" segment, return the one after.
                i = parts.index("activities")
                return parts[i + 1] if i + 1 < len(parts) else parts[-1]
            except ValueError:
                return parts[-1]

        per_module = Counter(_activity_package_label(fn) for fn in registered_activities)
        breakdown = ", ".join(f"{n} {m}" for m, n in per_module.most_common())
        workflow_names = ", ".join(w.__name__.replace("Workflow", "") for w in registered_workflows)

        _log_info("worker_started", "Worker started - listening on 'found-footy' task queue", task_queue="found-footy")
        _log_info("workflows_registered", f"Workflows: {workflow_names}", workflow_count=len(registered_workflows))
        _log_info("activities_registered", f"Activities: {len(registered_activities)} total ({breakdown})", activity_count=len(registered_activities))
        _log_info("schedules_configured", "Schedules: IngestWorkflow (daily 00:05 UTC), MonitorWorkflow (every 30s)")
        await worker.run()
    except Exception as e:
        import traceback
        _log_error("worker_failed", "Worker failed", error=str(e), traceback=traceback.format_exc())
        sys.exit(1)


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        _log_info("worker_stopped", "Worker stopped")
