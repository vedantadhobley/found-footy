"""
RAG Workflow - Team Alias Lookup (Stub)

This workflow will eventually use RAG to get team name aliases for Twitter search.
Currently a stub that passes the team name through as-is.

FUTURE: Query local LLM for variations like:
- "Atletico de Madrid" → ["Atletico", "Atleti", "ATM"]
- "Manchester United"  → ["Man United", "Man Utd", "MUFC"]

Triggered by Monitor when event reaches _monitor_complete=true.
Triggers TwitterWorkflow with resolved aliases.
"""
from temporalio import workflow
from temporalio.common import RetryPolicy
from datetime import timedelta
from dataclasses import dataclass
from typing import List

with workflow.unsafe.imports_passed_through():
    from src.activities import rag as rag_activities
    from src.workflows.twitter_workflow import TwitterWorkflow, TwitterWorkflowInput


@dataclass
class RAGWorkflowInput:
    """Input for RAGWorkflow"""
    fixture_id: int
    event_id: str
    team_name: str      # "Liverpool"
    player_name: str    # "Mohamed Salah"
    minute: int         # For workflow ID naming
    extra: int | None   # Extra time minutes


@workflow.defn
class RAGWorkflow:
    """
    Resolve team aliases via RAG, then trigger Twitter search workflow.
    
    This workflow exists to:
    1. Encapsulate the RAG lookup (stub now, AI later)
    2. Decouple monitor from Twitter retry logic
    3. Provide clean handoff point for future RAG implementation
    
    Current stub behavior:
    - Returns [team_name] as single-element list
    - TwitterWorkflow handles the single alias
    
    Future RAG behavior:
    - Query local LLM for 3 variations
    - Return ["Liverpool", "LFC", "Reds"]
    """
    
    @workflow.run
    async def run(self, input: RAGWorkflowInput) -> dict:
        workflow.logger.info(f"🔍 RAG lookup for team: {input.team_name}")
        
        # =========================================================================
        # Step 1: Get team aliases (stub - just returns team name)
        # =========================================================================
        aliases = await workflow.execute_activity(
            rag_activities.get_team_aliases,
            input.team_name,
            start_to_close_timeout=timedelta(seconds=30),
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
