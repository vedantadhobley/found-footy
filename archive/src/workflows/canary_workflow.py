"""
DOMCanaryWorkflow — hourly schedule wrapper around run_dom_canary.

Trivially thin: just executes the activity with sensible retry policy
and returns the result so the schedule history is queryable in the
Temporal UI. The actionable signal is in the activity's log lines
(Grafana-alertable on `module="canary", action="canary_failed"` etc.).

Phase 4 (P4b, 2026-05-26).
"""

from datetime import timedelta

from temporalio import workflow
from temporalio.common import RetryPolicy

with workflow.unsafe.imports_passed_through():
    from src.activities import canary as canary_activities
    from src.utils.footy_logging import log

MODULE = "canary_workflow"


@workflow.defn
class DOMCanaryWorkflow:
    """
    Run the DOM canary once. Designed to be invoked on an hourly Temporal
    schedule (see src/worker.py for setup).

    No retry inside the activity — a single failed canary check is the
    signal we want. Repeated retries within an hour would just add noise
    to the dashboard.
    """

    @workflow.run
    async def run(self) -> dict:
        log.info(workflow.logger, MODULE, "canary_run_started",
                 "DOMCanaryWorkflow started")

        result = await workflow.execute_activity(
            canary_activities.run_dom_canary,
            start_to_close_timeout=timedelta(seconds=150),  # twitter activity timeout is 120s
            retry_policy=RetryPolicy(maximum_attempts=1),  # don't retry — single signal wanted
        )

        log.info(workflow.logger, MODULE, "canary_run_complete",
                 f"DOMCanaryWorkflow complete: passed={result.get('passed')}",
                 passed=result.get("passed"),
                 reason=result.get("reason"),
                 videos_found=result.get("videos_found"))
        return result
