# Team Alias Generation with RAG (Wikidata + LLM)

## Overview

This document describes the RAG-based team alias generation system for improving Twitter search coverage.

**Current State**: ✅ **FULLY IMPLEMENTED** - `get_team_aliases(team_id, team_name)` performs true RAG

> **Note**: This document was written during design/development. The actual implementation in `src/activities/rag.py` uses:
> - Model: `Qwen3-VL-8B` via llama.cpp server (configurable via `LLAMA_URL` env var)
> - Vision: Qwen3-VL-8B also validates video content in `download.py`
> - Team type detection: API-Football `/teams?id={id}` returns `team.national` boolean
> - Pre-caching: IngestWorkflow pre-caches aliases for both teams during fixture ingestion
> 
> See the implementation for authoritative details.

### What is RAG?

**R**etrieval-**A**ugmented **G**eneration - the only approach we use:

1. **Retrieve**: Fetch all known aliases from Wikidata API (search for QID, then fetch entity data)
2. **Augment**: Include those aliases as context in the LLM prompt
3. **Generate**: LLM selects/derives the BEST aliases (up to 10) optimized for Twitter search

We don't trust the model's training data alone. RAG ensures we have authoritative, curated alias data from Wikidata, with the LLM acting as an intelligent filter/deriver.

### Why RAG?

Wikidata for Atlético Madrid (Q8701) returns 46+ aliases:
```
"Club Atlético de Madrid, SAD", "Atletico Madrid", "Club Atlético de Madrid", 
"Atletico", "El Atleti", "ATM Football Club", "Atletico of Madrid", 
"Atleti de Madrid", "ATM", "Atl. de Madrid", "The Atletico", "Atlético", 
"AtléticoDeMadrid", "Atlético Madrid"...
```

**Problem**: Many are bad for Twitter search (too long, formal names, articles like "El")

**Solution**: LLM processes these WITH the context that it's for Twitter search:
```
Input: 46 Wikidata aliases + "optimize for Twitter search"
Output: ["ATM", "Atletico", "Madrid", "Atleti", "Colchoneros"]  ← Short, from Wikidata list
```

**Twitter Search**: Uses OR syntax to combine all aliases in a single search:
```
"Griezmann (ATM OR Atletico OR Madrid OR Atleti OR Colchoneros)"
```

**Key constraint**: The LLM can ONLY select from the provided Wikidata list. It cannot hallucinate aliases. The code validates LLM output against the Wikidata list.

Special characters (é, ü, ñ) are handled by **post-processing normalization**, not the LLM.

### Endpoints

The LLM is accessed via an external llama.cpp server on the shared network:

| Environment | URL | Model |
|-------------|-----|-------|
| Dev | `http://llama-chat:8080` (via `luv-dev` network) | Qwen3-VL-8B |
| Prod | `http://llama-chat:8080` (via `luv-prod` network) | Qwen3-VL-8B |

The llama.cpp server runs as a shared service (not per-project) and provides an OpenAI-compatible API.

---

## Implementation Status

| Component | Status | Location |
|-----------|--------|----------|
| `RAGWorkflow` | ✅ Done | `src/workflows/rag_workflow.py` |
| `RAGWorkflowInput` dataclass | ✅ Done | `src/workflows/rag_workflow.py` |
| `get_team_aliases` activity | ✅ **IMPLEMENTED** | `src/activities/rag.py` |
| `save_team_aliases` activity | ✅ Done | `src/activities/rag.py` |
| Wikidata QID search | ✅ Done | `_search_wikidata_qid()` |
| Wikidata alias fetch | ✅ Done | `_fetch_wikidata_aliases()` |
| LLM validation | ✅ Done | Validates LLM output against Wikidata |
| Heuristic fallback | ✅ Done | `_select_best_wikidata_aliases()` |
| MongoDB caching | ✅ Done | By `team_id` as `_id` |
| Diacritics normalization | ✅ Done | `_normalize_alias()` |
| Worker registration | ✅ Done | `src/worker.py` |
| LLM service | ✅ Running | llama.cpp with Qwen3-VL-8B |
| Vision validation | ✅ Done | `validate_video_is_soccer()` in download.py |

**Optional enhancements**:
- Remove hardcoded nicknames from `team_data.py` (now redundant)

---

## Target Hardware: Framework Desktop (Strix Halo)

| Component | Specification |
|-----------|---------------|
| **APU** | AMD Ryzen AI Max+ 395 (16-core/32-thread, gfx1151 GPU) |
| **GPU** | AMD Radeon 8060S (integrated, 40 CUs RDNA 3.5) |
| **Memory** | 128GB LPDDR5x-8000 unified memory (~215 GB/s bandwidth) |

---

## LLM Server Setup

The LLM is provided by an external llama.cpp server running on the shared Docker network. Found-footy does not run its own LLM container.

### Connection Configuration

The worker connects to the llama.cpp server via environment variable:

```yaml
# docker-compose.yml
worker:
  environment:
    LLAMA_URL: http://llama-chat:8080  # External llama.cpp server
  networks:
    - found-footy-prod
    - luv-prod  # Shared network with llama-chat
```

### API Compatibility

llama.cpp provides an OpenAI-compatible API:

```bash
# Test from host
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "messages": [{"role": "user", "content": "Hello"}],
    "max_tokens": 100
  }'
```

### Current Model

| Model | Size | Features |
|-------|------|----------|
| **Qwen3-VL-8B F16** | ~17GB | Vision + Text, used for both RAG alias selection and video validation |

### Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| **External server** | Shared across multiple projects, single model loaded |
| **llama.cpp** (not Ollama) | More control, simpler, no model management overhead |
| **Qwen3-VL** | Vision-capable for video validation, also excellent for text |
| **F16 quantization** | Good quality, fits in 128GB unified memory |
| **OpenAI API format** | Standard interface, easy to switch models |

---

## Model Details

The **Qwen3-VL-8B** model handles both text (RAG alias selection) and vision (video validation).

### Text Generation (RAG)

For alias selection, use the `/no_think` suffix to skip chain-of-thought reasoning:

```python
prompt = f'''Words: {json.dumps(preprocessed)}

Select the best words for Twitter search. Return a JSON array. /no_think'''

response = await client.post(
    f'{LLAMA_URL}/v1/chat/completions',
    json={
        'messages': [
            {'role': 'system', 'content': SYSTEM_PROMPT},
            {'role': 'user', 'content': prompt}
        ],
        'max_tokens': 100,
        'temperature': 0.1
    }
)
```

### Vision (Video Validation)

For validating soccer content, send image as base64 in OpenAI multimodal format:

```python
response = await client.post(
    f'{LLAMA_URL}/v1/chat/completions',
    json={
        'messages': [{
            'role': 'user',
            'content': [
                {'type': 'text', 'text': 'Is this a soccer/football match? Answer YES or NO only.'},
                {'type': 'image_url', 'image_url': {'url': f'data:image/jpeg;base64,{image_b64}'}}
            ]
        }],
        'max_tokens': 10,
        'temperature': 0.1
    }
)
```

### Health Check

```bash
# Check if server is running
curl http://localhost:8080/health

# Or check with a simple completion
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"messages": [{"role": "user", "content": "hi"}], "max_tokens": 10}'
```

---

## MongoDB Schema: `team_aliases` Collection

Team aliases are cached using **team_id as `_id`** for O(1) lookups.

### Document Schema

```javascript
{
  _id: 541,                          // API-Football team_id (fast lookup)
  team_name: "Atletico Madrid",      // Original name from API (for display)
  national: false,                   // True if national team, False if club
  country: "Spain",                  // Country from API-Football teams endpoint
  city: "Madrid",                    // City from venue data (for Wikidata search)
  wikidata_qid: "Q8701",             // Wikidata entity ID
  wikidata_aliases: [                // All aliases from Wikidata (preserved)
    "Club Atlético de Madrid, SAD",
    "El Atleti",
    "Atlético Madrid",
    // ...
  ],
  twitter_aliases: [                 // LLM-derived, normalized (for search)
    "Atletico",
    "Atleti", 
    "ATM"
  ],
  model: "Qwen3-VL-8B",              // Model used (for debugging/retraining)
  created_at: ISODate("..."),
  updated_at: ISODate("...")
}
```

### Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| `_id` = team_id | Fastest possible lookup, no secondary index needed |
| `team_name` preserved | Frontend displays original name from API |
| `country`/`city` stored | Used for Wikidata disambiguation (e.g., "Newcastle" → "Newcastle upon Tyne, England" → Q18716 Newcastle United, not Q18485 Newcastle Town) |
| `wikidata_aliases` preserved | Audit trail, can re-derive if LLM improves |
| `twitter_aliases` normalized | Special chars removed (é→e), ready for Twitter search |
| `wikidata_qid` stored | Can refresh from Wikidata if aliases update |

### Index (Auto-created)

MongoDB automatically indexes `_id`, so no additional index is needed for lookups.

```javascript
// No index needed - _id is already indexed
// Lookup: db.team_aliases.findOne({_id: 541})  // O(1)
```

---

## Implementation: Replace the Stub

Replace `src/activities/rag.py` with:

```python
"""
RAG Activities - Team Alias Lookup with Wikidata + LLM

Flow:
1. Check MongoDB cache (team_aliases collection by team_id)
2. If miss:
   a. Fetch aliases from Wikidata
   b. LLM derives best 3 for Twitter
   c. Normalize (remove diacritics: é→e, ü→u, ñ→n)
   d. Cache result
3. Return normalized aliases for Twitter search

The original team name from API is preserved for frontend display.
"""
from temporalio import activity
from typing import List, Optional
import httpx
import json
import os
import unicodedata
from datetime import datetime, timezone


# Environment-aware URL (set in docker-compose)
LLAMA_URL = os.getenv("LLAMA_URL", "http://llama-chat:8080")

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
- "Real Madrid" → ["Real Madrid", "Madrid", "Real"]
- "Bayern Munich" → ["Bayern", "Bayern Munich", "FCB"]
"""


def _get_cached_aliases(store, team_id: int) -> Optional[dict]:
    """Check MongoDB cache for existing aliases by team_id."""
    return store.db.team_aliases.find_one({"_id": team_id})


def _cache_aliases(
    store, 
    team_id: int, 
    team_name: str,
    raw_aliases: List[str],
    twitter_aliases: List[str], 
    model: str,
    wikidata_qid: Optional[str] = None
) -> None:
    """Store aliases in MongoDB cache using team_id as _id."""
    store.db.team_aliases.update_one(
        {"_id": team_id},
        {
            "$set": {
                "team_name": team_name,
                "wikidata_qid": wikidata_qid,
                "raw_aliases": raw_aliases,
                "twitter_aliases": twitter_aliases,
                "model": model,
                "updated_at": datetime.now(timezone.utc)
            },
            "$setOnInsert": {
                "created_at": datetime.now(timezone.utc)
            }
        },
        upsert=True
    )


def _normalize_alias(alias: str) -> str:
    """
    Normalize alias for Twitter search by removing diacritics.
    
    Examples:
        "Atlético" → "Atletico"
        "München" → "Munchen"
        "Señor" → "Senor"
    """
    # NFD decomposes characters (é → e + combining accent)
    # Then we strip the combining characters (category 'Mn')
    normalized = unicodedata.normalize('NFD', alias)
    return ''.join(c for c in normalized if unicodedata.category(c) != 'Mn')


def _parse_llm_response(text: str) -> Optional[List[str]]:
    """Parse JSON array from LLM response, handling markdown wrapping."""
    # Handle markdown wrapping: ```json\n[...]\n```
    if "```" in text:
        parts = text.split("```")
        for part in parts:
            part = part.strip()
            if part.startswith("json"):
                text = part[4:].strip()
                break
            elif part.startswith("["):
                text = part
                break
    
    # Find the JSON array
    start = text.find("[")
    end = text.rfind("]") + 1
    if start >= 0 and end > start:
        text = text[start:end]
    
    try:
        aliases = json.loads(text.strip())
        if isinstance(aliases, list) and len(aliases) >= 3:
            return [str(a).strip() for a in aliases[:3]]
    except json.JSONDecodeError:
        pass
    
    return None


def _derive_fallback_aliases(team_name: str) -> List[str]:
    """
    Deterministic fallback when LLM unavailable.
    
    Derives aliases from team name:
    - Full name
    - First word (if multi-word)
    - Initials (if 2+ words)
    """
    words = team_name.split()
    aliases = [team_name]
    
    if len(words) > 1:
        aliases.append(words[0])
        initials = "".join(w[0].upper() for w in words if w[0].isalpha())
        if len(initials) >= 2:
            aliases.append(initials)
    
    while len(aliases) < 3:
        aliases.append(team_name)
    
    return aliases[:3]


@activity.defn
async def get_team_aliases(team_id: int, team_name: str) -> List[str]:
    """
    Get team name aliases via RAG (Wikidata + LLM) with MongoDB caching.
    
    Flow:
    1. Check cache by team_id (O(1) lookup)
    2. If miss:
       a. Fetch aliases from Wikidata
       b. LLM derives best 3 for Twitter
       c. Normalize (remove diacritics)
       d. Cache result
    3. Return normalized aliases for Twitter search
    
    Args:
        team_id: API-Football team ID (used as cache key)
        team_name: Full team name from API (for display/fallback)
    
    Returns:
        List of 3 normalized aliases for Twitter search
    """
    from src.data.mongo_store import FootyMongoStore
    
    activity.logger.info(f"🔍 Getting aliases for team {team_id}: {team_name}")
    
    store = FootyMongoStore()
    
    # 1. Check cache by team_id
    cached = _get_cached_aliases(store, team_id)
    if cached:
        aliases = cached.get("twitter_aliases", [])
        activity.logger.info(f"📦 Cache hit: {aliases}")
        return aliases
    
    activity.logger.info(f"🔄 Cache miss, fetching from Wikidata + LLM...")
    
    # 2a. Fetch from Wikidata (TODO: implement wikidata lookup by team_id mapping)
    wikidata_qid = None  # Will be looked up from team_id → QID mapping
    raw_aliases = []     # Will be fetched from Wikidata
    
    # 2b. Query LLM with Wikidata context
    try:
        async with httpx.AsyncClient(timeout=30.0) as client:
            # Build RAG prompt with Wikidata aliases as context
            if raw_aliases:
                prompt = f"""Team: {team_name}

Known aliases from Wikidata:
{chr(10).join(f'- {alias}' for alias in raw_aliases)}

Derive or select the 3 best aliases for Twitter search. Return JSON array only."""
            else:
                # Fallback if no Wikidata data yet
                prompt = f"Team: {team_name}\n\nReturn JSON array of 3 aliases:"
            
            response = await client.post(
                f"{OLLAMA_URL}/api/generate",
                json={
                    "model": OLLAMA_MODEL,
                    "prompt": prompt,
                    "system": SYSTEM_PROMPT,
                    "stream": False,
                    "options": {
                        "temperature": 0.3,
                        "num_predict": 50,
                    }
                }
            )
            response.raise_for_status()
            
            result = response.json()
            text = result.get("response", "").strip()
            
            llm_aliases = _parse_llm_response(text)
            
            if llm_aliases:
                # 2c. Normalize aliases (remove diacritics)
                twitter_aliases = [_normalize_alias(a) for a in llm_aliases]
                
                activity.logger.info(f"✅ LLM: {llm_aliases} → Normalized: {twitter_aliases}")
                
                # 2d. Cache for future use
                _cache_aliases(
                    store, 
                    team_id=team_id,
                    team_name=team_name,
                    raw_aliases=raw_aliases,
                    twitter_aliases=twitter_aliases,
                    model=OLLAMA_MODEL,
                    wikidata_qid=wikidata_qid
                )
                return twitter_aliases
            
            activity.logger.warning(f"⚠️ LLM returned invalid format: {text}")
            
    except httpx.ConnectError:
        activity.logger.warning("⚠️ Ollama not available, using fallback")
    except Exception as e:
        activity.logger.warning(f"⚠️ LLM error: {e}")
    
    # 3. Fallback (normalize but don't cache - let RAG try again later)
    fallback = _derive_fallback_aliases(team_name)
    fallback = [_normalize_alias(a) for a in fallback]
    activity.logger.info(f"📋 Fallback aliases: {fallback}")
    return fallback


@activity.defn
async def save_team_aliases(fixture_id: int, event_id: str, aliases: List[str]) -> bool:
    """
    Save resolved aliases to event in MongoDB.
    
    Stores in _twitter_aliases field for debugging/visibility.
    """
    from src.data.mongo_store import FootyMongoStore
    from src.data.models import EventFields
    
    activity.logger.info(f"💾 Saving aliases for {event_id}: {aliases}")
    
    store = FootyMongoStore()
    
    try:
        result = store.fixtures_active.update_one(
            {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
            {"$set": {f"events.$.{EventFields.TWITTER_ALIASES}": aliases}}
        )
        
        if result.modified_count > 0:
            activity.logger.info(f"✅ Saved aliases for {event_id}")
            return True
        else:
            activity.logger.warning(f"⚠️ No document modified for {event_id}")
            return False
    except Exception as e:
        activity.logger.error(f"❌ Failed to save aliases: {e}")
        return False
```

---

## Replacing Hardcoded Nicknames

### Current State (`team_data.py`)

```python
TOP_UEFA = {
    541: {"name": "Real Madrid", "nickname": "Madrid"},  # ← hardcoded
    40: {"name": "Liverpool", "nickname": "Liverpool"},  # ← hardcoded
    # ...
}
```

Used by `get_team_nickname()` → `build_twitter_search()` → returns ONE nickname.

### New State

1. **Remove `nickname` field** from `team_data.py` (or keep for reference only)
2. **Always use `get_team_aliases()`** which returns 3 aliases via LLM + cache
3. **TwitterWorkflow already handles multiple aliases** - it loops through the list

### Current State (Implemented)

`team_data.py` now uses **dynamic team tracking**:
- Fetches all teams from top 5 European leagues via API-Football
- Caches team IDs in MongoDB (refreshed every 24 hours)
- Falls back to legacy `TOP_UEFA_IDS` if API fails
- National teams (`TOP_FIFA_IDS`) are still statically defined

---

## Testing

> **Note**: The following testing instructions assume the llama.cpp server is running as an external service.

### 1. Verify LLM Server is Running

```bash
# Check health
curl http://localhost:8080/health

# Or check with a simple completion
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"messages": [{"role": "user", "content": "hi"}], "max_tokens": 10}'
```

### 2. Test Alias Selection

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "messages": [
      {"role": "system", "content": "Select the best Twitter search terms from the provided words. Return a JSON array of 3-5 terms."},
      {"role": "user", "content": "Words: [\"Liverpool\", \"LFC\", \"Reds\", \"Anfield\", \"YNWA\"]\n\nSelect best. /no_think"}
    ],
    "max_tokens": 100,
    "temperature": 0.1
  }'
```

Expected response:
```json
{"choices": [{"message": {"content": "[\"LFC\", \"Reds\", \"Liverpool\"]"}}]}
```

### 3. Test Vision (Video Validation)

```bash
# Extract a frame from a video first
ffmpeg -i video.mp4 -vframes 1 -f image2pipe -vcodec png - | base64 > frame.b64

# Then test vision
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d "{
    \"messages\": [{
      \"role\": \"user\",
      \"content\": [
        {\"type\": \"text\", \"text\": \"Is this a soccer/football match? Answer YES or NO.\"},
        {\"type\": \"image_url\", \"image_url\": {\"url\": \"data:image/png;base64,$(cat frame.b64)\"}}
      ]
    }],
    \"max_tokens\": 10
  }"
```

### 4. Test via Activity in Container

```python
# In Python REPL inside worker container
import asyncio
from src.activities.rag import get_team_aliases

async def test():
    # First call - cache miss, hits LLM (~1-2s)
    aliases = await get_team_aliases("Atletico de Madrid")
    print(f"First call: {aliases}")
    
    # Second call - cache hit (instant)
    aliases = await get_team_aliases("Atletico de Madrid")
    print(f"Second call: {aliases}")

asyncio.run(test())
```

### 4. Verify Cache

```bash
# Connect to Mongoku at localhost:4201 (dev) or 3201 (prod)
# Or via mongosh:
docker exec found-footy-dev-mongo mongosh found_footy -u ffuser -p ffpass --authenticationDatabase admin --eval "db.llm_team_aliases.find().pretty()"
```

---

## Phase 2: True RAG (Wikidata + LLM)

Phase 2 implements actual RAG - retrieving Wikidata aliases and using the LLM to intelligently filter them for Twitter search.

### Wikidata Sources

- https://www.wikidata.org/wiki/Q8701 (Atlético Madrid)
- https://www.wikidata.org/wiki/Q15789 (Bayern Munich)

Each team has a unique QID, and the Wikidata API returns structured alias data in JSON.

### Wikidata API

Fetch any team's aliases with:

```bash
# Get Atlético Madrid (Q8701) - aliases in English
curl "https://www.wikidata.org/wiki/Special:EntityData/Q8701.json" | jq '.entities.Q8701.aliases.en[].value'
```

Response contains `aliases.en` array:
```json
{
  "entities": {
    "Q8701": {
      "aliases": {
        "en": [
          {"value": "Club Atlético de Madrid, SAD"},
          {"value": "Atletico Madrid"},
          {"value": "Club Atlético de Madrid"},
          {"value": "Atletico"},
          {"value": "El Atleti"},
          {"value": "ATM Football Club"},
          {"value": "Atletico of Madrid"},
          {"value": "Atleti de Madrid"},
          {"value": "ATM"},
          {"value": "Atl. de Madrid"},
          {"value": "The Atletico"},
          {"value": "Atlético"},
          {"value": "AtléticoDeMadrid"},
          {"value": "Atlético Madrid"}
        ]
      }
    }
  }
}
```

### RAG Architecture (Phase 2)

```
┌─────────────────────────────────────────────────────────────────┐
│                        get_team_aliases()                        │
└───────────────────────────────┬─────────────────────────────────┘
                                │
                                ▼
                    ┌───────────────────────┐
                    │   Check MongoDB Cache  │
                    │  (llm_team_aliases)    │
                    └───────────┬───────────┘
                                │ miss
                                ▼
                    ┌───────────────────────┐
                    │  Lookup QID for team  │  ← Map API team_id → Wikidata QID
                    │  (team_wikidata_map)  │
                    └───────────┬───────────┘
                                │
                                ▼
                    ┌───────────────────────┐
                    │   RETRIEVE: Fetch     │  ← Wikidata REST API
                    │   aliases from Wiki   │     returns 10-20 aliases
                    └───────────┬───────────┘
                                │
                                ▼
                    ┌───────────────────────┐
                    │  AUGMENT: Build prompt│  ← Include all Wikidata aliases
                    │  with retrieved data  │     as context for LLM
                    └───────────┬───────────┘
                                │
                                ▼
                    ┌───────────────────────┐
                    │  GENERATE: LLM picks  │  ← Ollama selects best 3
                    │  best 3 for Twitter   │     for Twitter search
                    └───────────┬───────────┘
                                │
                                ▼
                    ┌───────────────────────┐
                    │    Cache in MongoDB   │
                    └───────────────────────┘
```

### New Collection: `team_wikidata_map`

Maps API-Football team IDs to Wikidata QIDs:

```json
{
  "_id": ObjectId("..."),
  "team_id": 530,
  "team_name": "Atlético de Madrid",
  "qid": "Q8701",
  "verified": true,
  "created_at": ISODate("2025-12-23T10:00:00Z")
}
```

This mapping can be:
1. Manually curated for top teams
2. Auto-discovered via Wikidata SPARQL (search by team name + instance_of:football_club)

### RAG Implementation (Phase 2)

```python
async def fetch_wikidata_aliases(qid: str) -> List[str]:
    """
    RETRIEVE: Fetch all aliases from Wikidata API.
    
    Args:
        qid: Wikidata entity ID (e.g., "Q8701" for Atlético Madrid)
    
    Returns:
        List of ALL aliases from Wikidata (unfiltered)
    """
    url = f"https://www.wikidata.org/wiki/Special:EntityData/{qid}.json"
    
    async with httpx.AsyncClient(timeout=10.0) as client:
        response = await client.get(url)
        response.raise_for_status()
        data = response.json()
    
    entity = data.get("entities", {}).get(qid, {})
    aliases_data = entity.get("aliases", {}).get("en", [])
    
    # Return ALL aliases - let LLM filter
    return [a["value"] for a in aliases_data]


# RAG System Prompt - tells LLM what we need
RAG_SYSTEM_PROMPT = """You are a Twitter search optimization expert for football/soccer.

Given a list of known aliases for a team (from Wikidata), derive or select the 3 BEST aliases for Twitter search.

You can DERIVE aliases by simplifying existing ones:
- Drop articles: "El Atleti" → "Atleti", "The Reds" → "Reds"
- Drop prefixes: "FC Bayern" → "Bayern", "Real Madrid CF" → "Real Madrid"
- Use common shortenings fans actually type

Selection criteria:
1. SHORT: Prefer 1-2 words (Twitter users don't type long names)
2. COMMON: Prefer what fans actually tweet (not official legal names)
3. UNIQUE: Each alias should catch different tweets

Special characters (é, ü, ñ) are fine - they will be normalized later.

Return ONLY a JSON array of exactly 3 strings. No explanation."""


async def get_team_aliases_rag(team_name: str, wikidata_aliases: List[str]) -> List[str]:
    """
    AUGMENT + GENERATE: Use LLM to pick best aliases from Wikidata data.
    
    Args:
        team_name: Team name from API
        wikidata_aliases: All aliases retrieved from Wikidata
    
    Returns:
        List of 3 best aliases for Twitter search
    """
    # AUGMENT: Build prompt with retrieved Wikidata data
    prompt = f"""Team: {team_name}

Known aliases from Wikidata:
{chr(10).join(f'- {alias}' for alias in wikidata_aliases)}

Derive or select the 3 best aliases for Twitter search. You may simplify these (drop articles, prefixes). Return JSON array only."""

    # GENERATE: LLM selects best 3
    async with httpx.AsyncClient(timeout=30.0) as client:
        response = await client.post(
            f"{OLLAMA_URL}/api/generate",
            json={
                "model": OLLAMA_MODEL,
                "prompt": prompt,
                "system": RAG_SYSTEM_PROMPT,
                "stream": False,
                "options": {"temperature": 0.2, "num_predict": 50}
            }
        )
        response.raise_for_status()
        result = response.json()
        
    # Parse LLM response
    aliases = _parse_llm_response(result.get("response", ""))
    return aliases if aliases else wikidata_aliases[:3]
```

### Example RAG Flow

**Input**: Team "Atlético de Madrid", QID "Q8701"

**Step 1 - RETRIEVE** (Wikidata):
```
["Club Atlético de Madrid, SAD", "Atletico Madrid", "Club Atlético de Madrid",
 "Atletico", "El Atleti", "ATM Football Club", "Atletico of Madrid", 
 "Atleti de Madrid", "ATM", "Atl. de Madrid", "The Atletico", "Atlético",
 "AtléticoDeMadrid", "Atlético Madrid"]
```

**Step 2 - AUGMENT** (Prompt):
```
Team: Atlético de Madrid

Known aliases from Wikidata:
- Club Atlético de Madrid, SAD
- Atletico Madrid
- Club Atlético de Madrid
- Atletico
- El Atleti
- ATM Football Club
- Atletico of Madrid
- Atleti de Madrid
- ATM
- Atl. de Madrid
- The Atletico
- Atlético
- AtléticoDeMadrid
- Atlético Madrid

Select the 3 best aliases for Twitter search. Return JSON array only.
```

**Step 3 - GENERATE** (LLM output):
```json
["Atletico", "Atleti", "ATM"]
```

### Why RAG > Either Alone

| Approach | Problem |
|----------|---------|
| **Wikidata only** | Returns 14 aliases including "Club Atlético de Madrid, SAD" - too many, many bad for Twitter |
| **LLM only** | May hallucinate aliases, miss lesser-known teams, training data cutoff |
| **RAG (both)** | Wikidata provides accurate data, LLM intelligently filters for use case |

---

## Host Setup: Vulkan for AMD GPU (Ubuntu 24.04 LTS)

Ollama uses **Vulkan** (not ROCm) for GPU access. This guide is for **Ubuntu 24.04 LTS** (Noble Numbat).

### Strix Halo Support: Already Covered ✅

**Good news**: Ubuntu 24.04 already has Mesa 25.0.7 via updates. Strix Halo (gfx1151) support was added in Mesa 24.2, so you're well covered.

```bash
# Check your Mesa version
apt-cache policy mesa-vulkan-drivers
# Should show: 25.0.7-0ubuntu0.24.04.2 or similar
```

**No PPA needed** - the stock Ubuntu 24.04 packages support Strix Halo out of the box.

### Step 1: Install Vulkan Drivers

```bash
# Mesa Vulkan driver for AMD (RADV) - already in Ubuntu 24.04
sudo apt update
sudo apt install -y mesa-vulkan-drivers vulkan-tools
```

### Step 2: Add User to Video/Render Groups

```bash
# Required for /dev/dri access
sudo usermod -aG video $USER
sudo usermod -aG render $USER

# Log out and back in (or reboot)
```

### Step 3: Verify GPU Access

```bash
# Check Vulkan is working
vulkaninfo | head -30

# Should show something like:
# GPU0:
#   apiVersion     = 1.3.xxx
#   driverVersion  = xx.x.x
#   vendorID       = 0x1002 (AMD)
#   deviceName     = AMD Radeon...

# Check /dev/dri exists
ls -la /dev/dri/
# Should show: card0, renderD128
```

### Step 4: Verify Docker GPU Access

```bash
# Test that docker can see the GPU (need --entrypoint to override ollama entrypoint)
docker run --rm --device /dev/dri:/dev/dri --entrypoint /bin/sh ollama/ollama:latest -c "ls -la /dev/dri"
```

### Step 5: Test Ollama GPU Detection

```bash
# Start the stack
docker compose -f docker-compose.dev.yml up -d ollama

# Check logs for Vulkan detection
docker logs found-footy-dev-ollama 2>&1 | grep -i "vulkan\|gpu"

# Pull a model and run
docker exec found-footy-dev-ollama ollama pull phi3:mini
docker exec found-footy-dev-ollama ollama run phi3:mini "Hello"
```

### Troubleshooting

**"GPU not detected" in Ollama logs:**
```bash
# Verify /dev/dri is mounted
docker exec found-footy-dev-ollama ls -la /dev/dri/

# Check if it's a permissions issue
# Your user must be in video/render groups
groups $USER
```

**"vulkaninfo: command not found":**
```bash
sudo apt install vulkan-tools
```

**"No Vulkan devices found":**
```bash
# Check if kernel supports your GPU
dmesg | grep -i amdgpu

# May need newer kernel for Strix Halo
uname -r  # Should be 6.8+ for Strix Halo
```

### Strix Halo Specific Notes

The Framework Desktop with Strix Halo (gfx1151) requires:
- Linux kernel **6.12+** for full support (6.8+ minimum)
- Mesa **24.3+** for RADV Vulkan

If on Ubuntu LTS, add the kisak mesa PPA:
```bash
sudo add-apt-repository ppa:kisak/kisak-mesa
sudo apt update && sudo apt upgrade
```

---

## Setup Checklist

### Per Environment (Dev/Prod)

- [ ] `docker compose up -d` (creates volumes automatically)
- [ ] Pull model: `docker exec found-footy-{env}-ollama ollama pull phi3:mini`
- [ ] Create index: `db.llm_team_aliases.createIndex({ "team_name": 1 }, { unique: true })`
- [ ] Replace stub in `src/activities/rag.py`
- [ ] Test with curl
- [ ] Test with real fixture

### Code Changes

- [ ] Add `httpx` to `requirements.txt` (if not present)
- [ ] Update `src/activities/rag.py` with implementation above
- [ ] (Optional) Remove hardcoded nicknames from `team_data.py`

---

## Resources

- **Ollama API Docs**: https://github.com/ollama/ollama/blob/main/docs/api.md
- **Strix Halo Testing**: https://github.com/lhl/strix-halo-testing (benchmark data)
- **Framework Desktop Wiki**: https://strixhalo-homelab.d7.wtf/AI/AI-Capabilities-Overview
