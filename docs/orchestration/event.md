# Event workflow

Current behavior for per-event discovery and video processing. See the
[orchestration index](./README.md) for the complete workflow map.

## EventWorkflow

The per-goal orchestrator (renamed from DiscoveryWorkflow, decisions.md
2026-08-03 — the workflow became the event orchestrator, so "Discovery"
undersold it; the discovery *phase* keeps its name). Spawned
Temporal-direct by Monitor's `ReconcileFixture` via `DownstreamSpawner`
when an event's `downstream_triggered` flips (workflow ID `event-{id}`,
failed-only reuse; NOT scheduled — 2026-07-16, revised by FF-007). Location:
`internal/workflow/event.go` (orchestration) + the `event_pipeline*.go`
(consumer) + `internal/activity/discovery/`. The shared spawn and candidate
records live in `internal/contract/discovery/`, outside either caller.

Runs a **producer + consumer concurrently** (`workflow.Go` + a
`workflow.Selector` queue), with Temporal owning completion — the
consumer returns when search is done AND nothing is in flight (no idle
timeout):

**Producer** (the discovery search loop). `GetDiscoveryConfig` →
`FetchTeamAliases` (canonical name only; resolved aliases are disconnected) →
`querybuilder.Build(player, canonical, nil)` → N usable observations × M
spacing (`config.DiscoveryConfig`, default 15 × 60s) of
`SearchTweets` with per-event `exclude_urls` accumulating across attempts
(so attempts 2+ stop early on consecutive-already-seen). Each new
candidate becomes workflow-owned as `CandidateEvidence`. The producer submits
all new candidates to `DownloadAndStage` before awaiting their concurrent
`StoreCandidate` observation inserts, so Postgres does not gate clip launch.
The activity returns
the exact MD5, staging key, and media metadata. The consumer claims that MD5
before scheduling dense `HashVideo`; simultaneous byte-identical candidates
wait behind one claimant instead of repeating ffmpeg work. After a successful
hash, new histories retain those follower URLs until the shared validation path
has a real terminal result (FF-065). Histories started before FF-022 retain the
versioned `VideoWorkflow` child command sequence.
Wall-clock
`max_age_minutes` filter
(decisions.md 2026-07-23).

`DownloadAndStage` resolves the tweet and downloads its selected CDN variant
inside one retryable activity. A 403 from the metadata endpoint is terminal
`geo_restricted`; a 403 from the subsequent CDN byte request is transient
`ErrCDNForbidden`. The latter consumes the normal four activity attempts,
rerunning resolution before every download so an expired or edge-rejected
variant URL can refresh (FF-029). Exhaustion still follows FF-002's correlated
`download_error` path. New histories retain FF-060's bounded stage/class in
`outcome_detail.failure` without persisting the raw error or signed variant.

**Search availability contract (FF-061).** The browser result is one of
`rendered`, `explicit_empty`, `login`, `upstream_error`, or
`unknown_timeout`. Only the first two advance `attempts_completed`. New
histories give `SearchTweets` one activity attempt, then retry unavailable
probes at the normal one-minute workflow cadence so one logical probe cannot
multiply browser traffic. `DISCOVERY_MAX_UNAVAILABLE_ATTEMPTS` is a separate
budget, default 15. With the default 15 usable searches, one new history can
issue at most 30 SearchTweets activity executions. A failed per-event transport
can add one static-service HTTP request inside an execution.

Every probe checkpoints the monotonic usable/unavailable counters plus the
latest secret-free page/timeline evidence in downstream metadata. A replacement
execution restores both budgets. Exhausting the unavailable budget drains
already-owned candidates and completes with `twitter_unavailable` when none
were processed; fixture completion cannot wait forever. The
`ff-061-search-availability` marker leaves pre-FF-061 histories on FF-017's
three/four-attempt activity policy and historical checkpoint behavior. The
[decision](../decisions/2026-08-20-twitter-search-attempts-require-usable-observations.md)
and [incident](../incidents/2026-08-20-twitter-feed-suppression.md) hold the
rationale and production evidence.

Classified non-2xx browser responses cross the activity boundary as retryable
Temporal application errors with typed output details. Pre-FF-061 histories
therefore execute their original retry chain. New histories make one activity
call, decode those details in EventWorkflow, and advance only the unavailable
counter.

**Candidate failure contract (FF-002 + FF-022 + FF-060).** `download_error`
stamps the persisted candidate `failed`; no staging object exists. New
histories also persist bounded `outcome_detail.failure.stage/class` evidence
from the final retryable activity error. The stages cover resolve, scratch,
CDN download, probe, staging upload, and a workflow fallback; raw errors and
signed media URLs remain outside Postgres. `hash_error` stamps only the
claimant `failed` and calls `DeleteStaging` with its key. A waiting exact-byte
candidate then receives ownership and its own full hash retry budget, so one
unreadable staging object cannot discard the cluster. Invalid download output
uses `video_workflow_invalid_outcome` and reclaims any returned staging key.
The compatibility child retains FF-002's correlated unexpected-child and typed
terminal-output paths. Cancellation bypasses every forensic and cleanup
command under FF-015.

**Exact-follower outcome contract (FF-065).** Exact-byte collapse avoids
duplicate work; it does not by itself prove a durable winner. A candidate that
matches an existing promoted asset becomes `duplicate` immediately with
`winner_asset_id`. A hash waiter or a candidate that matches a vision-pending
representative contributes popularity and releases its own staging object, but
its URL stays workflow-owned until that representative terminates. Promotion
makes followers `duplicate`; deterministic content rejection makes every member
`rejected` with the shared evidence; exhausted vision or promotion makes every
member `failed` with the shared reason. These branches share one hash, vision,
and promotion retry unit. The `ff-065-exact-follower-outcome` marker preserves
the former immediate-duplicate command sequence for histories already in
flight.

**Candidate durability contract (FF-034).** The workflow retains each
candidate's immutable event, query/attempt, tweet, author, age, and video-page
evidence until processing reaches a terminal outcome. Terminal persistence is
one idempotent UPSERT: it finishes an observed row or creates the complete row
if the earlier observation insert never landed. Only that successful activity
advances workflow ownership from in-flight to terminal. A terminal write that
exhausts retries fails EventWorkflow, leaves its downstream checklist open, and
prevents a false successful parent. A failed observation insert does not block
the already-dispatched clip; it keeps the search attempt uncheckpointed so a
replacement execution cannot skip evidence that never became durable.

**Critical-path measurement contract (FF-050).** EventWorkflow records the
vendor-first-seen to workflow-start interval, every search attempt, and the
workflow-observed latency of observation persistence, download, dense hash,
vision, promotion, terminal persistence, and frontend dirty-signal
publication. Each candidate line is correlated by event, fixture, tweet, and
search attempt; recovered candidates are explicit. The measurements use
Temporal's deterministic clock and structured replay-aware logs only. They do
not add activities, alter retry policies, gate decisions, or change the
Selector's serialized ownership rules. Activity-stage duration therefore
includes task-queue admission and retry backoff by design.

**Twitter feed-classification contract (FF-051 + FF-061).** Every discovery attempt
sends the same broad Latest query and the configured local age cutoff; the
workflow does not add server-side time operators or grow the age window across
attempts. Search measurement lines record `max_age_minutes`, `result_state`,
`stop_reason`, `scrolls`, `initial_articles`, `tweets_parsed`, and
`video_tweets`. An explicit X empty state is usable. A `feed_timeout` is
`unknown_timeout` and does not advance the logical attempt. The HTTP client
records the bounded final route/title, selector bits, SearchTimeline
status/failure, and rate headers; no bodies or credentials are retained.

**Cancellation contract (FF-015).** Producer cancellation from an activity or
the between-attempt `workflow.Sleep` terminates the producer and records its
error while a deferred close always marks the search side done. The consumer
returns any `workflow.Await` error instead of awaiting the canceled context
again. Cancellation therefore closes the workflow without another search,
another child spawn, or `finalizeEvent`. The monitor's event-removal
transaction owns downstream-checklist closure, and its destroy/release path
owns cleanup for this case.

**Failed-execution recovery contract (FF-007).** The monitor may start a new
run under the same deterministic Workflow ID only when the prior run closed
unsuccessfully. EventWorkflow has no outer execution timeout: its attempt loop
is finite, while each activity and Video child retains its own timeout. Before
new work starts, the replacement run loads active persisted assets, the
monotonic `attempts_completed` and `unavailable_attempts` checkpoints plus the
latest classified search evidence from downstream metadata, and every
persisted candidate with its full evidence. Terminal candidates seed
exclusions; candidates still observed/pending are re-driven and become
in-flight in workflow memory. Search resumes at the first uncompleted usable
attempt. Unavailable probes advance only their own budget. The checklist remains
open until the replacement run reaches normal finalization.
A Temporal change marker keeps executions started before FF-007 on their old
command sequence; every new or replacement execution records version 1 and
uses recovery. FF-034 independently versions the evidence-carrying terminal
UPSERT so retained histories keep their original activity commands.

**Historical candidate-repair contract.** A completed EventWorkflow is not
reset and its Temporal history is not asked to repeat business work. The
guarded `scripts/replay_clock_rejects` operation registers a new deterministic
workflow/checklist identity, checkpoints discovery at the configured maximum,
and moves only its exact terminal selector back to pending. EventWorkflow then
restores the full candidate and asset state, re-drives those pending rows, and
performs no fresh search. Candidate terminal UPSERT keeps the previous verdict
under `outcome_detail.replay`. The runner processes events sequentially and
requires a closed checklist, the original selected count, and zero selected
pending rows after each workflow. See the
[historical repair decision](../decisions/2026-08-19-historical-candidate-repair-reuses-event-workflow.md).

**Stale-running recovery contract (FF-025).** Each duplicate start describes
the current Temporal execution. The process records the exact run ID plus its
history-length and state-transition counters. Termination is permitted only
after the same run remains `RUNNING`, both counters remain unchanged for the
entire quiet bound, total run age also exceeds that bound, and no newer
activity heartbeat exists. The default 30-minute bound grows to twice the
configured attempt spacing or four configured query timeouts plus five
minutes, whichever is larger. A changed run/status/counter, recent heartbeat,
malformed response, RPC failure, or exact-run termination race fails closed.
After a successful termination, the same failed-only ID starts a replacement
that restores FF-007's Postgres checkpoint. This path never marks the
checklist or fixture complete; only normal EventWorkflow finalization can do
that. Inspection and termination errors are included in ReconcileFixture's
error output while the next poll retains ownership and retries. See the
[stale-run recovery decision](../decisions/2026-08-17-stale-event-recovery-requires-progress-proof.md).

**Consumer** (`event_pipeline*.go`, serialized). Exact-byte ownership precedes
dense hashing, then two dedup stages straddle vision (#171 shipped 2026-08-09):

- **Exact claim + gate** (`onDownloadDone`, `onHashDone`; `onVideoDone` for old
  histories): the first staged candidate for an MD5 owns dense hashing. Later
  arrivals wait; success drops their staging objects and credits every vote to
  the representative without hashing them. Their terminal outcomes wait for
  that representative under FF-065. Claimant failure transfers ownership to
  the next independent staging object. An MD5 already present in a kept asset
  collapses immediately. A match against a vision-pending clip accumulates its
  vote and follower URL in memory (#180). FF-005 bounds dense
  extraction to 640-pixel-wide grayscale PNGs before Go equalizes and reduces
  to the final 9×8 dHash. FF-041 carries the algorithm, preprocessing, and
  sample interval through Temporal and Postgres; sequences with different
  versions never compare. Fewer than the primary `MinRunFrames` (30) readable
  hashes returns the deterministic `insufficient_hash_frames` content rejection
  for every byte-identical waiter without an activity retry. A hash-successful
  unique clip fires **vision** (`ValidateClip` on
  joi — screen-gate + period-aware clock).
  Validation retries transient rate-limit, capacity, unavailable, and
  infrastructure failures up to three attempts. Invalid request/auth/model and
  malformed-response classes are non-retryable; after one attempt the existing
  failure callback records `vision_error` and deletes staging (FF-012). When
  API-Football supplied no event minute, soccer footage that passes the content
  gates remains unverified rather than becoming a false wrong-clock reject
  (FF-031). Each model frame carries a nullable visible-period enum. Structured
  clock normalization supports both continuous and reset-per-period scorebugs;
  exact displays with two structurally valid conventions retain both readings
  (`45:xx 2H` as 45/90 and `15:xx ET2` as 120/105);
  explicit period conflicts reject, while a plausible relative interpretation
  without visible period evidence can only soft-keep as unverified (FF-057).
  Clock rejection persists all raw frame observations plus their normalized
  readings for post-hoc diagnosis.
  Perceptual dedup is deliberately NOT here: a clip's verified/unverified
  category is unknown until vision, and md5-identical bytes are trivially the
  same category.
- **Post-vision** (`onVisionDone`, `dedupAndPromote`): a rejected clip is
  dropped; a verified/unverified clip runs **category-scoped perceptual dedup**
  (`matchAssets` — same pool only, ALL matches, dHash isn't transitive). The
  replay-safe tiered policy accepts either 27 of 30 aligned frames at Hamming
  ≤12 or 45 of 50 at Hamming ≤16. The 30-frame route remains hash admission and
  fallback for shorter sequences; a historical config result with no sustained
  fields disables only the 50-frame route. Dedup then runs
  which-to-keep. New histories send the entire accepted cluster to
  `CommitClipPlacement`: candidate outcome and asset attribution, newly
  credited popularity, conflict-safe asset/share creation, and bridged-loser
  supersession commit under one event-locked Postgres transaction. A unique or
  better cluster winner uses a deterministic asset UUID and S3 key; the
  activity copies staging before the transaction. An existing winner receives
  only previously uncredited source votes. Supersession moves all loser
  candidate credits to the winner, merges loser popularity exactly once,
  retires loser shares, and returns loser objects for best-effort reclaim.
  Staging deletion remains the required idempotent activity tail. A durable
  deterministic asset row proves the destination copy preceded it, so a retry
  skips an impossible second source copy. FF-067 also reads the event's removed
  state under this same row lock before any public mutation. A placement that
  loses to VAR records uncredited cluster members as
  `rejected/event_removed`, creates no asset/share/popularity change, deletes
  both its deterministic destination and staging key, and returns a typed
  non-public result. A placement that commits first remains owned by the
  monitor's subsequent `DestroyEvent` teardown.
- **Compatibility:** `ff-066-atomic-clip-placement` version 1 selects that path.
  Older histories retain independent `PromoteAndPersist`,
  `BumpAssetPopularity`, `SupersedeAssets`, terminal-outcome, and
  `RebalanceRanks` commands. The old activities stay registered until those
  histories age out.
- **Rank:** The API derives `ROW_NUMBER()` on every read from active membership,
  timestamp verification, popularity, file size, creation time, and lexical
  share ID. New-history writes never rebalance stored rank. The compatibility
  column remains only because older Temporal histories still write it.
- **Emit** (N3): after every successful placement, including a
  popularity-only duplicate, the pipeline fires the `event.video` dirty signal
  through `livefeed.PublishEventVideo`. Publication waits for the activity's
  persistence and cleanup tail. A retry that observes an already committed
  placement still returns `Announce=true`, because the workflow never observed
  the failed activity completion and still owes invalidation. Consumers refetch
  current state, so an extra signal from an external re-drive is harmless. A
  placement rejected by the FF-067 removal gate does not emit. VAR
  `DestroyEvent` also does not emit; the event disappears through the parent's
  `fixture.update` refetch.

### Dedup keeper selection versus public ranking

These are separate policies over different sets. Do not merge their criteria:

| Policy | Set being ordered | Comparator | Effect |
|---|---|---|---|
| Dedup keeper selection | Perceptually matching clips in the same verified or unverified pool | [`IsUpgrade`](../../internal/domain/video/quality.go): meaningfully longer capped duration, then bits per pixel with an anti-churn margin, then resolution; incumbent wins ties | Chooses which bytes and asset identity survive a duplicate cluster. It never uses popularity. |
| Public ranking | Distinct active shares for one event after dedup | [`CompareShares`](../../internal/domain/video/rank.go): verified, popularity, file size, older creation time, then share ID | Assigns the frontend order. It does not supersede assets or decide whether two clips are duplicates. |

When a better duplicate supersedes an incumbent, placement merges the loser's
popularity into the keeper, moves candidate attribution, and retires the
loser's shares. Read-derived rank immediately reflects the surviving active
set. This preserves accumulated source votes without allowing popularity to
keep inferior duplicate bytes. Candidate attribution makes the vote transfer
and direct duplicate credit retry-idempotent (FF-011/FF-066).

On completion—and only after every workflow-owned candidate is durably
terminal—`finalizeEvent` marks the `event_downstream_workflows` row
complete with an `outcome_class` (the pg `workflow_type` stays `'discovery'` —
the internal downstream label). `AssetsKept` is the LIVE count (`len(p.assets)`
— supersede removes losers), not cumulative promotes. Methodology + rationale:
[decisions.md 2026-08-09](../decisions.md) + [historical video-dedup proposal](../design/proposals/video-dedup/);
promotion retry and cleanup contract in
[`2026-08-16-promotion-retries-complete-durable-tail.md`](../decisions/2026-08-16-promotion-retries-complete-durable-tail.md);
history in [audit-2026-08-05](../design/audits/audit-2026-08-05.md) Tier-1 #1.

**Per-event Firefox fleet binding (#160, gated on `FleetEnabled`; live in prod).**
When `GetDiscoveryConfig` returns `FleetEnabled=true`, the producer derives
`instanceAddr := fleetactivity.InstanceAddr(EventID)` — a pure function of the
event ID, no registry lookup — and threads it through every
`SearchTweetsInput.InstanceAddr`, so this event's searches hit its own dedicated
Firefox (provisioned back at debounce count=1). Empty when disabled → searches
fall back to the shared twitter service. `finalizeEvent` calls
`ReleaseFirefox(EventID)` on normal completion when `FleetEnabled`, the
happy-path teardown; the monitor's Step 4.5 release covers an event that never
reaches finalize (decay/VAR cancellation). Both are idempotent.
