# Design Audit — 2026-06-30

A holistic look at what found-footy is, what it does well, what it does
clumsily, and which clumsy parts are missing a *primitive* (vs which are
just legacy inertia or solved-but-undeployed). Companion to:

- [`audit.md`](./audit.md) — file-level correctness audit (May 2026,
  ~50 findings still mostly open). Asks "what's wrong at this line?"
- [`roadmap.md`](./roadmap.md) — the committed 7-phase rewrite plan.
  Asks "what do we ship this quarter?"

This audit asks **"is this the right shape?"** It's opinionated, and the
opinions are gated by lived evidence (incident references included), but
they're opinions, not findings. The point is to have something to argue
with before the rewrite phases run.

The recommendation at the end ([§16](#16-implementation-order))
reorganizes the roadmap given what this audit surfaces — it doesn't
replace it.

---

## 0. What's already working — don't touch (yet)

The fastest way to ruin a deep-pass rewrite is to "fix" the parts that
are already load-bearing-correct. Catalog of stuff that has earned its
keep:

- **5-collection (now 6) MongoDB split-of-concerns** (`fixtures_staging`,
  `fixtures_live`, `fixtures_active`, `fixtures_completed`,
  `team_aliases`, `top_flight_cache`). The `fixtures_live` overwrite
  buffer in particular is genuinely clever — a stable "snapshot of raw
  API state" we can diff against our enhanced `fixtures_active` without
  risking overwriting enhancement fields. Don't collapse this without a
  concrete win.
- **Workflow-ID-as-`$addToSet` idempotent counter.** Mongo array of
  workflow IDs + `len(array) >= threshold` check, instead of a counter
  field. Idempotent under Temporal retries and concurrent monitor
  cycles. This pattern is right.
- **Scoped dedup by `timestamp_verified`.** Verified clips only compare
  against verified S3 videos; unverified against unverified. Prevents
  the "Goal 1 clip got replaced by a Goal 2 clip with similar broadcast
  framing" failure mode that surfaced in prod. Keep.
- **Per-event `UploadWorkflow` serialization via `signal-with-start`.**
  One workflow per event with deterministic ID `upload-{event_id}`, fed
  by FIFO signals from concurrent DownloadWorkflows. Serializes S3
  dedup correctly without locks. The *primitive* is right; see [§4](#4-dedup-strategy-end-to-end)
  for whether the *scope* of serialization (per-event) is too narrow.
- **Fire-and-forget child workflows with `ABANDON` parent close.** The
  short-cycle MonitorWorkflow can't block on 10-min Twitter workflows;
  ABANDON makes lifecycle work without leaking workflow handles. Right.
- **The multi-stage download filter shape.** Cheap filters first (URL,
  duration, dimension), then medium (aspect ratio, MD5), then expensive
  (AI vision, perceptual hash). Recently tightened
  (aspect ratio `1.75-1.82` on 2026-06-30; snowflake-ID length
  validation in P2b). Shape is right; values are tunable.
- **Caddy-based hostname routing for HTTP services.** The 2026-05
  migration from host ports to `proxy` Caddy was right — see
  [`decisions.md`](./decisions.md). Don't revisit.
- **joi via Caddy hostname (`http://llama-small.joi`).** Stable across
  model swaps and port reassignments. Right.

Everything below this point is what's clumsy.

---

## 1. The build–deploy gap: prod ≠ main

**This is the load-bearing one.** Everything else in this audit assumes
"prod runs main." On 2026-06-30 we discovered prod was running images
~7 weeks behind main — Phase 1 telemetry (`ef16d2f`) and Phase 2a
cause-logging (`65164f1`) were committed but never deployed, and every
"why didn't this work" investigation since had been looking at code prod
wasn't executing.

**Current state.** Manual `docker compose build` + `up -d`. No CI, no
automated deploy, no signal of drift. The scaler manages replica counts
but is blind to image versions. Image rebuilds happen when a human
remembers.

**Lived problem.** Phase 1 (2026-05-22), Phase 2a (2026-05-22), and the
`events.py` drop-workflow fix (2026-05-31) all sat dirty / undeployed
until 2026-06-30. The Netherlands v Morocco zero-video match would have
been diagnosable in 2 minutes via the typed-error logs; instead it took
45 minutes of Loki archaeology plus a shell into the container to figure
out the running code didn't have the fix at all.

**The missing primitive.** A "prod is N commits behind main" signal,
plus a deploy hook trustworthy enough that humans don't have to think
about it.

**Proposed alternative.**

1. Worker startup banner emits a structured log line with `image_tag`,
   `git_sha`, `built_at`. Promtail ships it to Loki. A saved Grafana
   panel shows "currently-running commit SHA on prod workers vs main
   HEAD."
2. A `bin/deploy` script (or `make deploy`) that does
   `git pull && docker compose build worker twitter && docker compose up -d --no-deps --no-build`
   for prod, dev, or both. Same script invocable manually or from CI.
3. A GitHub Actions (later: self-hosted Forgejo-actions per the
   migration plan) workflow on push-to-main that posts a build-ready
   event to a webhook, which triggers (2) on the luv host. No registry
   needed — luv builds locally from its own checkout.

**Scope sketch.** M. Banner + script are S each; the poll-or-webhook
trigger is the M. Gating: webhook secret storage (lives in `.env`),
handling in-flight TwitterWorkflows during a worker recreate (verified
last night: Temporal replays them on the new worker).

**Why this is section 1.** Nothing else matters if prod silently
diverges from main again. The deploy-tracking panel should ship *before*
any other rewrite work begins — otherwise we'll re-litigate "is prod
actually running this fix?" every session.

---

## 2. Workflow ID conventions and identity

**Current design.** Three patterns coexist:

| Workflow         | ID pattern                                       | Stable? |
| ---------------- | ------------------------------------------------ | ------- |
| TwitterWorkflow  | `twitter-{event_id}`                             | yes (Sprint 1 stabilized) |
| DownloadWorkflow | `download{N}-{team}-{player}-{event_id}`         | **no** — `team` and `player` come from API and can mutate |
| UploadWorkflow   | `upload-{event_id}`                              | yes |
| RAGWorkflow      | `rag-{team_id}`                                  | yes |

**Lived problem.** When the API reassigns the goal scorer mid-match
(assist-to-goal flip, own-goal re-attribution), attempts 8+ get
different `team`/`player` slugs than 1-7. REJECT_DUPLICATE doesn't
catch this — different IDs aren't duplicates by ID. The Temporal UI
shows inconsistent names side-by-side that confuse anyone trying to
follow a single event's history. This is also the cosmetic complaint
from the 2026-06-30 conversation.

**The missing primitive.** A stamping convention that ALWAYS uses
`event_id` + stage + attempt-sequence, never API-mutable fields. The
constraint: *the workflow ID must be derivable from the event_id and a
stage-local counter alone.*

**Proposed alternative.**

```
twitter-{event_id}
download-{attempt:02d}-{event_id}     # was: download{N}-{team}-{player}-{event_id}
upload-{event_id}
rag-{team_id}                         # already stable
```

`event_id` itself encodes `{fixture}_{team}_{player}_{type}_{seq}` so it
carries human-readable identity. Temporal UI search finds workflows by
`event_id` substring regardless of stage.

**Scope sketch.** S. One line in
`src/workflows/twitter_workflow.py:473`. Rollout: ship the new format,
accept that in-flight TwitterWorkflows during the cutover might rarely
re-spawn a DownloadWorkflow that's already running with the old format
(REJECT_DUPLICATE doesn't catch cross-format dupes). Cheap, rare,
permanent fix.

**Bigger question (don't action).** Is `_event_id` (the Mongo key) the
same identity as the workflow-lineage root? Currently parallel
namespaces. Could collapse. Probably not worth it — the parallelism is
fine and Mongo-key concerns (uniqueness, indexing, querying) differ from
workflow-lifecycle concerns.

---

## 3. Data model — Mongo discipline, typing, identity

**The constraint settled before this audit started.** You committed to
Mongo + the 5/6-collection split + embedded events / videos arrays
because the hot path runs `$addToSet` + nested-document updates every
30 s, and Mongo handles those natively in ways Postgres's JSONB
columns wouldn't match without losing the document-model ergonomics.
That decision stays — the question isn't "what store" but "how do we
get *discipline* out of Mongo so we stop paying for its permissiveness
at write time."

**Current access patterns — Mongo is fitted for these.**

| Pattern                                         | Frequency | Mongo today                          |
| ----------------------------------------------- | --------- | ------------------------------------ |
| Find fixture by ID                              | constant  | trivial                              |
| Find events of a fixture                        | constant  | trivial (embedded)                   |
| Find videos of an event                         | constant  | trivial (embedded)                   |
| Append workflow ID to event's tracking array    | constant  | `$addToSet` atomic                   |
| Cross-fixture: "recent goals by player X"       | rare      | aggregate pipeline                   |
| Cross-fixture: "stats over fixtures by league"  | rare      | aggregate pipeline                   |

The hot path is fully native; cold reporting is awkward but not a
bottleneck. Verdict on Mongo-vs-Postgres: **keep Mongo**, not as a
compromise but because the embedded model genuinely fits this workload.

**Lived problems — Mongo's permissiveness as ambient cost.**

1. **No schema validation, no field-name guard.** Mongo accepts
   whatever shape we write. The MD5-dedup silent miss in
   `upload.py:352` (`s3_key` vs `_s3_key`) would have been a startup
   error with a JSON Schema validator; instead it cost weeks of
   undetected dedup drift.
2. **Event-ID uniqueness via string concatenation, race-prone.**
   `_event_id = f"{fixture}_{team}_{player}_{type}_{seq}"` — the
   sequence number is bookkept in app code. Two MonitorWorkflows can
   both detect "Goal by player 234, no prior" within the same poll
   cycle, both compute `seq=1`, both write `_event_id = "5000_40_234_Goal_1"`.
   First wins; second silently overwrites. Rare but real, and there's
   no constraint to catch it.
3. **Event-ID is also workflow-ID, also Mongo key, also log key, also
   `_telemetry` partition key.** One string serves five purposes. When
   the API re-attributes the scorer mid-match (the `feature/event-matching`
   branch's whole problem), the event_id either drifts (breaking
   everything that referenced the old one) or stays stale (breaking
   the new-attribution case).
4. **`event._twitter_aliases` denormalized snapshot.** It's a
   point-in-time copy of `team_aliases.{team_id}.twitter_aliases` taken
   when the TwitterWorkflow spawned. Intentional (don't re-resolve mid-
   search), but with no constraint or "snapshot_at" timestamp, you can't
   tell whether the stale snapshot is *by design* or *corruption*.
5. **47 `except Exception: return [] / False / None` patterns** in
   `mongo_store.py` history (per May `audit.md`). Phase 1 typed errors
   landed at the activity boundary; haven't pushed into the data
   layer. Swallowed Mongo errors are still indistinguishable from
   "no data" to callers.
6. **Six collections, zero declared invariants.** What fields are
   required on a fixture document? Which are nullable mid-lifecycle?
   What's the valid set of `fixture.status.short` values? The answer
   to all of these lives in scattered code paths and reader memory.
7. **No write-time typing.** Activities pass `dict`s into
   `mongo_store` methods. Pydantic v1 was used early in some places
   then abandoned. Renaming a field is a global grep, not a type
   error.

**The missing primitive.** Three pillars, none of which exists today:
(a) a write-time **typed Python model layer** so dicts at the
mongo-store boundary become type errors at import; (b) **MongoDB-side
schema validators** so anything that slips past (a) is rejected at the
storage layer; (c) **identity discipline** so `_event_id` stops being a
string-concat that races and starts being a stable handle.

**Proposed methodology — three pillars + identity migration.**

### Pillar A: Pydantic models as the write-time contract

A new `src/data/models/` package owns the canonical types. Activities
construct models, pass them to `mongo_store` methods that accept
typed inputs only. Field renames become type errors at the `import`
boundary.

```python
# src/data/models/event.py
from datetime import datetime
from typing import Literal
from pydantic import BaseModel, ConfigDict, Field

EventType = Literal["Goal", "Card", "subst", "Var"]

class Player(BaseModel):
    id: int | None
    name: str | None  # null until API identifies scorer

class Team(BaseModel):
    id: int
    name: str

class EventTime(BaseModel):
    elapsed: int          # match minute reported by API
    extra: int | None     # +N stoppage time

class Telemetry(BaseModel):
    """Phase 1 per-event telemetry, see §7."""
    search_attempts: int = 0
    videos_discovered: int = 0
    videos_downloaded: int = 0
    download_failure_classes: dict[str, int] = Field(default_factory=dict)
    validation_pass_rate: float | None = None
    primary_failure_class: str | None = None
    time_to_first_s3_seconds: float | None = None

class Event(BaseModel):
    """An API-reported event with our enhancement fields layered on."""
    model_config = ConfigDict(populate_by_name=True)

    # Identity — see Pillar C below
    event_id: str = Field(alias="_event_id")
    event_natural_key: str | None = Field(default=None, alias="_event_natural_key")

    # API fields (mutable; refreshed each poll)
    type: EventType
    detail: str
    player: Player | None
    team: Team
    time: EventTime

    # Our enhancement fields (prefixed _ in Mongo)
    first_seen: datetime = Field(alias="_first_seen")
    monitor_workflows: list[str] = Field(default_factory=list, alias="_monitor_workflows")
    monitor_complete: bool = Field(default=False, alias="_monitor_complete")
    twitter_aliases: list[str] = Field(default_factory=list, alias="_twitter_aliases")
    twitter_aliases_snapshot_at: datetime | None = Field(default=None, alias="_twitter_aliases_snapshot_at")
    discovered_videos: list[DiscoveredVideo] = Field(default_factory=list, alias="_discovered_videos")
    download_workflows: list[str] = Field(default_factory=list, alias="_download_workflows")
    download_complete: bool = Field(default=False, alias="_download_complete")
    s3_videos: list[str] = Field(default_factory=list, alias="_s3_videos")  # share IDs (see §4)
    telemetry: Telemetry | None = Field(default=None, alias="_telemetry")
    removed: bool = Field(default=False, alias="_removed")
```

Note that the snapshot-at addition on `twitter_aliases` (problem 4) is
purely a documentation aid surfaced *by the type* — present means
"intentional snapshot," absent means "not yet resolved." Discrepancy
between snapshot and current team_aliases is then visibly intentional.

### Pillar B: MongoDB-side JSON Schema validators

For every collection, declare a JSON Schema validator at creation time
(or via `collMod` on existing collections). Mongo enforces shape at
write — anything that slips past Pydantic still gets rejected at the
storage boundary.

```javascript
db.runCommand({
  collMod: "fixtures_active",
  validator: {
    $jsonSchema: {
      bsonType: "object",
      required: ["_id", "fixture", "teams", "league", "_activated_at", "events"],
      properties: {
        _id: { bsonType: "int" },
        fixture: {
          bsonType: "object",
          required: ["id", "date", "status"],
          properties: {
            status: {
              bsonType: "object",
              required: ["short"],
              properties: {
                short: { enum: ["NS","TBD","1H","HT","2H","ET","BT","P","LIVE","SUSP","INT","PST"] },
                elapsed: { bsonType: ["int","null"] },
                extra: { bsonType: ["int","null"] }
              }
            }
          }
        },
        _activated_at: { bsonType: "date" },
        events: {
          bsonType: "array",
          items: {
            bsonType: "object",
            required: ["_event_id", "type", "_first_seen"],
            properties: {
              _event_id: { bsonType: "string", pattern: "^(e_[a-f0-9]{12}|\\d+_\\d+_\\d+_[A-Za-z]+_\\d+)$" },
              type: { enum: ["Goal","Card","subst","Var"] },
              _monitor_workflows: { bsonType: "array", items: { bsonType: "string" } },
              _telemetry: { bsonType: ["object","null"] }
            }
          }
        }
      }
    }
  },
  validationLevel: "strict",
  validationAction: "warn"   // ← "warn" during migration, then "error" once stable
})
```

The `_event_id` pattern accepts BOTH the legacy `5000_40_234_Goal_1`
shape and the new `e_<12-hex>` UUID shape during migration —
backward-compatible read, forward-compatible write.
`validationAction: "warn"` logs violations but accepts the write
through; flip to `"error"` once the warning rate is zero (i.e., all
write paths are emitting valid shapes). The `validator` itself stays
declarative; it's data, not code.

### Pillar C: UUID-based `_event_id` with a natural-key sidecar

Stop concatenating strings. Mint a short-prefix UUID at first
detection, store the original natural key as a sidecar for display
and substring queries.

```python
import uuid

def mint_event_id() -> str:
    """e_<12-hex> — collision-proof, fixed-length, sortable enough."""
    return f"e_{uuid.uuid4().hex[:12]}"

# When MonitorWorkflow detects a new event:
new_event = Event(
    event_id=mint_event_id(),
    event_natural_key=f"{fixture_id}_{team_id}_{player_id}_{event_type}_{seq}",
    type=event_type,
    player=player,
    team=team,
    time=time,
    first_seen=datetime.now(tz=UTC),
)
```

The sequence-race risk (problem 2) goes away: two MonitorWorkflows
detecting "Goal by player 234" concurrently mint two different UUIDs,
both insert, both visible. Mongo deduplicates via a unique-index on
`(_id, events._event_natural_key)` if you want strict one-event-per-
natural-key semantics, or you accept both and let the 3-poll debounce
collapse them at the natural-key level. (Recommend the latter — the
race window is small enough that the debounce naturally handles it.)

**Workflow ID conventions update.** From [§2](#2-workflow-id-conventions-and-identity):
- `twitter-{event_id}` → `twitter-e_a1b2c3d4e5f6`
- `download-{NN}-{event_id}` → `download-03-e_a1b2c3d4e5f6`
- `upload-{event_id}` → `upload-e_a1b2c3d4e5f6`

Lose the human-readable substring in workflow IDs, gain stability.
Temporal UI search can still find by natural-key via the
`event_natural_key` sidecar exposed in the workflow's input arguments
(searchable as `Attribute.eventNaturalKey="5000_40_234_Goal_1"`).

**`_event_id` migration mechanics.** This is the part that's
non-trivial — every existing reference must stay readable:

| Reference site                                 | Handling                                       |
| ---------------------------------------------- | ---------------------------------------------- |
| Mongo `events[].event_id` (string)             | Accept both formats via pattern in validator   |
| Workflow IDs in flight at deploy time          | Stay valid (Temporal doesn't reject by format) |
| `_monitor_workflows[]` / `_download_workflows[]` arrays containing string-format event IDs | Stay readable (`$addToSet` doesn't care) |
| Loki / Grafana queries that filter by event_id substring | Add `event_natural_key` as a parallel filter; keep both working |
| `mongo_store` query methods using event_id     | Methods accept either format (Pydantic-side coercion or transparent) |
| `fixtures_completed` historical documents       | Never rewritten — stay in legacy format forever |

The `event_natural_key` sidecar is the "the human still wants this" handle.
Logs continue to show "Goal by Szoboszlai at 23′" via the natural key;
the workflow lineage tracks via UUID.

### Pillar D: Typed errors at the data layer

Push the Phase 1 typed-error discipline down into `mongo_store`. Today's
`except Exception: return None` patterns get replaced with typed-error
raises:

```python
# src/utils/errors.py — extend the Phase 1 taxonomy
class MongoConflictError(FootyError):     # DuplicateKeyError, etc.
    pass

class MongoTransientError(FootyError):    # NotPrimaryError, network, etc. — retry-eligible
    retry_eligible = True

class MongoPermanentError(FootyError):    # validator rejection, malformed query
    retry_eligible = False
```

Callers can now reason about the error: transient → retry; permanent →
log + raise; conflict → handle (e.g. fall back to read-after-write).

### Migration path

1. **Land `src/data/models/` package.** Pydantic types for all
   existing collection shapes. No enforcement; activities can keep
   passing dicts. (S)
2. **Add JSON Schema validators to all six collections via
   `collMod`, in `validationAction: "warn"` mode.** Log violations,
   accept writes. Watch Loki for warnings. (S × 6 = M)
3. **Gradually convert `mongo_store` methods to accept Pydantic
   models, then *only* Pydantic models.** The acceptance-of-dicts
   removal is the brittle moment — gate on "Loki shows zero validator
   warnings for 1 week" before flipping each collection's method set
   to typed-only. (M)
4. **Land UUID minting for new events, sidecar natural key.**
   `_event_id` writes the UUID; reads accept both formats. Workflow ID
   construction switches to UUID format. (M)
5. **Update Loki dashboards / saved queries to handle both event_id
   formats.** (S)
6. **Flip validators from `warn` to `error`** once the warning rate
   has been zero for 1 week. (S)
7. **Push typed errors into `mongo_store`.** Replace each
   `except Exception` with classified raises. (M)
8. **Historical `fixtures_completed` documents stay in legacy
   format.** Read-side code accepts both indefinitely. No backfill
   mutation needed. (S, ongoing read-compat)

Each step is independently shippable and reversible.

### What this unlocks downstream

- **[§4](#4) `video_assets` / `video_shares` schemas** get typed from
  day one — every field is declared in `src/data/models/`, validated
  at write.
- **[§7](#7) error recovery** gets richer error classes to act on
  (`MongoConflictError` → retry-with-lookup, etc.).
- **[§11](#11) FastAPI response models** can reuse the same Pydantic
  types — no duplication between API DTOs and storage models.
- **[§12](#12) testing** gets free generators (Pydantic models →
  factories → synthetic fixture lifecycle harness).
- **[§13](#13) code organization** gets the typed domain models the
  per-domain bundles are organized around.

### Sub-piece scope table

| Sub-piece                                                 | Size |
| --------------------------------------------------------- | ---- |
| `src/data/models/` package (all collection types)         | S    |
| JSON Schema validators × 6 collections (`warn` mode)      | M    |
| Pydantic-only acceptance at `mongo_store` write boundary  | M    |
| UUID `_event_id` minting + sidecar                        | M    |
| Workflow ID convention update (§2 lands here too)         | S    |
| Loki / Grafana update for dual event_id formats           | S    |
| Validator flip from `warn` to `error`                     | S    |
| Typed-Mongo-errors layered on Phase 1                     | S    |

Total **L** when sequenced as one phase; **M** if you do the
discipline pillars (A + B + D) in one pass and defer Pillar C (UUID)
to a follow-up. Recommend doing all four together — Pillar C makes
the workflow ID work in [§2](#2) clean instead of layered, and the
typed models are far more useful when they enforce identity discipline
than when they're just structural.

This is the highest-leverage *foundational* deepening because every
other rewrite phase reuses its outputs. Run it after [§1](#1) (deploy
gate so we're confident "main is prod") and before [§4](#4) (dedup
re-arch) and [§13](#13) (code re-org).

---

## 4. Dedup strategy end-to-end

**Current design.** Per-event scoped dedup (verified vs unverified
pools), parallelized via `asyncio.gather`. MD5 batch dedup for exact
matches. Perceptual hash with 16-bit Hamming threshold, 3-consecutive-
frame requirement at offset-tolerant comparison. UploadWorkflow
serialized per-event via `signal-with-start`. S3-match loop in
`upload.py:614` `break`s at the first matching S3 video instead of
collapsing all matches.

**The constraint that drove the current design — load-bearing, not a
bug.** Video URLs leak. Every `_s3_videos[i].s3_url` we write to Mongo
can be:

- Surfaced to vedanta.systems via the API
- Embedded in OpenGraph cards by og-server
- Shared externally by users (tweet / Slack / DM)
- Bookmarked, indexed, linked from third-party sites
- Cached by CDNs and OG-card servers

Once a URL is out there, deleting the underlying S3 object breaks it.
The `break`-at-first-match in `upload.py:614` is a defense against
exactly this: when a duplicate arrives, we keep both S3 objects alive
so neither URL goes 404. The current proposal in
`docs/proposals/dedup-unification.md` introduces a `_video_redirects`
field as a band-aid, which acknowledges the constraint but doesn't
restructure around it.

So the real question isn't "how do we collapse duplicates" (easy) — it's
"how do we get URL stability *and* clean dedup at the same time." Those
are in tension only because S3 URLs are doubling as share identifiers.
Decouple them and the tension dissolves.

**Lived problems — what the current hack actually costs.**

1. **Storage bloat.** Same content lives at N S3 keys; new uploads pile
   up forever. Mongo `_s3_videos` array grows with redundant entries.
2. **Popularity-vote dilution.** "How many times has this clip been
   discovered?" is a counter on whichever S3 object happened to be in
   the array first. If a viewer is looking at the second upload, the
   popularity vote on the canonical first one is invisible to them. The
   rankings users see don't reflect the actual aggregate signal.
3. **Ranking inconsistency.** `recalculate_video_ranks` sorts each
   event's `_s3_videos` independently. Two events showing the same
   physical clip can end up with different ranks based on different
   discovery orders. There's no canonical "this is the rank of this
   clip" because there's no canonical "this clip" — only the per-event
   copies.
4. **No cross-event dedup.** Goals scored ≤2 min apart by the same
   team often share celebration / replay footage. We treat them as
   isolated dedup namespaces; the same byte-identical clip gets
   uploaded twice (once per event).
5. **Cross-instance dedup not expressible.** Per-event UploadWorkflow
   serialization handles within-event races; two UploadWorkflows on
   different events running concurrently both upload byte-identical
   clips with no awareness of each other.
6. **VAR removal is destructive to shared URLs.** When an event gets
   VAR'd we delete the S3 videos and the URLs go 404. The same problem
   as dedup-driven deletion. Not just a dedup constraint — anywhere we
   ever want to make an S3 object go away, we hit it.

**The missing primitive.** Decouple "what's stored in S3" from "what
URL we serve to consumers." Today the API exposes raw S3 URLs; that's
the layer violation. Add an indirection layer and (a) dedup gets
trivial, (b) VAR no longer kills shared URLs, (c) we can re-upload to
higher quality without breaking anything, (d) the API contract gets
cleaner.

**Proposed alternative — two-collection refactor with URL indirection.**

Add `video_assets` (the canonical byte-store) + `video_shares` (the
public share-ID layer):

```python
# video_assets — one row per byte-distinct S3 object
{
  _id: ObjectId(...),
  fixture_id: 1562345,
  s3_key: "1562345/canonical/abc123.mp4",
  s3_url_canonical: "http://minio:9000/footy/1562345/canonical/abc123.mp4",
  perceptual_hash: "dense:0.25:0=ab12...",
  perceptual_hash_prefix: "ab12",     # LSH bucket for similarity lookup
  md5: "...",
  width: 1920, height: 1080, duration: 45.2,
  file_size: 14_823_911,
  popularity: 7,                       # cross-event vote count
  first_seen: <datetime>,
  events: ["1562345_40_234_Goal_1", ...],  # back-refs, for reporting
  superseded_by: null | ObjectId(...), # set when this asset is dedup-merged
}

# video_shares — one row per share-stable ID exposed to the world
{
  _id: "v_abc123def456",               # the public share id, never changes
  asset_id: ObjectId(video_assets._id), # current canonical asset (mutable)
  event_id: "1562345_40_234_Goal_1",   # event this share is "for"
  timestamp_verified: true,
  extracted_minute: 23,
  created_at: <datetime>,
  state: "active" | "removed",
  removed_reason: null | "var" | "policy" | "asset_gone",
}
```

The event document's `_s3_videos` array becomes a list of `share_id`
references, not embedded S3 metadata. The API surfaces share IDs
(`GET /api/v1/videos/{share_id}` → 302 to the asset's current canonical
S3 URL, with `Cache-Control: no-store` so the redirect can be re-pointed
later). Public URLs become **forever-stable** even when:

- Two duplicates get merged (both shares' `asset_id` re-points to the
  same surviving asset)
- An asset gets superseded by a higher-quality version (the asset row
  gets `superseded_by` set; the share's `asset_id` re-points; old URL
  keeps working but serves the better clip)
- VAR removes the event (shares get `state=removed,
  removed_reason="var"`; the URL returns a friendly 410 Gone with a
  JSON body explaining "this goal was removed by VAR", not a raw S3 404)
- We re-upload, re-encode, migrate storage backends — all opaque to
  consumers

**Dedup behavior under the new shape.**

UploadWorkflow on incoming clip:

1. Compute perceptual hash + MD5.
2. Query `video_assets` by `(fixture_id, perceptual_hash_prefix)` —
   small candidate set (the prefix shards into LSH buckets so this is
   O(few)).
3. Hamming-compare against candidates.
4. If match found:
   - Bump existing asset's `popularity` (atomic `$inc`).
   - Add this event_id to the asset's `events` array (`$addToSet`).
   - Reuse the asset's S3 object — **don't re-upload**.
   - Mint a new `video_shares` row pointing to the existing asset, for
     this event. New share-id, same asset.
5. If no match:
   - Upload to S3, insert `video_assets`, mint `video_shares`.
6. Append the share-id to the event's `_s3_videos` (which is now just
   `list[str]`).

Cross-event dedup is automatic (the query at step 2 is fixture-wide).
Cross-instance dedup needs a unique index on
`video_assets.(fixture_id, perceptual_hash)` — `DuplicateKeyError` on
insert means "concurrent uploader beat us, retry the lookup + reuse
path." Atomic.

**The og-server consumes share IDs.** Currently og-server reads Mongo
for fixture+event metadata + S3 URLs and serves OpenGraph cards. After
this refactor, og-server reads share IDs and serves the redirect (or a
small landing page with the share embedded). OG cards remain stable
across asset re-pointing — Twitter/Slack/Discord caches keep working.

**VAR handling becomes a state transition, not a delete.** Currently
VAR removal deletes the S3 objects, hard-removes the event from Mongo.
After this refactor, VAR sets `video_shares.state="removed"` and the
asset's reference count drops; a periodic GC can clean up assets with
0 active shares (with a grace period for re-shares). Public URLs return
410 Gone instead of breaking the share consumer's day.

**Migration path (the part that's actually hard).**

1. **Land the schemas.** Create `video_assets` + `video_shares` empty.
   No code change yet.
2. **Dual-write phase.** New uploads write to both the new collections
   AND the legacy `_s3_videos` embedded array. The API still serves
   legacy URLs. No behavior change observable yet.
3. **Backfill.** Walk `fixtures_completed._s3_videos`, mint
   `video_assets` (one per distinct S3 object) + `video_shares` (one
   per existing array entry). Existing URLs continue to work because
   the S3 objects haven't moved.
4. **Land the share-id API endpoint** (`GET /api/v1/videos/{share_id}`).
   At this point both URL shapes work: legacy raw S3 URLs (for
   already-shared links) and new share-id URLs (for new shares).
5. **Cut new event documents to share-id-only `_s3_videos`.** Frontend
   starts requesting share-id URLs for newly-discovered videos.
   Stop dual-writing the embedded array on new uploads.
6. **Read-side compat indefinitely.** Old `fixtures_completed`
   documents keep their embedded arrays. The data-access layer reads
   both shapes. No backfill mutation of historical docs needed (read-
   shape compatibility is cheaper than write-shape migration).
7. **Background dedup pass (optional, do anytime after step 6).** Walk
   `video_assets`, perceptual-hash-cluster the ones that should be
   merged, pick a survivor per cluster, re-point all shares to the
   survivor, mark losers `superseded_by`. Don't delete the S3 objects
   immediately — keep them for a grace period so any in-flight CDN
   caches resolve cleanly.

**Tied to embedding migration.** Track 3 of LLM redesign swaps
perceptual hashing for image embeddings. With this schema, the swap
is local: `perceptual_hash` and `perceptual_hash_prefix` become
`embedding` (vector) and `embedding_lsh_bucket` (or a Mongo Atlas vector
index if we ever migrate Mongo). API surface unchanged. `video_assets`
remains the canonical layer.

**Why this is structural, not "a new collection."** The current dedup
limitation isn't a logic bug — it's a missing layer of indirection
between storage identity and share identity. Add the layer; dedup gets
clean, VAR stops breaking shared URLs, supersession becomes safe,
re-encoding becomes safe, the API contract gets cleaner, the og-server
contract gets cleaner. The "hacky shortcut" of `break`-at-first-match
becomes unnecessary.

**Scope sketch.** **L. Multi-week.** Concrete sub-pieces:

| Sub-piece                                              | Size |
| ------------------------------------------------------ | ---- |
| Two collections + schema validators + indexes          | S    |
| Dual-write in UploadWorkflow (legacy + new)            | M    |
| Backfill script + cutover gate                         | M    |
| `GET /api/v1/videos/{share_id}` redirect endpoint      | S    |
| Frontend cutover from raw S3 URLs to share IDs         | M (lives in vedanta-systems) |
| og-server cutover                                      | S    |
| VAR-as-state-transition in `remove_event_from_active`  | S    |
| Read-side compat for legacy `_s3_videos` shape         | S    |
| Background dedup-merge pass (optional, deferable)      | M    |

Dependencies: [§3](#3) UUID event IDs simplify the share-id ↔ event
join. [§11](#11) Phase 6 API cutover is the natural home for the new
endpoint; ship them together. Run F-1 first to land the data
discipline; F-3 is this work; F-5 (Phase 6) consumes the share-id
endpoint as a first-class consumer.

This is the highest-leverage section in the audit — every other dedup
concern, the VAR concern, the asset-supersession concern, and a chunk
of the Phase 6 API contract all flow from this single primitive being
correct.

---

## 5. Parallelism and concurrency

**Current state.** Workers run 30 concurrent activities + 10 concurrent
workflow tasks. Hash generation is **per-video** parallel
(`asyncio.gather` across the N discovered videos in a DownloadWorkflow)
but **per-frame serial** inside `_generate_perceptual_hash`. AI vision
calls are semaphore-bounded to 2 per worker process via
`LLM_SEMAPHORE = asyncio.Semaphore(2)` — but that's **per-process**, so
8 worker replicas × 2 = 16 concurrent vs joi's hard cap of 2.

**Lived problems.**

1. **joi LLM cap regularly exceeded.** Documented globally; biting
   harder during WC peaks.
2. **No priority.** CL night and R16 night compete equally for the
   discovery budget — no way to say "France-Norway is more important
   than this Conference League fixture."
3. **Scaler reacts, doesn't pre-empt.** Spawns more workers when queue
   depth grows, but doesn't know about *broadcast importance* — only
   "are there activities to do."
4. **Frame-level hash gen is the cheap win nobody took.** Each video's
   ~200-800 frames could be hashed in parallel within the activity
   (semaphore-bounded against memory). Currently serial means a 60-second
   video takes ~10s of hash gen wall-clock when it could take ~1s.

**The missing primitives.**

- A **global LLM concurrency gate** that knows about joi's hard cap
  regardless of replica count.
- A **frame-level worker pool** inside hash gen, gated by a memory-aware
  semaphore.
- **Per-fixture priority lanes** — separate Temporal task queues per
  priority level, scaler scales each independently.

**Proposed alternatives.**

1. **Ship Track 1 of `llm-stack-redesign.md` first.** Workspace LLM
   gateway in front of joi. Owns the global concurrency contract, kills
   the per-process Semaphores. Existing design — just ship it.
2. **Add a frame-level semaphore to `_generate_perceptual_hash`.**
   `asyncio.Semaphore(8)` (tunable) bounds concurrent frame extraction.
   Memory bound: ~50MB per frame in flight, so 8 frames ≈ 400MB ceiling
   per video. Safe.
3. **Defer priority lanes** until we observe contention under (1)+(2).
   Premature to design before the gateway exists.

**Scope sketch.** Track 1 is M (existing proposal). Frame-level pool is
S. Priority lanes XL — separate task queues, separate scaler logic,
separate workflow scheduling. Hold.

---

## 6. LLM concurrency and serving topology

**Current state.** Two activity types make LLM calls:
`download.validate_video_is_soccer` (vision) and `rag.get_team_aliases`
(text). Both endpoint `http://llama-small.joi`. Both use a per-process
`asyncio.Semaphore(2)`. joi's hard cap is 2 concurrent calls per the
host-budget rule.

**Lived problem.** With 8 worker replicas × 2 activities × 2 per-process
= up to 32 concurrent against a 2-cap. joi returns 503 / drops to a
crawl when over.

**The missing primitive.** A global concurrency gate that's aware of all
callers across the workspace, not just within one Python process.

**Proposed alternative.** Track 1 of
`docs/proposals/llm-stack-redesign.md` — workspace-level LLM gateway
container, fronted by Caddy, owns the concurrency contract. Each project
(found-footy, spin-cycle, future legal-tender) calls
`http://llm.<base-domain>/v1/chat/completions` instead of joi directly.
Gateway enforces global concurrency, priority headers, backpressure,
metrics. Kills the per-process Semaphores entirely.

**Bigger question.** Should priority be a header per request
(`X-Priority: high`) or a queue (separate gateway endpoints per
priority)? Header is simpler; queue gives Prometheus separation for
free. Lean header now, queue later if metrics confuse.

**Scope sketch.** M (existing proposal). Trivial removal of per-process
Semaphores after gateway lands.

---

## 7. Error taxonomy and recovery

**Current state.** Phase 1 typed errors LIVE as of 2026-06-30 night.
~6 error classes
(`VideoGeoRestrictedError`, `VideoNotAvailableError`, `LLMUnavailableError`,
etc.). Per-event `_telemetry` field. `underlying_error_class` log fields
on download failures.

**Still missing.**

1. **Dead-letter handling.** When all 10 download attempts fail
   (NL-Mar pattern), the event sits in `fixtures_completed` with 0
   videos. Nothing flags it for retry or human review. No
   `_dead_letter` collection.
2. **Per-error-class retry policy.** Today everything retries 3× with
   2× backoff regardless of error type. `VideoNotAvailableError` should
   retry 0× (tweet was deleted; permanent). `VideoGeoRestrictedError`
   should retry 0× on the same proxy but YES on a different proxy (if
   proxy pool ever ships). Currently we waste budget retrying permanent
   failures.
3. **Partial-completion semantics.** What if upload succeeds for 3 of 5
   videos in a batch? Currently the event might end with `_s3_videos`
   populated but `_download_complete=true` even though we didn't fully
   process the batch. No way to express "we did our best, here's what
   succeeded."

**The missing primitives.**

- A **`dead_letters` collection** keyed by `event_id` with the failure
  class and the original event payload.
- A **per-error-class retry decorator** on activities, gated by the
  Phase 1 typed errors.
- An **explicit per-batch outcome shape** on `_telemetry`
  (`batches: [{batch_id, attempted, succeeded, failed_by_class}]`).

**Proposed alternative.** Don't pre-design — observe a week of Phase 1
telemetry first. We finally have the data to know which error classes
dominate. After that:

- Add dead-letter collection (S)
- Add per-class retry table (S)
- Extend `_telemetry` for batch outcomes (S)

**Scope sketch.** S each, M total, gated on observation. Start
collecting failure-class distribution now via Loki dashboards.

---

## 8. Twitter fleet management

**The constraint.** The Twitter API v2 switch is a deferred business
decision (see end of section). For the foreseeable future, the
discovery layer is Firefox + Selenium against x.com — the choice is
committed, the question is how to *operate* it well. The current
methodology has structural seams that bite during peak loads and
re-auth cycles; that's what this section is about.

**Current design — one Firefox per container, scaler scales container
replicas.** Each `twitter` container owns:

- A Firefox process (kept warm between searches, idle CPU bleed patched
  2026-06-30).
- A profile directory at `/data/firefox_profile_{instance_id}` where
  `instance_id = md5(HOSTNAME)[:8]`. Profile dirs are container-local
  (volume-mounted but per-container path), so cookie state is *not*
  shared across replicas.
- A small FastAPI service on `:8888` with `/health` and `/search`.

The scaler reads MongoDB's "active goals in progress" count and scales
the `twitter` compose service between 2 and 8 replicas. The cookie
backup file at `~/.config/found-footy/twitter_cookies.json` (host
volume) is written when a container successfully authenticates and read
when a new container boots — **one-way sync, on boot only**.

**Lived problems — what this model actually costs.**

1. **Cookie state is fragmented across replicas.** Re-authenticating in
   one container (VNC into `twitter-prod-2`) writes that container's
   profile *and* the backup file. But the other live containers
   already have their own copies of the *old* cookies and don't re-read
   the backup until they restart. Result: half the replicas may serve
   stale-auth searches for hours. This is a real bug today — when the
   user has re-authed in prod, search results have been inconsistent
   across instances for the rest of that match window.
2. **Cold-start cost per replica.** When scaler bursts to 8 replicas
   for a CL/WC night, the 6 new containers each pay ~30-60s of
   Firefox + Selenium + profile-load startup. Peak demand arrives
   slightly faster than peak capacity comes online.
3. **One search at a time per container.** Each Firefox instance
   serializes searches because there's a single `driver` reference.
   With N containers we have N concurrent searches max — not N×K. Could
   be a single container running K Firefox processes in a pool.
4. **No graceful drain on scale-down.** Scaler stops a container via
   SIGTERM; whatever search is mid-flight dies, the worker activity
   gets a 503-like failure. No "finish active searches, refuse new"
   handshake.
5. **Health-check is binary.** `GET /health` returns
   `{healthy, authenticated, session_timeout}`. Doesn't communicate
   cookie age, recent search latency, consecutive-failure count,
   memory pressure, DOM-canary status. Scaler and worker can't route
   around degraded instances.
6. **No automated re-auth signal.** Cookies expire every few weeks. We
   notice when search starts returning 0 results during a match. There
   should be a Prometheus alert (or ntfy push) when consecutive auth
   failures spike across the fleet — *before* a match goes dark.
7. **Scaler is metric-impoverished.** Scaling uses "active goals count"
   alone. Doesn't know about per-instance backlog, fleet-wide cookie
   staleness, or search-latency degradation. Can't make decisions like
   "this instance is sick, spawn a replacement and drain it."
8. **DOM-selector fragility couples the entire fleet.** When Twitter
   ships a DOM change, every Firefox in the fleet breaks simultaneously.
   The hourly canary workflow (P4b) catches breaks after the fact, but
   recovery requires a code change + redeploy — no graceful degradation,
   no canary-driven feature gates.

**The missing primitive.** A Twitter fleet *manager* methodology that
owns session lifecycle and cookie coordination as first-class concerns,
separate from container replica count. Today "container = instance =
cookie owner" — three things welded into one; structurally separate
them.

**Proposed methodology — decouple instance from container, centralize
cookie state, enrich the health protocol.**

Three structural changes within the committed Firefox+Selenium+cookies
constraint:

### (a) Cookies move from per-container disk to a Mongo canonical row

New collection `twitter_sessions`:

```python
{
  _id: "canonical",                  # single-row pattern
  cookies: <bson.Binary>,            # serialized cookie blob (JSON or pickled list)
  cookies_version: 47,               # monotonic counter; bumped on each re-auth
  authenticated: true,
  last_refresh: <datetime>,          # last successful re-auth or verification
  last_search_succeeded_at: <datetime>,
  consecutive_auth_failures: 0,
  estimated_expiry: <datetime>,      # rolling estimate from observed lifetime
  reauth_notes: "VNC re-auth by Vedanta 2026-06-15",
}
```

Container behavior changes:

- **On boot:** read `_id="canonical"`, import cookies into Firefox via
  Selenium's `add_cookie` API. The backup file at
  `~/.config/found-footy/twitter_cookies.json` becomes a safety net for
  cold-starts when Mongo is unreachable (rare), not the authoritative
  source.
- **Before each search:** re-read `cookies_version` from Mongo. If
  it's newer than the local copy, hot-swap cookies in the running
  Firefox session — no browser restart needed. Re-auth in one container
  propagates to all others within seconds, not on next restart.
- **On successful re-auth:** write the new blob to Mongo, bump
  `cookies_version`, also update the backup file as safety net.

This single change eliminates the "stale-auth replicas serving wrong
results" failure mode (problem 1 above) without touching the scaling
model.

### (b) Rich health protocol

`/health` returns a structured payload:

```json
{
  "healthy": true,
  "authenticated": true,
  "cookies_version_local": 47,
  "cookies_version_canonical": 47,
  "cookies_age_seconds": 432,
  "last_search_latency_ms": 1830,
  "last_search_succeeded_at": "2026-06-30T18:14:22Z",
  "consecutive_search_failures": 0,
  "consecutive_auth_failures": 0,
  "browser_pid": 3208889,
  "memory_rss_mb": 1640,
  "in_flight_searches": 1,
  "draining": false,
  "dom_canary_last_status": "pass",
  "dom_canary_last_check": "2026-06-30T18:00:00Z"
}
```

Consumers:

- **Scaler** reads aggregate fleet health from all instances and
  decides not just *how many* replicas, but *whether to drain one* —
  e.g. "this instance's `last_search_latency_ms` is 5× the fleet
  median over the last 5 minutes → spawn a replacement, drain this
  one." Currently scaler is blind to per-instance degradation.
- **Worker routing** (the instance-discovery code in
  `src/activities/twitter.py`) picks the healthiest instance per
  request, not just round-robin. Routes around an instance that's
  authenticated-stale or showing high latency.
- **Loki/Prometheus** scrape `/health` and emit metrics. Prometheus
  alert when fleet-wide median `consecutive_auth_failures > 2` over
  5 min — *before* matches go dark.

### (c) Graceful drain on SIGTERM

Container intercepts SIGTERM:

1. Set `draining=true` in local health payload.
2. Refuse new `/search` requests with `503` and `X-Drain: true`
   response header.
3. Wait for `in_flight_searches` to reach 0 (with a 30s ceiling).
4. Exit cleanly.

Worker activity sees `503 X-Drain: true` and retries against a
different instance via the registry. Scaler's `down` command respects
the drain window (waits a max-grace period before forcing).

### What this does NOT address

- **Firefox-tab-pool pattern.** One container running K Firefox
  processes is a follow-up — compounds with this work but isn't
  required for the structural problems above. Fleet-wide concurrency
  is today bottlenecked by joi LLM cap (see §6), not by Firefox
  concurrency. Hold until that's true.
- **DOM-selector graceful degradation.** Beyond the canary catching
  breaks, no per-search fallback. Track 3 path (use the X syndication
  API for video metadata where possible, fall back to DOM only for
  tweet listings) is worth its own design proposal, separate from
  fleet management.
- **Source-quality scoring.** Verified accounts vs random users —
  separate change in [§10](#10-filter-pushdown-and-pipeline-ordering).
- **Snowflake-ID truncation root cause.** Still a dedicated
  investigation; not a fleet issue per se.

**Migration path.**

1. **Land `twitter_sessions` collection schema + indexes** (single row,
   `_id="canonical"`).
2. **Bootstrap.** A migration script reads the current cookie backup
   file, writes it to Mongo with `cookies_version=1`.
3. **Add the in-Mongo cookie boot path** to twitter session code. On
   boot, read canonical row, import into Firefox. Backup file
   becomes secondary.
4. **Add the per-search version check + hot-swap.** Worker doesn't
   notice; behavior is "cookies are always fresh."
5. **Move re-auth write path to Mongo.** VNC re-auth flow writes to
   Mongo first, then backup file.
6. **Roll the new behavior across the fleet** by recreating containers
   one at a time (scaler-safe — recreates are equivalent to the
   already-supported scale-up + scale-down lifecycle).
7. **Land the rich `/health` payload.** Workers + scaler + Loki start
   consuming the new fields.
8. **Land the SIGTERM drain handler.** Scaler's `down` command
   respects drain.
9. **Add the cookie-staleness Prometheus alert** (§9 ties in).

Each step is independently shippable; rollback is "ignore the new
fields, fall back to old paths." Low blast radius.

**The API-switch question stays deferred.** Twitter API v2 basic tier
(~$100/mo, ~10K tweets/mo) remains a real decision to evaluate annually.
Current scrape volume is ~9K interactions/mo, just under basic tier;
reliability and operator overhead are the real drivers. **After this
fleet methodology lands, the operational cost of scraping drops
markedly** (less re-auth pain, less search inconsistency, fewer
silent-fail windows), which weakens the API-switch case in the near
term. Revisit when (a) volume grows past basic tier, (b) Twitter ships
a DOM change that takes > 24h to recover from, or (c) we want to
extend discovery to non-Twitter sources (in which case the abstraction
layer for "discovery provider" gets built and Twitter becomes one of N).

**Scope sketch.** M overall. Sub-pieces:

| Sub-piece                                              | Size |
| ------------------------------------------------------ | ---- |
| `twitter_sessions` schema + boot read                  | S    |
| Cookie hot-swap on `cookies_version` change            | S    |
| Auth-success → Mongo write path (replaces file-only)   | S    |
| Rich `/health` payload + Loki/Prometheus integration   | S    |
| Worker routing around degraded instances               | S    |
| SIGTERM drain handler + scaler grace-period            | S    |
| Cookie-staleness Prometheus alert                      | S    |
| Bootstrap migration script                             | S    |

Total ~1 week of focused work. No external dependencies — Mongo +
Selenium + the existing scaler registry are all the moving parts.
Should run after F-1 (data discipline) so the `twitter_sessions` and
health payload have Pydantic types from day one.

This is the second-highest-leverage section in the audit after [§4](#4) —
fleet flakiness during peak loads is one of the user-visible quality
issues, and the structural seams above are the root cause of basically
every Twitter-related incident in the operational history.

---

## 9. Observability, alerting, deploy visibility

**Current state.** Phase 1 telemetry just landed. Loki has logs.
`docs/logging.md` documents canonical queries. `monitor-loki:3100` is
the forensic endpoint. Grafana exists but found-footy doesn't have a
dedicated dashboard.

**Still missing.**

1. **Alerting.** No Prometheus alerts on coverage SLO drop. No PagerDuty
   / ntfy / Slack notifications. We find out things are bad when the
   user notices.
2. **Deploy tracking.** "Prod is on commit X, main is on commit Y" not
   visible anywhere — see [§1](#1-the-build-deploy-gap-prod--main).
3. **Saved dashboards.** `docs/logging.md` is the cookbook but every
   investigation re-types the queries. Should be `view dashboard` not
   `LogQL practice`.
4. **Per-match coverage SLO visibility.** Phase 4a shipped per-match
   coverage summary log lines, but they're not yet aggregated into a
   "this week's coverage rate by league" view.

**Missing primitives.**

- **Alerting rules in Prometheus/Loki** (separate from queries).
- **A `deploy_event` log line** emitted at worker startup with
  `image_tag`, `git_sha`, `built_at` (cross-references §1).
- **A `found-footy.json` Grafana dashboard** committed to the repo and
  importable.

**Proposed alternatives.**

1. Ship a baseline dashboard: rolling coverage SLO, error-class
   breakdown, joi LLM cap-exceeded rate, scaler decisions, deploy
   freshness. ~5 panels.
2. Emit `deploy_event` log line per §1.
3. Add 3-5 alerts: coverage SLO < 50% for 1h, error rate spike, joi
   503-rate, scaler stuck at MAX_INSTANCES for >10m, prod commit drift
   > 7 days from main.

**Scope sketch.** S each, M total. Foundational because the rest of the
audit's recommendations rely on "we can observe whether they worked."

---

## 10. Filter pushdown and pipeline ordering

**Current state.** Filtering happens at `src/activities/download.py`:
aspect ratio (just tightened), short-edge resolution, duration. AI
vision filters further (soccer/screen) post-download. Perceptual hash
runs post-AI.

**Pushdown opportunities.**

1. **Duration filter** runs in `download.py` after the syndication-API
   response, but `duration_seconds` is **already available in the
   Twitter search result** before the worker even sees the URL. Move
   the duration filter into `twitter/session.py` to drop garbage at
   discovery.
2. **URL-format pre-validation.** Snowflake length already validated
   (P2b). Could add: is the host x.com / twitter.com? Is the status ID
   numeric? Is the URL well-formed? Cheap rejects pre-download.
3. **Account-quality scoring.** Tweets by verified accounts /
   broadcaster accounts / accounts with > N followers are higher-EV
   candidates than random users. Could filter or rank at discovery.

**Proposed alternative.** Add a `_pre_filter_videos` step in
`twitter/session.py` that drops videos failing duration / URL-format
checks before returning the URL list to the worker. Saves a per-clip
activity round-trip.

**Scope sketch.** S for duration push-down + URL format checks. M for
account-quality scoring (needs taxonomy + tuning).

---

## 11. Cross-project boundary — vedanta-systems API

**Current state.** vedanta-systems direct-reads Mongo over the
`luv-prod` docker network via a Node Express service that knows the
found-footy schema. Worker calls `notify_frontend_refresh` for SSE
push. Phase 6 of the roadmap is the FastAPI cutover.

**What Phase 6 has right.** Replacing direct Mongo reads with a typed
HTTP contract. URL stability under future schema changes. Owning the
SSE endpoint server-side.

**What Phase 6 is missing.**

1. **No typed contract.** Plan is "FastAPI exposes endpoints" — but is
   there an OpenAPI spec? If yes, do we generate TS types from it for
   vedanta-systems? Currently the API and the frontend share knowledge
   via documentation, not code.
2. **No auth.** vedanta-systems is currently trust-the-network on
   `luv-prod`. Once Phase 6 lands and the API is hostnamed via Caddy
   (`found-footy.<base-domain>/api/v1/...`), should we add Authorization
   headers? Probably yes given the multi-project portal context.
3. **No event push.** SSE is pull-shaped — the client opens a stream,
   server pushes updates. Goal-video-ready could also be webhook-pushed
   (vedanta-systems exposes a webhook endpoint, found-footy posts when
   a video ships). Lower-latency than SSE on cold connections.
4. **No contract versioning.** Phase 6 plans `/api/v1/...` but no plan
   for what happens when the contract changes. Sunset window? Deprecate
   headers?

**Proposed alternative.** Take the Phase 6 budget and add:

- **OpenAPI spec with TS type generation.** Single command in the
  vedanta-systems CI pipeline regenerates types from the spec. Schema
  drift becomes a TS error.
- **Auth via Caddy.** Caddy already fronts everything; add a header
  check (`X-Internal-Token` from the vedanta-systems API) at the
  Caddyfile, not in FastAPI.
- **Webhook delivery for "video ready" events.** Keep SSE for the
  realtime stream, add webhook for durable delivery.
- **Versioning policy in `docs/api-contract.md`.** Already exists as a
  file; populate it with the contract rules.
- **Share-id video endpoint** (`GET /api/v1/videos/{share_id}` → 302
  to the canonical S3 URL). This is the consumer side of [§4](#4)'s
  indirection layer; without it, the rest of §4 doesn't pay off
  publicly. Ship the two together — `video_shares` and the endpoint —
  as one cohesive unit. og-server consumes the same endpoint instead
  of reading Mongo directly.

**Scope sketch.** Phase 6 is currently budgeted L. These additions move
it to L+ but they're the right additions.

---

## 12. Testing strategy

**Current state.** ~5% coverage. `test_clock_parsing.py` and
`test_activity_registration.py` are real.
`tests/test_rag_pipeline.py` is broken at import (per May audit).

**Lived problems.**

1. Every refactor is risky because no safety net. The Phase 3 module
   splits shipped without per-module tests; we trusted the human
   review and prod observation.
2. NL-Mar diagnostic relied on production data because no synthetic
   harness exists. We can't reproduce a goal-detection-to-S3-upload
   path locally without watching a live match.
3. Regressions can ship undetected — the `events.py` drop-workflow fix
   was authored 2026-05-31 and presumably never exercised in test (the
   bug it fixes was a Mongo-API limitation that only surfaces against a
   live Mongo).

**Missing primitives.**

- A **synthetic fixture lifecycle harness**: `tests/synthetic/` with a
  `test_match_lifecycle.py` that runs the full pipeline against dev
  infra using a recorded fixture's API response + a recorded set of
  Twitter clips. Replays the whole flow.
- **Per-module unit tests** for the Phase 3 split modules
  (download/vision/hashing, upload/core/dedup/replacement, data/* mixins).
- **Integration tests** for download → upload signal flow, dedup
  scoping, completion marking.

**Proposed alternative.** Target 50% coverage by end of deep-pass (from
5%). Start with the synthetic harness — one well-tested end-to-end
scenario uncovers more bugs than 50 unit tests against trivial
functions.

**Scope sketch.** L. But pay-back is enormous — every subsequent change
is faster and safer. Best run *alongside* the other audit-driven phases,
not as a separate "now we write tests" sprint that nobody will start.

---

## 13. Code organization — domain-driven structure post-Phase 3

**The constraint.** Docker-compose stack is locked (worker container,
twitter container, scaler container, infra). This section is about
*Python code organization within those containers*, not about
introducing new deployment units or microservices.

**Current state — what Phase 3 already delivered.**

| Old monolith              | After Phase 3                                                                       |
| ------------------------- | ----------------------------------------------------------------------------------- |
| `download.py` (1672 lines) | `download/core.py` + `vision.py` + `hashing.py`                                    |
| `upload.py` (1467 lines)   | `upload/core.py` + `upload/dedup.py` + `upload/replacement.py`                      |
| `mongo_store.py` (1608)    | `data/store.py` + per-domain mixins (`fixtures.py`, `events.py`, `videos.py`, `aliases.py`, `cache.py`) |
| `twitter/session.py`       | `session.py` + `scrape.py`                                                          |

The splits were valuable — they unlocked Phase 4+ work that wouldn't
have fit in the monoliths. But they were *file-shape* splits, not
*ownership* splits. The mixins all live on one class
(`FootyMongoStore`) so there's no actual encapsulation boundary; the
download / upload sub-packages still call directly into `mongo_store`
and `s3_store`; the activity files still vary wildly in size and
responsibility.

**Lived problems with the Phase-3 shape.**

1. **`data/events.py` is 660+ lines and is the central nervous system.**
   It carries event CRUD, workflow-array `$addToSet` tracking, lifecycle
   state mutations (`mark_event_monitor_complete` etc.), VAR removal,
   completion gating — all on one mixin class. Touching any of those
   concerns ripples to all the others.
2. **Activity files have no clear ceiling on responsibility.**
   `src/activities/monitor.py` is 760+ lines and contains the staging
   poll, the pre-activation failsafe, the active fetch, the event
   processing, the completion check, the frontend-refresh notification
   — six concerns in one file. Compare to `twitter/twitter.py` at 280
   lines doing only Twitter search. The big files are big because
   nothing told them not to be.
3. **No "domain service" layer.** Activities import `mongo_store`
   directly and orchestrate Mongo writes inline. There's nowhere to
   say "the rules for whether an event is ready to trigger Twitter
   live here, separate from the Mongo CRUD." So those rules live
   scattered across `monitor.py`, `events.py`, and the workflow code.
4. **Test surface is the activity boundary, not the unit boundary.**
   Because activities are the only callers of business logic, the
   only way to test event-lifecycle decisions is via Temporal
   integration tests with a live Mongo. We have ~5% coverage and
   it's not coincidental.
5. **Cross-domain operations have no obvious home.** "VAR a goal"
   touches event (mark removed) + video (delete S3) + share (mark
   gone, per §4). It currently lives in `mongo_store.events.py` as
   a 100-line method that reaches across collections; should be an
   orchestration that calls the event service + video service +
   share service. Today the boundary doesn't exist to put it
   somewhere honest.
6. **The video-rank-drift bug** (added to `todo.md` 2026-06-30 from
   the Norway v Côte d'Ivoire match) is a category instance: ranks
   are computed in `upload/core.py`, written via `mongo_store.videos`,
   read by frontend code, and there's no single "owns video ranking"
   service. The bug is a concurrency / partial-write issue that a
   `VideoAssetService.recalculate_ranks(fixture_id)` with proper
   transactional discipline would have made impossible.
7. **`twitter/session.py` is 1100+ lines** even after the `scrape.py`
   extraction. Browser-automation, cookie management, search
   orchestration, health-check, lifecycle — all in one file. §8's
   fleet methodology compounds the case for further splitting this.

**The missing primitive.** Clear **domain boundaries** with services
as the only `mongo_store` / `s3_store` callers. Activities orchestrate
*services*, not Mongo. Workflows orchestrate *activities*, not
business logic.

**Proposed methodology — domain bundles with explicit composition.**

### Bundle shape (one per domain)

```
src/domains/<name>/
  __init__.py             # public API: from .service import <Name>Service
  models.py               # Pydantic types from §3
  store.py                # Mongo CRUD, accepts/returns models from §3
  service.py              # business logic, calls store, NEVER touches mongo directly
  lifecycle.py            # state machine, if applicable
  events.py               # domain-event types emitted for cross-domain wiring (optional)
  tests/
    test_service.py       # unit tests, mock store
    test_lifecycle.py     # state-machine tests
    test_store.py         # integration tests against dev Mongo
```

### Concrete domain enumeration

| Domain        | Owns                                                                | Today's source                              | Size after move |
| ------------- | ------------------------------------------------------------------- | ------------------------------------------- | --------------- |
| `fixture`     | fixtures_{staging,live,active,completed} CRUD + lifecycle (NS→active→completed) | `data/fixtures.py` + parts of `monitor.py`  | M               |
| `event`       | events array CRUD + 3-poll debounce state machine + VAR transitions + telemetry | `data/events.py` (660 lines) + most of `monitor.py:process_fixture_events` | L |
| `video_asset` | canonical S3 byte-store (the §4 `video_assets` collection) + ranking | `data/videos.py` + `upload/core.py` + `upload/dedup.py` | M  |
| `video_share` | public share IDs (the §4 `video_shares` collection) + redirection logic | NEW (lands with §4)                       | S               |
| `team_alias`  | team_aliases collection + RAG pipeline + Wikidata + top_flight cache | `data/aliases.py` + `activities/rag.py` + `utils/team_data.py` | M     |
| `twitter_session` | Firefox fleet management from §8 (cookies, /health, drain) + the `twitter_sessions` collection | `twitter/` (whole package after §8 lands) | M           |
| `vision`      | AI clock extraction + soccer/screen classification + embedding (when Track 3 lands) | `activities/vision.py` + `activities/hashing.py` | M       |
| `discovery`   | Twitter search orchestration + URL extraction + source filtering    | `twitter/scrape.py` + parts of `activities/twitter.py` | M    |
| `llm_gateway` | Track 1 client; concurrency-aware joi caller                       | NEW (lands with §6)                        | S               |

Nine domains. Five exist in some form; four are new (§4 video_asset
re-shaping, §4 video_share, §8 twitter_session fleet collection, §6
llm_gateway client).

### What activities and workflows look like after

Activities become thin orchestrators that compose domain services.
The Mongo / S3 dependencies vanish from activity code.

```python
# src/activities/monitor.py — process_fixture_events, after re-org
from src.domains.event import EventService, EventLifecycle
from src.domains.fixture import FixtureService

@activity.defn
async def process_fixture_events(fixture_id: int, workflow_id: str) -> dict:
    """Process events for a single fixture in this monitor cycle.

    Returns the list of events newly stable enough to trigger Twitter.
    """
    fixtures = FixtureService()
    events = EventService()
    lifecycle = EventLifecycle()

    fixture = await fixtures.get_active(fixture_id)
    if fixture is None:
        return {"twitter_triggered": []}

    changes = await events.detect_changes(fixture)
    await events.register_monitor_workflow(fixture_id, workflow_id, changes)

    triggered = []
    for event in changes.new_or_advanced:
        if lifecycle.is_stable_and_player_known(event):
            await events.mark_monitor_complete(event)
            triggered.append(event.as_twitter_input())

    return {"twitter_triggered": triggered}
```

Compare to today's `process_fixture_events` (~150 lines, mixed
Mongo updates and lifecycle decisions). The new shape is short
because *the lifecycle decisions live in `EventLifecycle`*, not here.

Workflows continue to orchestrate activities — they don't import
domain services directly (Temporal replay constraints make this
awkward). The activity boundary is the right contract between the
deterministic workflow layer and the side-effectful service layer.

### Cross-domain operations get an explicit home

"VAR a goal" today is a single 100-line method on `mongo_store.events`.
After re-org it's a *use case* in `src/usecases/`:

```python
# src/usecases/var_remove_event.py
async def var_remove_event(fixture_id: int, event_id: str) -> VarOutcome:
    """When the API surfaces an event as removed (VAR), tear down the
    enhancement chain. Touches event, video_asset, video_share."""
    events = EventService()
    assets = VideoAssetService()
    shares = VideoShareService()

    event = await events.get(fixture_id, event_id)
    if event is None or event.removed:
        return VarOutcome.no_op()

    # Mark the event removed (atomic).
    await events.mark_removed(event, reason="var")

    # Decrement refcounts on each share; assets with no live shares
    # become eligible for GC (separate periodic task).
    for share_id in event.s3_videos:
        await shares.mark_removed(share_id, reason="var")
        await assets.decrement_refcount_via_share(share_id)

    return VarOutcome.success(event_id=event_id, shares_marked=len(event.s3_videos))
```

Use cases are the home for cross-domain workflows that don't fit
cleanly in one domain. They're called from activities; activities are
called from Temporal workflows.

### Subsystem packages (not domains)

Some packages aren't domains — they're infrastructure or external
adapters:

- `src/orchestration/` — Temporal workflow definitions live here
  (currently `src/workflows/`; rename for clarity post-re-org).
- `src/api/` — FastAPI (Phase 6 / [§11](#11)). Calls into use cases
  + services.
- `src/scaler/` — already separate package, unchanged.
- `twitter/` — already its own container; after [§8](#8) ships, the
  contents reorganize internally but the package stays.
- `src/utils/` — Pydantic models from [§3](#3), config, logging.
  Resists growth — anything that's domain logic moves to a domain.

### Migration ordering

Domain-by-domain extraction, dependency-ordered:

1. **`fixture`** — smallest, lowest risk, no inbound dependencies.
   Establishes the bundle pattern, validates the Pydantic-based
   `store.py` shape from [§3](#3). (M)
2. **`event`** — the big one (660 lines today plus most of
   `monitor.py`). Depends on `fixture`. Lifecycle decisions extracted
   into `EventLifecycle`. This is the load-bearing extraction. (L)
3. **`team_alias`** — relatively self-contained, but the RAG pipeline
   refactor is real work. Can run in parallel with `event` if a
   second pair of hands. (M)
4. **`video_asset` + `video_share`** — lands with [§4](#4) dedup
   re-arch. Re-org of `upload/` package + the new collections.
   Eliminates the rank-drift bug from `todo.md`. (L)
5. **`discovery`** — extract from `twitter/scrape.py` +
   `activities/twitter.py`. Depends on §8 fleet methodology landing. (M)
6. **`twitter_session`** — lands with [§8](#8). (M)
7. **`vision`** — extract from `activities/vision.py` +
   `activities/hashing.py`. (M)
8. **`llm_gateway`** — lands with [§6](#6) Track 1. (S)

### Activities and workflows migrate alongside

Each domain extraction includes the rewrite of the activities that
previously held that domain's logic inline. So:

- Extracting `event` → rewrite `monitor.process_fixture_events`,
  `monitor.complete_fixture_if_ready`, `download.register_download_workflow`,
  etc. to use `EventService` + `EventLifecycle`.
- Extracting `video_asset` → rewrite `upload.*` activities.

This is the "natural home for the docstring backfill" mentioned in
the original §13: every file gets touched, so every file gets the
new module header + per-function docstrings landed in the same diff.

### Backward compatibility during migration

`mongo_store` survives as a **thin delegation layer** during the
extraction phases. `mongo_store.events.add_event_to_active(...)`
delegates to `EventService().add_to_active(...)` under the hood.
Old call sites keep working; new call sites use the service. Once
every call site is migrated, the delegation layer disappears.

### Sub-piece scope

| Sub-piece                                  | Size | Depends on                          |
| ------------------------------------------ | ---- | ----------------------------------- |
| Bundle scaffold + first domain (`fixture`) | M    | [§3](#3) Pydantic models            |
| `event` extraction + lifecycle pulled out  | L    | `fixture` landed                    |
| `team_alias` extraction + RAG re-org       | M    | `fixture` landed                    |
| `video_asset` + `video_share` (with §4)    | L    | [§3](#3), [§4](#4)                  |
| `discovery` extraction                     | M    | [§8](#8) fleet                      |
| `twitter_session` (with §8)                | M    | [§3](#3), [§8](#8)                  |
| `vision` extraction                        | M    | [§3](#3)                            |
| `llm_gateway` (with §6 Track 1)            | S    | [§6](#6)                            |
| `mongo_store` delegation-layer demolition  | S    | All extractions complete            |
| Docstring backfill (per-file, in the same commits) | rolling | each extraction                |

Total **L** as a sequenced phase; full re-org takes the longest of
any audit section but pays back the cleanest. Run after [§3](#3) and
in coordination with [§4](#4), [§6](#6), [§8](#8) — the new collections
and methodologies land naturally as domain extractions instead of as
separate bolt-ons.

The end state: every activity file ≤ 200 lines; every domain bundle
≤ ~1000 lines split across `models.py` / `store.py` / `service.py` /
`lifecycle.py`; cross-domain operations live in `src/usecases/` with
explicit names; `mongo_store` and `s3_store` are vestigial and
ready to delete.

---

## 14. Configuration management

**Current state.** `src/utils/config.py` is a flat constants file. Phase
4 added `orchestration_config.py` and started a `dedup_config.py` split
but didn't finish. Some magic numbers still appear inline (e.g. workflow
timeouts in DownloadWorkflow).

**Lived problems.**

1. Magic numbers re-appear in code despite the configs.
2. No env-var overrides — config is code, so changing a value means a
   rebuild and deploy.
3. No per-environment differentiation beyond docker-compose differences.
4. No type checking — `MAX_HAMMING_DISTANCE = "10"` would silently
   become a string-comparison bug.

**Missing primitives.**

- **Layered config**: defaults in code + env-var overrides + optional
  per-env YAML.
- **Typed config**: Pydantic `BaseSettings`.

**Proposed alternative.** Collapse into a single `src/utils/settings.py`
using Pydantic `BaseSettings`. Support env overrides (`AR_MIN_RATIO=1.78`
overrides the default). Document via the schema. Print on worker startup
so we can see "this worker started with these values."

**Scope sketch.** S. Mechanical-but-touches-everything change. Best
done in tandem with §13.

---

## 15. Single-node assumption and capacity (deferred)

**Current state.** Everything on luv: Mongo, Postgres, MinIO, scaler,
workers, twitter, monitor stack. joi is separate but is single-purpose
LLM serving.

**Lived problems.** 100°C thermal events during CL/WC peaks. Memory
budget per host CLAUDE.md is ~63 GB steady, ~101 GB at peak. Scaler can
spawn 8 worker + 8 twitter replicas — each with their own Firefox
process pre-2026-06-30.

**Why this section is deferred.** Almost all the thermal pressure came
from the Firefox CPU bleed (patched 2026-06-30) and the joi
concurrency-cap exceeded (Track 1 will fix). Premature to design
multi-host before observing post-fix utilization.

**What to do.** Observe through one full WC week with the new state.
If thermal events recur, the candidate split is "twitter replicas
move to a second host" — they're the largest per-replica RAM consumers
and don't need access to the local Mongo/Postgres/MinIO. Workers must
stay on luv (network round-trip costs).

**Scope sketch.** Deferred. Re-evaluate post-Track 1 + dashboards.

---

## 16. Implementation order

Reorganizing the existing [roadmap](./roadmap.md) given this audit:

### Phase F-0: Foundation (must-do-first, ~3 days)

These are the unblocks. Nothing else matters without them.

- **[§1](#1-the-build-deploy-gap-prod--main)** Deploy tracking + bin/deploy script.
- **[§9](#9-observability-alerting-deploy-visibility)** Baseline Grafana dashboard + 5 alerts.
- Existing **roadmap Phase 1** (was committed but not deployed pre-2026-06-30; now deployed but
  observe data, refine taxonomies).
- Existing **roadmap Phase 2** (status-ID truncation root cause — this is overdue).

### Phase F-1: Identity and schema discipline (~3 days)

The data-correctness foundation for everything else.

- **[§2](#2-workflow-id-conventions-and-identity)** Workflow ID unification (S).
- **[§3](#3-data-model--mongo-vs-postgres-plus-schema-discipline)**
  JSON Schema validators + Pydantic write layer + UUID event IDs (M).
- **[§14](#14-configuration-management)** Pydantic BaseSettings config (S).

### Phase F-2: Concurrency contract (~2 days)

Off-loads the load-bearing scarcity (joi LLM cap) properly.

- **[§6](#6-llm-concurrency-and-serving-topology)** Track 1 workspace LLM gateway (M).
- **[§5](#5-parallelism-and-concurrency)** Frame-level hash gen pool (S).

### Phase F-3: Dedup re-architecture (~3 days)

The big functional improvement nobody has done yet.

- **[§4](#4-dedup-strategy-end-to-end)** Fixture-wide `video_hashes` collection (M).
- Resolves the `break`-at-first-match bug *and* enables cross-event /
  cross-instance dedup.
- Sets up Track 3 (embedding swap) as a future drop-in.

### Phase F-4: Discovery hardening (~2 days)

- **[§10](#10-filter-pushdown-and-pipeline-ordering)** Twitter-side
  duration + URL filtering (S).
- **[§8](#8-twitter-scraping-strategy)** Tier 1 (URL validation +
  source filtering) and Tier 2 (cookie health prediction).
- Defer Tier 3 (API switch) as separate decision.

### Phase F-5: Frontend boundary (~3-4 days)

- **[§11](#11-cross-project-boundary--vedanta-systems-api)** Phase 6 FastAPI
  + OpenAPI + TS gen + Caddy auth + webhook events.

### Phase F-6: Testing + organization (~3-5 days, alongside everything above)

- **[§12](#12-testing-strategy)** Synthetic fixture lifecycle harness +
  per-module unit tests as modules get touched.
- **[§13](#13-code-organization-post-phase-3)** Domain-driven re-org.
- Docstring backfill as part of (13) — touching every file anyway.

### Deferred

- **[§7](#7-error-taxonomy-and-recovery)** Dead-letter + retry policy
  refinement — observe Phase 1 data first.
- **[§15](#15-single-node-assumption-and-capacity-deferred)** Multi-host
  capacity planning — observe post-Track 1 first.

### Cross-cutting

- Backfill docstrings per the new policy as files get touched
  (don't do a separate docstring-only pass).
- Update `audit.md` (May 2026) entries as their items get closed by the
  phases above.
- Append architectural decisions to `decisions.md` as each phase ships.

### Honest total

~16-20 focused days of work, distributed across 6-8 weeks if working
part-time. The roadmap had this at 12-15 days; the audit added
~3-5 days of foundation work (F-0 + F-1 + F-2) that wasn't in the
original plan but addresses the biggest *operational* gaps, not just the
correctness ones.

The endpoint is a found-footy that:

- Self-reports when prod diverges from main (no more 7-week surprises).
- Has typed errors, typed config, typed Mongo writes, OpenAPI'd APIs.
- Deduplicates fixture-wide (not just per-event), with the math
  swappable to embeddings.
- Routes LLM calls through a global gateway with priority + concurrency.
- Discovers Twitter clips through cheaper filters, with cookie-health
  prediction.
- Surfaces a clean API contract to vedanta-systems, push + pull.
- Tests its own lifecycle in CI.
- Has docs that match the code that's running.

That's the "phenomenal" target.
