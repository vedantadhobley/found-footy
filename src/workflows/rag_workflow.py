"""
RAG Workflow - Team Alias Lookup via Ollama LLM

Uses local Ollama LLM (GPU-accelerated) to generate team aliases for Twitter search.

Flow:
1. Check cache for pre-computed aliases (from Ingest)
2. If miss: Query get_team_aliases activity (calls Wikidata + Ollama)
3. Save aliases to event for debugging
4. Trigger TwitterWorkflow with resolved aliases

Examples:
- "Atletico de Madrid" → ["Atletico", "Atleti", "ATM"]
- "Manchester United"  → ["MUFC", "Devils", "Utd", "Manchester", "United"]

Triggered by Monitor when event reaches _monitor_complete=true.
Triggers TwitterWorkflow with resolved aliases.

Note: Aliases are pre-cached at ingestion time for BOTH teams in each fixture.
This ensures opponent teams (non-tracked) have aliases ready when they score.
"""
from temporalio import workflow
from temporalio.common import RetryPolicy
from datetime import timedelta
from dataclasses import dataclass
from typing import List, Optional

with workflow.unsafe.imports_passed_through():
    from src.activities import rag as rag_activities
    from src.workflows.twitter_workflow import TwitterWorkflow, TwitterWorkflowInput


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
    Resolve team aliases via cache or LLM, then trigger Twitter search workflow.
    
    This workflow:
    1. Checks cache for pre-computed aliases (from Ingest)
    2. Falls back to full RAG lookup if cache miss
    3. Normalizes aliases (removes diacritics)
    4. Passes aliases to TwitterWorkflow for search
    """
    
    @workflow.run
    async def run(self, input: RAGWorkflowInput) -> dict:
        workflow.logger.info(f"🔍 RAG lookup for team {input.team_id}: {input.team_name}")
        
        # =========================================================================
        # Step 1: Try fast cache lookup first (pre-cached during ingestion)
        # =========================================================================
        aliases = await workflow.execute_activity(
            rag_activities.get_cached_team_aliases,
            input.team_id,
            start_to_close_timeout=timedelta(seconds=10),
            retry_policy=RetryPolicy(maximum_attempts=2),
        )
        
        if aliases:
            workflow.logger.info(f"📦 Cache hit: {aliases}")
        else:
            # Cache miss - do full RAG lookup (Wikidata + LLM)
            workflow.logger.info(f"🔄 Cache miss, running full RAG pipeline...")
            aliases = await workflow.execute_activity(
                rag_activities.get_team_aliases,
                args=[input.team_id, input.team_name],
                start_to_close_timeout=timedelta(seconds=60),  # LLM can take a few seconds
                retry_policy=RetryPolicy(maximum_attempts=2),
            )
            workflow.logger.info(f"📋 RAG resolved aliases: {aliases}")
        
        # =========================================================================
        # Step 2: Save aliases to MongoDB for debugging/visibility
        # =========================================================================
        await workflow.execute_activity(
            rag_activities.save_team_aliases,
            args=[input.fixture_id, input.event_id, aliases],
            start_to_close_timeout=timedelta(seconds=10),
            retry_policy=RetryPolicy(maximum_attempts=2),
        )
        
        # =========================================================================
        # Step 3: Trigger TwitterWorkflow with aliases
        # TwitterWorkflow handles all 3 attempts internally with 3-min timers
        # =========================================================================
        player_last = input.player_name.split()[-1] if input.player_name else "Unknown"
        team_clean = input.team_name.replace(" ", "_").replace(".", "_").replace("-", "_")
        minute_str = f"{input.minute}+{input.extra}min" if input.extra else f"{input.minute}min"
        
        twitter_workflow_id = f"twitter-{team_clean}-{player_last}-{minute_str}-{input.event_id}"
        
        workflow.logger.info(f"🐦 Starting TwitterWorkflow: {twitter_workflow_id}")
        
        # Execute as child workflow (waits for completion)
        # TwitterWorkflow manages all 3 attempts internally
        result = await workflow.execute_child_workflow(
            TwitterWorkflow.run,
            TwitterWorkflowInput(
                fixture_id=input.fixture_id,
                event_id=input.event_id,
                player_name=input.player_name,
                team_aliases=aliases,
            ),
            id=twitter_workflow_id,
            execution_timeout=timedelta(minutes=20),  # 3 attempts @ ~5min each + timers
        )
        
        return {
            "status": "completed",
            "aliases": aliases,
            "twitter_result": result,
        }
