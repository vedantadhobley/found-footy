"""
RAG Workflow - Team Alias Lookup via Ollama LLM

FAST workflow that resolves team aliases and hands off to TwitterWorkflow.

Design Philosophy:
- This workflow should complete QUICKLY (~30-90 seconds)
- Does NOT wait for TwitterWorkflow - fires it off and returns immediately
- TwitterWorkflow runs independently and manages its own lifecycle
- TwitterWorkflow WILL wait for its Download child workflows (required for data integrity)

Why fire-and-forget Twitter from RAG?
- RAG's job is ONLY alias resolution - a quick lookup task
- Twitter runs for 10-15 minutes (3 attempts with 3-min waits)
- No reason for RAG to hold resources waiting for Twitter
- Twitter handles marking _download_complete after all downloads finish

Flow:
1. Check cache for pre-computed aliases (from Ingest)
2. If miss: Query get_team_aliases activity (calls Wikidata + Ollama)
3. Save aliases to event for debugging
4. START TwitterWorkflow (fire-and-forget) with resolved aliases
5. Return immediately

Examples:
- "Atletico de Madrid" → ["Atletico", "Atleti", "ATM"]
- "Manchester United"  → ["MUFC", "Devils", "Utd", "Manchester", "United"]

Triggered by: Monitor when event reaches _monitor_complete=true
Starts: TwitterWorkflow (fire-and-forget, runs independently)

Note: Aliases are pre-cached at ingestion time for BOTH teams in each fixture.
This ensures opponent teams (non-tracked) have aliases ready when they score.
"""
from temporalio import workflow
from temporalio.common import RetryPolicy
from temporalio.workflow import ParentClosePolicy
from datetime import timedelta
from dataclasses import dataclass
from typing import List, Optional

with workflow.unsafe.imports_passed_through():
    from src.activities import rag as rag_activities
    from src.workflows.twitter_workflow import TwitterWorkflow, TwitterWorkflowInput
    from src.utils.footy_logging import log

MODULE = "rag_workflow"


@dataclass
class RAGWorkflowInput:
    """Input for RAGWorkflow"""
    fixture_id: int
    event_id: str
    team_id: int                    # API-Football team ID (for caching)
    team_name: str                  # "Liverpool"
    player_name: Optional[str]      # "Mohamed Salah" or None if unknown
    minute: int                     # For workflow ID naming
    extra: Optional[int] = None    # Extra time minutes


@workflow.defn
class RAGWorkflow:
    """
    Resolve team aliases via cache or LLM, then START Twitter search workflow.
    
    IMPORTANT: This workflow does NOT wait for TwitterWorkflow to complete.
    It fires off TwitterWorkflow and returns immediately. This is safe because:
    - TwitterWorkflow manages its own lifecycle (3 attempts, waits for downloads)
    - TwitterWorkflow marks _download_complete only after all downloads finish
    - Fixture completion logic checks _download_complete flag
    
    This workflow:
    1. Checks cache for pre-computed aliases (from Ingest) - fast path
    2. Falls back to full RAG lookup if cache miss - slower but rare
    3. Saves aliases to event for debugging
    4. STARTS TwitterWorkflow (fire-and-forget)
    5. Returns immediately with aliases resolved
    
    Expected duration: 30-90 seconds
    """
    
    @workflow.run
    async def run(self, input: RAGWorkflowInput) -> dict:
        log.info(workflow.logger, MODULE, "started", "RAGWorkflow STARTED",
                 event_id=input.event_id, team_id=input.team_id,
                 team_name=input.team_name, player=input.player_name,
                 minute=input.minute)
        
        # =========================================================================
        # Step 1: Try fast cache lookup first (pre-cached during ingestion)
        # =========================================================================
        log.info(workflow.logger, MODULE, "checking_cache",
                 "Checking alias cache", team_id=input.team_id)
        
        aliases = None
        cache_hit = False
        
        try:
            aliases = await workflow.execute_activity(
                rag_activities.get_cached_team_aliases,
                input.team_id,
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(
                    maximum_attempts=3,
                    initial_interval=timedelta(seconds=2),
                    backoff_coefficient=2.0,
                ),
            )
            if aliases:
                cache_hit = True
                log.info(workflow.logger, MODULE, "cache_hit",
                         "Cache HIT", team_id=input.team_id, aliases=aliases)
        except Exception as e:
            log.error(workflow.logger, MODULE, "cache_lookup_failed",
                      "Cache lookup FAILED", team_id=input.team_id, error=str(e))
        
        if not aliases:
            # Cache miss - do full RAG lookup (Wikidata + LLM)
            log.info(workflow.logger, MODULE, "cache_miss",
                     "Cache MISS - Running full RAG pipeline", team_id=input.team_id)
            
            try:
                aliases = await workflow.execute_activity(
                    rag_activities.get_team_aliases,
                    args=[input.team_id, input.team_name],
                    start_to_close_timeout=timedelta(seconds=90),
                    retry_policy=RetryPolicy(
                        maximum_attempts=3,
                        initial_interval=timedelta(seconds=5),
                        backoff_coefficient=2.0,
                    ),
                )
                log.info(workflow.logger, MODULE, "rag_success",
                         "Full RAG SUCCESS", team_id=input.team_id, aliases=aliases)
            except Exception as e:
                log.error(workflow.logger, MODULE, "rag_failed",
                          "Full RAG FAILED", team_id=input.team_id, error=str(e))
                # Fallback to just team name - better than nothing
                aliases = [input.team_name]
                log.warning(workflow.logger, MODULE, "using_fallback",
                            "Using FALLBACK aliases", team_id=input.team_id, aliases=aliases)
        
        # =========================================================================
        # Step 2: Save aliases to MongoDB for debugging/visibility
        # =========================================================================
        log.info(workflow.logger, MODULE, "saving_aliases",
                 "Saving aliases to MongoDB", event_id=input.event_id)
        
        try:
            await workflow.execute_activity(
                rag_activities.save_team_aliases,
                args=[input.fixture_id, input.event_id, aliases],
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(maximum_attempts=3),
            )
            log.info(workflow.logger, MODULE, "aliases_saved",
                     "Aliases saved", event_id=input.event_id, aliases=aliases)
        except Exception as e:
            log.warning(workflow.logger, MODULE, "save_aliases_failed",
                        "Failed to save aliases - Continuing anyway",
                        event_id=input.event_id, error=str(e))
        
        # =========================================================================
        # Step 3: START TwitterWorkflow (fire-and-forget)
        # We don't wait for Twitter - it runs independently and handles:
        # - 3 search attempts with 3-min spacing
        # - Waiting for each Download workflow to complete
        # - Marking _download_complete when all done
        # =========================================================================
        player_last = input.player_name.split()[-1] if input.player_name else "Unknown"
        team_clean = input.team_name.replace(" ", "_").replace(".", "_").replace("-", "_")
        minute_str = f"{input.minute}+{input.extra}min" if input.extra else f"{input.minute}min"
        
        twitter_workflow_id = f"twitter-{team_clean}-{player_last}-{minute_str}-{input.event_id}"
        
        log.info(workflow.logger, MODULE, "starting_twitter",
                 "Starting TwitterWorkflow (fire-and-forget)",
                 twitter_id=twitter_workflow_id, aliases=aliases)
        
        try:
            # START child workflow - don't wait (fire-and-forget)
            # ParentClosePolicy.ABANDON: Twitter continues even when RAG completes
            await workflow.start_child_workflow(
                TwitterWorkflow.run,
                TwitterWorkflowInput(
                    fixture_id=input.fixture_id,
                    event_id=input.event_id,
                    player_name=input.player_name,
                    team_aliases=aliases,
                ),
                id=twitter_workflow_id,
                parent_close_policy=ParentClosePolicy.ABANDON,
                task_queue="found-footy",  # Explicit queue - don't inherit from parent
                # No execution_timeout - Twitter manages its own lifecycle
            )
            log.info(workflow.logger, MODULE, "twitter_started",
                     "TwitterWorkflow STARTED", twitter_id=twitter_workflow_id)
        except Exception as e:
            log.error(workflow.logger, MODULE, "twitter_start_failed",
                      "Failed to start TwitterWorkflow",
                      twitter_id=twitter_workflow_id, error=str(e))
            # Re-raise - this is a critical failure
            raise
        
        log.info(workflow.logger, MODULE, "completed", "RAGWorkflow COMPLETED",
                 event_id=input.event_id, cache_hit=cache_hit,
                 aliases=aliases, twitter_id=twitter_workflow_id)
        
        return {
            "status": "completed",
            "event_id": input.event_id,
            "team_id": input.team_id,
            "aliases": aliases,
            "cache_hit": cache_hit,
            "twitter_workflow_id": twitter_workflow_id,
        }
