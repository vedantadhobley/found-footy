# RAG Implementation for Team Alias Lookup

## Overview

This document outlines the architectural changes to decouple Twitter search from the Monitor workflow and implement a RAG (Retrieval-Augmented Generation) system for generating team name aliases used in Twitter video searches.

## Current Architecture

```
MonitorWorkflow (scheduled every 1 min)
    │
    ├── Debounce events (3 polls = 3 min to confirm goal)
    │
    └── On monitor_complete=true:
        ├── Check _twitter_count, trigger TwitterWorkflow
        ├── Next poll: if !twitter_complete && count < 3, trigger again
        └── Repeat until 3 attempts made
```

**Problems:**
1. Twitter retries are coupled to 1-minute monitor interval
2. We want 3-minute spacing between Twitter attempts
3. No intelligent team name aliasing (e.g., "Atletico de Madrid" should search as "Atletico", "Atleti", "ATM")

## Proposed Architecture

```
MonitorWorkflow (scheduled every 1 min)
    │
    ├── Debounce events (3 polls = 3 min)
    │
    └── On monitor_complete=true → trigger RAGWorkflow (ONCE, fire-and-forget)
                                        │
                                        ├── get_team_aliases activity
                                        │   └── Query local LLM for aliases
                                        │
                                        └── Start TwitterWorkflow (child)
                                                │
                                                ├── ATTEMPT 1:
                                                │   ├── Search "Salah Liverpool"
                                                │   ├── Search "Salah LFC"  
                                                │   ├── Search "Salah Reds"
                                                │   └── Download → S3
                                                │
                                                ├── 3-min durable timer
                                                │
                                                ├── ATTEMPT 2: Same 3 queries
                                                │
                                                ├── 3-min durable timer
                                                │
                                                ├── ATTEMPT 3: Same 3 queries
                                                │
                                                └── Mark _twitter_complete=true
```

## File Structure

```
src/
├── workflows/
│   ├── rag_workflow.py          # NEW: RAGWorkflow
│   ├── twitter_workflow.py      # MODIFIED: Self-managing retries
│   ├── download_workflow.py     # UNCHANGED
│   └── monitor_workflow.py      # SIMPLIFIED: Remove retry logic
│
├── activities/
│   ├── rag.py                   # NEW: get_team_aliases, save_team_aliases
│   ├── twitter.py               # MODIFIED: Accept aliases, run 3 searches
│   └── download.py              # UNCHANGED
│
└── data/
    └── models.py                # Add TWITTER_ALIASES field
```

## Implementation Details

### 1. RAGWorkflow (`src/workflows/rag_workflow.py`)

```python
from dataclasses import dataclass
from datetime import timedelta
from temporalio import workflow
from temporalio.common import RetryPolicy

with workflow.unsafe.imports_passed_through():
    from src.activities import rag as rag_activities

@dataclass
class RAGWorkflowInput:
    fixture_id: int
    event_id: str
    team_name: str      # "Liverpool"
    player_name: str    # "Mohamed Salah"

@workflow.defn
class RAGWorkflow:
    """
    Resolve team aliases via RAG, then trigger Twitter search workflow.
    
    This workflow:
    1. Queries local LLM for team name variations
    2. Stores aliases in MongoDB for debugging
    3. Triggers TwitterWorkflow with resolved aliases
    """
    
    @workflow.run
    async def run(self, input: RAGWorkflowInput) -> dict:
        # Step 1: Get team aliases from RAG
        aliases = await workflow.execute_activity(
            rag_activities.get_team_aliases,
            input.team_name,
            start_to_close_timeout=timedelta(seconds=60),
            retry_policy=RetryPolicy(maximum_attempts=3),
        )
        
        # Step 2: Store aliases in MongoDB
        await workflow.execute_activity(
            rag_activities.save_team_aliases,
            args=[input.fixture_id, input.event_id, aliases],
            start_to_close_timeout=timedelta(seconds=10),
        )
        
        # Step 3: Trigger Twitter workflow with aliases
        from src.workflows.twitter_workflow import TwitterWorkflow, TwitterWorkflowInput
        
        await workflow.execute_child_workflow(
            TwitterWorkflow.run,
            TwitterWorkflowInput(
                fixture_id=input.fixture_id,
                event_id=input.event_id,
                player_name=input.player_name,
                team_aliases=aliases,
            ),
            id=f"twitter-{input.event_id}",
            task_queue="found-footy",
        )
        
        return {"status": "completed", "aliases": aliases}
```

### 2. RAG Activities (`src/activities/rag.py`)

```python
from temporalio import activity
from typing import List
import httpx

@activity.defn
async def get_team_aliases(team_name: str) -> List[str]:
    """
    Get team name aliases for Twitter search via local LLM.
    
    Examples:
        "Atletico de Madrid" → ["Atletico", "Atleti", "ATM"]
        "Manchester United"  → ["Man United", "Man Utd", "MUFC"]
        "Liverpool"          → ["Liverpool", "LFC", "Reds"]
        "Real Madrid"        → ["Real Madrid", "Real", "Madrid"]
    
    Args:
        team_name: Full team name from API
    
    Returns:
        List of 3 aliases for Twitter search
    """
    activity.logger.info(f"🔍 Getting aliases for team: {team_name}")
    
    # TODO: Replace stub with actual LLM call
    # See "Local LLM Integration" section below
    
    # STUB: Return team name 3x to test the flow
    aliases = [team_name, team_name, team_name]
    
    activity.logger.info(f"📋 Resolved aliases: {aliases}")
    return aliases


@activity.defn
async def save_team_aliases(fixture_id: int, event_id: str, aliases: List[str]) -> bool:
    """Save resolved aliases to event for debugging/visibility."""
    from src.data.mongo_store import FootyMongoStore
    from src.data.models import EventFields
    
    store = FootyMongoStore()
    
    result = store.fixtures_active.update_one(
        {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
        {"$set": {f"events.$.{EventFields.TWITTER_ALIASES}": aliases}}
    )
    
    return result.modified_count > 0
```

### 3. TwitterWorkflow Changes

The TwitterWorkflow will be restructured to:
- Accept `team_aliases: List[str]` in input
- Build 3 search queries per attempt (one per alias)
- Manage all 3 attempts internally with durable timers
- Mark `_twitter_complete` only after all attempts

```python
@dataclass
class TwitterWorkflowInput:
    fixture_id: int
    event_id: str
    player_name: str
    team_aliases: List[str]  # ["Liverpool", "LFC", "Reds"]

@workflow.defn
class TwitterWorkflow:
    @workflow.run
    async def run(self, input: TwitterWorkflowInput) -> dict:
        for attempt in range(1, 4):
            # Run 3 searches (one per alias)
            all_videos = []
            for alias in input.team_aliases:
                query = f"{input.player_name.split()[-1]} {alias}"
                result = await workflow.execute_activity(
                    execute_twitter_search,
                    args=[input.fixture_id, input.event_id, query],
                    ...
                )
                all_videos.extend(result.get("videos", []))
            
            # Dedupe and save
            unique = dedupe_by_url(all_videos)
            await save_twitter_results(...)
            
            # Download
            await workflow.execute_child_workflow(DownloadWorkflow.run, ...)
            
            # Wait 3 minutes (except after last attempt)
            if attempt < 3:
                wait = self._calculate_wait_to_next_3min_boundary()
                await workflow.sleep(timedelta(seconds=wait))
        
        # Mark complete after all attempts
        await mark_twitter_complete(...)
        return {"status": "completed", "attempts": 3}
```

### 4. Monitor Simplification

Remove from monitor:
- `_twitter_count` tracking
- Twitter retry logic
- `update_event_twitter_count` calls
- "additional searches" category

Monitor now just:
1. Debounces events (3 polls)
2. Triggers RAGWorkflow once when `_monitor_complete=true`
3. Checks `_twitter_complete` for fixture completion

### 5. New Event Fields

```python
class EventFields:
    # ... existing ...
    TWITTER_ALIASES = "_twitter_aliases"  # ["Liverpool", "LFC", "Reds"]
```

## Execution Timeline

```
T+0:00  Goal detected, _monitor_count=1
T+1:00  _monitor_count=2
T+2:00  _monitor_count=3, _monitor_complete=true
        → RAGWorkflow started
        → get_team_aliases("Liverpool") → ["Liverpool", "LFC", "Reds"]
        → TwitterWorkflow started

T+2:10  Twitter Attempt 1:
        → Search "Salah Liverpool" → 3 videos
        → Search "Salah LFC" → 2 videos (1 dup)
        → Search "Salah Reds" → 1 video (all dups)
        → Dedupe → 4 unique videos
        → Download → 3 uploaded to S3
        → Sleep until T+5:00

T+5:00  Twitter Attempt 2:
        → Same searches, 1 new video found
        → Download → 1 uploaded
        → Sleep until T+8:00

T+8:00  Twitter Attempt 3:
        → Same searches, 0 new videos
        → _twitter_complete=true

T+9:00  Monitor sees _twitter_complete=true
        → Fixture eligible for completion
```

---

# Local LLM Deployment on Strix Halo

## Hardware Specifications

| Component | Specification |
|-----------|---------------|
| **CPU** | AMD Ryzen AI Max+ 395 |
| | 16-core / 32-thread |
| | 3.0 GHz base, 5.1 GHz boost |
| | 64 MB L3 Cache |
| | 120W sustained, 140W boost |
| **GPU** | AMD Radeon 8060S (integrated) |
| | 40 Compute Units |
| | Up to 2.9 GHz |
| | 32 MB MALL Cache |
| **Unified Memory** | Shared CPU/GPU memory pool |

## ROCm + Ollama Stack

The recommended approach for running local LLMs on AMD hardware is **Ollama with ROCm**.

### Docker Compose Configuration

Add to `docker-compose.yml`:

```yaml
services:
  ollama:
    image: ollama/ollama:rocm
    container_name: found-footy-ollama
    ports:
      - "11434:11434"
    volumes:
      - ollama-models:/root/.ollama
    devices:
      - /dev/kfd
      - /dev/dri
    group_add:
      - video
      - render
    environment:
      - HSA_OVERRIDE_GFX_VERSION=11.0.0  # For RDNA 3.5
      - OLLAMA_NUM_PARALLEL=2
      - OLLAMA_MAX_LOADED_MODELS=1
    deploy:
      resources:
        limits:
          memory: 32G
    restart: unless-stopped

volumes:
  ollama-models:
    name: found-footy-ollama-models
```

### Host Setup (Ubuntu/Debian)

```bash
# 1. Install ROCm 6.x
wget https://repo.radeon.com/amdgpu-install/6.0/ubuntu/jammy/amdgpu-install_6.0.60000-1_all.deb
sudo dpkg -i amdgpu-install_6.0.60000-1_all.deb
sudo amdgpu-install --usecase=rocm,graphics --no-dkms

# 2. Add user to required groups
sudo usermod -aG video,render $USER

# 3. Verify GPU detection
rocminfo | grep "Name:"
# Should show: gfx1150 or similar for RDNA 3.5

# 4. Set environment (add to ~/.bashrc)
export HSA_OVERRIDE_GFX_VERSION=11.0.0
```

### Model Selection

For the team alias task, a small efficient model is sufficient:

| Model | Size | Speed | Recommendation |
|-------|------|-------|----------------|
| **Phi-3 Mini** | 3.8B | Fast | ✅ Best for this task |
| **Mistral 7B** | 7B | Medium | Good quality |
| **Llama 3.1 8B** | 8B | Medium | Highest quality |
| **Qwen2 7B** | 7B | Medium | Good multilingual |

```bash
# Pull model (inside container or host)
docker exec found-footy-ollama ollama pull phi3:mini

# Or for better quality
docker exec found-footy-ollama ollama pull mistral:7b-instruct-q4_K_M
```

### Integration with RAG Activity

```python
# src/activities/rag.py

import httpx
from temporalio import activity
from typing import List
import json

OLLAMA_URL = "http://found-footy-ollama:11434"

SYSTEM_PROMPT = """You are a football/soccer team name expert. Given a team's full name, 
return exactly 3 short aliases commonly used on Twitter/X to refer to this team.

Rules:
- Return ONLY a JSON array of 3 strings
- Aliases should be short (1-2 words max)
- Include common abbreviations, nicknames, or shortened names
- Order by popularity (most common first)

Examples:
- "Atletico de Madrid" → ["Atletico", "Atleti", "ATM"]
- "Manchester United" → ["Man United", "Man Utd", "MUFC"]
- "Borussia Dortmund" → ["Dortmund", "BVB", "Borussia"]
- "Paris Saint-Germain" → ["PSG", "Paris", "Paris SG"]
"""

@activity.defn
async def get_team_aliases(team_name: str) -> List[str]:
    """
    Get team name aliases via local Ollama LLM.
    
    Uses Phi-3 Mini or similar small model for fast inference.
    Falls back to [team_name] * 3 if LLM unavailable.
    """
    activity.logger.info(f"🔍 Getting aliases for team: {team_name}")
    
    try:
        async with httpx.AsyncClient(timeout=30.0) as client:
            response = await client.post(
                f"{OLLAMA_URL}/api/generate",
                json={
                    "model": "phi3:mini",
                    "prompt": f"Team: {team_name}\n\nReturn JSON array of 3 aliases:",
                    "system": SYSTEM_PROMPT,
                    "stream": False,
                    "options": {
                        "temperature": 0.3,  # Low temp for consistency
                        "num_predict": 50,   # Short response
                    }
                }
            )
            response.raise_for_status()
            
            result = response.json()
            text = result.get("response", "").strip()
            
            # Parse JSON array from response
            # Handle cases like: ["Atletico", "Atleti", "ATM"]
            # or wrapped in markdown: ```json\n[...]\n```
            if "```" in text:
                text = text.split("```")[1]
                if text.startswith("json"):
                    text = text[4:]
            
            aliases = json.loads(text.strip())
            
            if isinstance(aliases, list) and len(aliases) >= 3:
                aliases = [str(a).strip() for a in aliases[:3]]
                activity.logger.info(f"✅ LLM aliases: {aliases}")
                return aliases
            
    except Exception as e:
        activity.logger.warning(f"⚠️ LLM failed, using fallback: {e}")
    
    # Fallback: just use team name
    activity.logger.info(f"📋 Fallback aliases: [{team_name}] * 3")
    return [team_name, team_name, team_name]
```

### Performance Expectations

On Strix Halo with integrated Radeon 8060S:

| Model | Tokens/sec | Latency (3 aliases) |
|-------|------------|---------------------|
| Phi-3 Mini (3.8B) | ~40-60 t/s | 1-2 seconds |
| Mistral 7B Q4 | ~25-35 t/s | 2-4 seconds |
| Llama 3.1 8B Q4 | ~20-30 t/s | 3-5 seconds |

The unified memory architecture means models up to ~24GB can run without issue, but smaller quantized models (Q4_K_M) are recommended for speed.

### Memory Allocation

The integrated GPU shares system RAM. Recommended allocation:

```yaml
# docker-compose.yml
environment:
  - OLLAMA_NUM_PARALLEL=2      # Max concurrent requests
  - OLLAMA_MAX_LOADED_MODELS=1 # Keep 1 model in memory
```

For a 32GB system:
- OS + Apps: ~8GB
- Docker containers: ~4GB
- Ollama model: ~8GB (7B Q4)
- Headroom: ~12GB

### Testing the Integration

```bash
# 1. Start Ollama container
docker compose up -d ollama

# 2. Pull model
docker exec found-footy-ollama ollama pull phi3:mini

# 3. Test directly
curl http://localhost:11434/api/generate -d '{
  "model": "phi3:mini",
  "prompt": "Team: Liverpool FC\n\nReturn JSON array of 3 aliases:",
  "stream": false
}'

# Expected: {"response": "[\"Liverpool\", \"LFC\", \"Reds\"]", ...}
```

### Alternative: vLLM with ROCm

For higher throughput (multiple concurrent requests), consider vLLM:

```yaml
services:
  vllm:
    image: rocm/vllm:latest
    container_name: found-footy-vllm
    ports:
      - "8000:8000"
    devices:
      - /dev/kfd
      - /dev/dri
    environment:
      - HSA_OVERRIDE_GFX_VERSION=11.0.0
    command: >
      --model microsoft/Phi-3-mini-4k-instruct
      --dtype float16
      --max-model-len 4096
      --gpu-memory-utilization 0.8
```

vLLM provides an OpenAI-compatible API, making integration easier:

```python
# Using OpenAI client with vLLM
from openai import AsyncOpenAI

client = AsyncOpenAI(
    base_url="http://found-footy-vllm:8000/v1",
    api_key="not-needed"
)

response = await client.chat.completions.create(
    model="microsoft/Phi-3-mini-4k-instruct",
    messages=[
        {"role": "system", "content": SYSTEM_PROMPT},
        {"role": "user", "content": f"Team: {team_name}"}
    ],
    temperature=0.3,
    max_tokens=50
)
```

---

## Migration Checklist

- [ ] Create `src/workflows/rag_workflow.py`
- [ ] Create `src/activities/rag.py` with stub implementation
- [ ] Add `TWITTER_ALIASES` to `EventFields` in models.py
- [ ] Add `update_event_aliases` method to `FootyMongoStore`
- [ ] Restructure `TwitterWorkflow` for self-managed retries
- [ ] Simplify `MonitorWorkflow` to remove twitter retry logic
- [ ] Update `debounce_events` activity to trigger RAGWorkflow
- [ ] Register new workflow and activities in worker.py
- [ ] Test with stub (returns team name 3x)
- [ ] Add Ollama service to docker-compose
- [ ] Implement actual LLM call in `get_team_aliases`
- [ ] Test end-to-end with real fixtures

## Open Questions

1. **Caching**: Should we cache team aliases? (Same team appears in multiple fixtures)
2. **Fallback strategy**: If LLM gives bad output, what's the fallback?
3. **Multilingual**: Some team names are in Spanish/Italian - should LLM handle translation?
4. **Rate limiting**: Max concurrent Ollama requests?
