# RAG Implementation - Future LLM Integration

## Overview

This document describes **how to implement the actual LLM-based team alias lookup** in the RAGWorkflow. The workflow infrastructure is already in place with a stub implementation.

**Current State**: `get_team_aliases(team_name)` returns `[team_name]` (stub)  
**Future State**: Query local LLM (Ollama) for intelligent aliases

---

## What's Already Implemented

The workflow infrastructure is complete:

| Component | Status | Location |
|-----------|--------|----------|
| `RAGWorkflow` | ✅ Done | `src/workflows/rag_workflow.py` |
| `RAGWorkflowInput` dataclass | ✅ Done | `src/workflows/rag_workflow.py` |
| `get_team_aliases` activity | ✅ Stub | `src/activities/rag.py` |
| `save_team_aliases` activity | ✅ Done | `src/activities/rag.py` |
| `_twitter_aliases` field | ✅ Done | `src/data/models.py` |
| Worker registration | ✅ Done | `src/worker.py` |

**All that remains is replacing the stub in `get_team_aliases` with an actual LLM call.**

---

## Target Hardware: Strix Halo

| Component | Specification |
|-----------|---------------|
| **CPU** | AMD Ryzen AI Max+ 395 (16-core/32-thread) |
| **GPU** | AMD Radeon 8060S (integrated, 40 CUs) |
| **Memory** | Unified CPU/GPU memory pool |

The integrated GPU shares system RAM, making it ideal for running quantized LLMs.

---

## Recommended Stack: Ollama + ROCm

### Docker Compose Addition

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

### Host Setup (Ubuntu)

```bash
# 1. Install ROCm 6.x
wget https://repo.radeon.com/amdgpu-install/6.0/ubuntu/jammy/amdgpu-install_6.0.60000-1_all.deb
sudo dpkg -i amdgpu-install_6.0.60000-1_all.deb
sudo amdgpu-install --usecase=rocm,graphics --no-dkms

# 2. Add user to required groups
sudo usermod -aG video,render $USER

# 3. Verify GPU detection
rocminfo | grep "Name:"

# 4. Set environment
export HSA_OVERRIDE_GFX_VERSION=11.0.0
```

### Model Selection

For team alias lookup, a small model is sufficient:

| Model | Size | Speed | Notes |
|-------|------|-------|-------|
| **Phi-3 Mini** | 3.8B | ~50 t/s | ✅ Recommended - fast, good quality |
| Mistral 7B Q4 | 7B | ~30 t/s | Higher quality, slower |
| Qwen2 7B | 7B | ~30 t/s | Good multilingual support |

```bash
# Pull model
docker exec found-footy-ollama ollama pull phi3:mini
```

---

## Implementation: Replace the Stub

Update `src/activities/rag.py`:

```python
"""
RAG Activities - Team Alias Lookup

Activities for the RAGWorkflow.
"""
from temporalio import activity
from typing import List
import httpx
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
- "Liverpool" → ["Liverpool", "LFC", "Reds"]
"""


@activity.defn
async def get_team_aliases(team_name: str) -> List[str]:
    """
    Get team name aliases via local Ollama LLM.
    
    Uses Phi-3 Mini for fast inference (~1-2 seconds).
    Falls back to [team_name] if LLM unavailable.
    
    Args:
        team_name: Full team name from API (e.g., "Liverpool")
    
    Returns:
        List of 3 aliases for Twitter search
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
            # Handle markdown wrapping: ```json\n[...]\n```
            if "```" in text:
                text = text.split("```")[1]
                if text.startswith("json"):
                    text = text[4:]
            
            # Find the JSON array in the response
            start = text.find("[")
            end = text.rfind("]") + 1
            if start >= 0 and end > start:
                text = text[start:end]
            
            aliases = json.loads(text.strip())
            
            if isinstance(aliases, list) and len(aliases) >= 3:
                aliases = [str(a).strip() for a in aliases[:3]]
                activity.logger.info(f"✅ LLM aliases: {aliases}")
                return aliases
            
            activity.logger.warning(f"⚠️ LLM returned invalid format: {text}")
            
    except httpx.ConnectError:
        activity.logger.warning("⚠️ Ollama not available, using fallback")
    except json.JSONDecodeError as e:
        activity.logger.warning(f"⚠️ Failed to parse LLM response: {e}")
    except Exception as e:
        activity.logger.warning(f"⚠️ LLM error: {e}")
    
    # Fallback: just use team name
    activity.logger.info(f"📋 Fallback aliases: [{team_name}]")
    return [team_name]


@activity.defn
async def save_team_aliases(fixture_id: int, event_id: str, aliases: List[str]) -> bool:
    """Save resolved aliases to event for debugging/visibility."""
    from src.data.mongo_store import FootyMongoStore
    from src.data.models import EventFields
    
    activity.logger.info(f"💾 Saving aliases for {event_id}: {aliases}")
    
    store = FootyMongoStore()
    
    try:
        result = store.fixtures_active.update_one(
            {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
            {"$set": {f"events.$.{EventFields.TWITTER_ALIASES}": aliases}}
        )
        return result.modified_count > 0
    except Exception as e:
        activity.logger.error(f"❌ Failed to save aliases: {e}")
        return False
```

---

## Performance Expectations

On Strix Halo with integrated Radeon 8060S:

| Model | Tokens/sec | Latency (3 aliases) |
|-------|------------|---------------------|
| Phi-3 Mini (3.8B) | ~40-60 t/s | 1-2 seconds |
| Mistral 7B Q4 | ~25-35 t/s | 2-4 seconds |
| Llama 3.1 8B Q4 | ~20-30 t/s | 3-5 seconds |

The unified memory architecture allows models up to ~24GB without issue.

---

## Testing

### 1. Start Ollama

```bash
docker compose up -d ollama
docker exec found-footy-ollama ollama pull phi3:mini
```

### 2. Test Directly

```bash
curl http://localhost:11434/api/generate -d '{
  "model": "phi3:mini",
  "prompt": "Team: Liverpool FC\n\nReturn JSON array of 3 aliases:",
  "system": "Return ONLY a JSON array of 3 team aliases.",
  "stream": false
}'
```

Expected:
```json
{"response": "[\"Liverpool\", \"LFC\", \"Reds\"]", ...}
```

### 3. Test via Activity

```python
# In Python REPL inside worker container
import asyncio
from src.activities.rag import get_team_aliases

async def test():
    aliases = await get_team_aliases("Atletico de Madrid")
    print(aliases)  # ["Atletico", "Atleti", "ATM"]

asyncio.run(test())
```

---

## Alternative: vLLM

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

vLLM provides an OpenAI-compatible API:

```python
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

## Open Questions

1. **Caching**: Should we cache aliases? Same team appears in multiple fixtures.
   - Option: Store in MongoDB `team_aliases` collection
   - Option: In-memory LRU cache in activity

2. **Multilingual**: Some team names are Spanish/Italian/German.
   - Phi-3 handles this well
   - Consider explicit language detection

3. **Fallback Priority**: When LLM returns bad output:
   - Currently: `[team_name]` (single alias)
   - Option: Parse partial results
   - Option: Secondary model

4. **Rate Limiting**: Max concurrent Ollama requests:
   - `OLLAMA_NUM_PARALLEL=2` limits concurrent inference
   - Activity timeout (30s) handles slow responses

---

## Migration Checklist

- [ ] Add `ollama` service to `docker-compose.yml`
- [ ] Set up ROCm on host (if not already)
- [ ] Pull model: `ollama pull phi3:mini`
- [ ] Replace stub in `src/activities/rag.py`
- [ ] Add `httpx` to `requirements.txt`
- [ ] Test with real fixture
- [ ] Consider caching strategy
