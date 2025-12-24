"""
RAG Workflow - Team Alias Lookup via Ollama LLM

Uses local Ollama LLM (GPU-accelerated) to generate team aliases for Twitter search.

Flow:
1. Query get_team_aliases activity (checks cache, calls Ollama if miss)
2. Save aliases to event for debugging
3. Trigger TwitterWorkflow with resolved aliases

Examples:
- "Atletico de Madrid" → ["Atletico", "Atleti", "ATM"]
- "Manchester United"  → ["Man United", "Man Utd", "MUFC"]

Triggered by Monitor when event reaches _monitor_complete=true.
Triggers TwitterWorkflow with resolved aliases.
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
    Resolve team aliases via Ollama LLM, then trigger Twitter search workflow.
    
    This workflow:
    1. Queries Ollama for 3 team aliases (cached by team_id)
    2. Normalizes aliases (removes diacritics)
    3. Passes aliases to TwitterWorkflow for search
    """
    
    @workflow.run
    async def run(self, input: RAGWorkflowInput) -> dict:
        workflow.logger.info(f"🔍 RAG lookup for team {input.team_id}: {input.team_name}")
        
        # =========================================================================
        # Step 1: Get team aliases (checks cache, calls Ollama if miss)
        # =========================================================================
        aliases = await workflow.execute_activity(
            rag_activities.get_team_aliases,
            args=[input.team_id, input.team_name],
            start_to_close_timeout=timedelta(seconds=60),  # LLM can take a few seconds
            retry_policy=RetryPolicy(maximum_attempts=2),
        )
        
        workflow.logger.info(f"📋 Resolved aliases: {aliases}")
        
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
