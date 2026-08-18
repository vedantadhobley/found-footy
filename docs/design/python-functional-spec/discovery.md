# Python discovery behavior

Frozen legacy behavior from the [Python functional-spec index](./README.md).

## 5. Twitter Search / Discovery Behavior

### Trigger

Spawned by **MonitorWorkflow** as a fire-and-forget child workflow
when:
- `len(_monitor_workflows) >= 3` AND `_monitor_complete == false`
- WorkflowID: `twitter-{event_id}` (stable, enables dedup)
- Parent close policy: **ABANDON** (Twitter continues even if
  Monitor dies)
- ID reuse policy: **REJECT_DUPLICATE** (Temporal server enforces
  one TwitterWorkflow per event)

**File:** `archive/src/workflows/monitor_workflow.py:184-220`

### Alias Resolution (Cache vs. RAG)

TwitterWorkflow's first step is to resolve team aliases:

1. **Try cache lookup** (~30s timeout):
   - Query `team_aliases.find_one({_id: team_id})`
   - If found: use `twitter_aliases` array, proceed to search

2. **Cache miss:** Run full RAG pipeline (~30-90s):
   - Fetch Wikidata SPARQL query for team
   - Extract aliases, preprocess to single words
   - Call LLM (Qwen3-VL on joi) to select best Twitter words
   - Merge with team name + nationality adjectives
   - Save to `team_aliases` collection

3. **RAG fails:** Fallback to `[team_name]` (single word), log
   warning, continue

**File:** `archive/src/workflows/twitter_workflow.py:163-230`

### Search Cascade (15 Attempts, 10 Registrations Target)

TwitterWorkflow uses a **WHILE loop** (not a fixed FOR loop):

```
attempt = 0
while attempt < 15:  // Max 15 attempts
  attempt += 1

  download_count = get_download_workflow_count()
  if download_count >= 10:
    break  // Got 10 downloads, done!

  existing_urls = get_existing_video_urls()  // Dedup exclusion

  query = build_search_query(player_names, team_aliases)  // OR logic
  videos = execute_twitter_search(query, existing_urls, max_age=3min)

  save_discovered_videos(videos)

  download_workflow_id = f"download{attempt}-..."
  spawn_download_workflow(videos)  // Fire-and-forget

  wait(60 seconds)  // START-to-START spacing
```

**Key behaviors:**
- Exits when **10 DownloadWorkflows have registered** (count tracked
  via `_download_workflows` array)
- Safety cap: **15 max attempts** (prevents infinite loops)
- Spacing: **60 seconds between attempts** (START-to-START, so slow
  searches don't push later attempts late)
- **Always spawns DownloadWorkflow**, even with 0 videos
  (DownloadWorkflow registers itself regardless, so failed searches
  still increment the count)
- **Fire-and-forget**: TwitterWorkflow doesn't wait for download to
  complete; moves to next attempt immediately

**File:** `archive/src/workflows/twitter_workflow.py:249-517`

### Search Query Construction

For each attempt, TwitterWorkflow builds a query with **OR
operators**:

```
player_or = "Florian"  // or "(Florian OR Wirtz)" if multiple player names
team_or = "LFC"        // or "(LFC OR Liverpool OR Reds)" if multiple aliases

query = f"{player_or} {team_or}"
// Examples:
// "Florian (LFC OR Liverpool)"
// "(Florian OR Wirtz) Liverpool"
// "Mohamed Salah (LFC OR Reds OR Anfield)"
```

**File:** `archive/src/workflows/twitter_workflow.py:365-384`

### Twitter Search Mechanism (Browser Automation)

TwitterWorkflow calls `execute_twitter_search(query, existing_urls,
max_age_minutes=3)`, which:

1. **POSTs to Firefox automation service** (Selenium-based browser
   automation, separate Docker container)
2. **Selects tweets** matching the query from Twitter's "Latest" tab
3. **Extracts video metadata** (URL, duration, view count, likes,
   retweets)
4. **Chronological early-exit:** Stops scrolling when finding a
   tweet older than `max_age_minutes` (default 3 min)
5. **Deduplication by URL:** Skips videos already in `existing_urls`
6. **Adaptive early-exit:** If 2 consecutive scroll iterations
   return 0 new videos, stop scrolling

Returns **ALL videos found** (not limited at query time), sorted by
duration descending. TwitterWorkflow then picks top **5 longest
videos** to pass to DownloadWorkflow.

**Only video-bearing tweets are persisted.** Non-video tweets are
discarded on-the-fly during scrolling. There is no `_seen_tweets`
array — only `_discovered_videos`.

**File:** `archive/src/activities/twitter.py:243-350`

### Event Existence Check (VAR Termination)

At the start of each attempt, TwitterWorkflow checks if the event
still exists:

```
if not check_event_exists(fixture_id, event_id):
  log "Event deleted (VAR'd) - terminating early"
  return {...terminated_early: True...}
```

**File:** `archive/src/workflows/twitter_workflow.py:289-325`

### Monitor Complete Flag

TwitterWorkflow sets `_monitor_complete = true` **at the VERY
START** (before alias lookup, search, or anything else). This proves
to the system that TwitterWorkflow actually started running (not
just attempted to spawn). If TwitterWorkflow never runs (Temporal
issue), `_monitor_complete` stays false, and MonitorWorkflow will
retry spawning it.

**File:** `archive/src/workflows/twitter_workflow.py:143-161`

### Video Persistence (Discovered Videos)

TwitterWorkflow **saves ONLY the videos it's actually attempting to
download** to `_discovered_videos`:

```
save_discovered_videos(fixture_id, event_id, videos_to_download)
```

Videos found in earlier attempts are **not re-searched** (via
`get_twitter_search_data()`), but videos skipped due to
`max_videos_per_attempt = 5` limit are **not saved** (and may be
searched again in later attempts).

This is a **pragmatic tradeoff**: keeps video metadata bounded while
ensuring we don't re-search known videos.

**File:** `archive/src/workflows/twitter_workflow.py:448-469`

### Completed State

TwitterWorkflow **terminates when:**
1. **Download count reaches 10** (all 10 DownloadWorkflows have
   registered), OR
2. **Max attempts reached (15)**, OR
3. **Event was deleted** (VAR check failed)

It **does NOT set `_download_complete`** — that's set by
UploadWorkflow (or DownloadWorkflow on exit as failsafe, see §6).

Returns summary dict with total videos found, download count, exit
reason.

**File:** `archive/src/workflows/twitter_workflow.py:519-560`

---
