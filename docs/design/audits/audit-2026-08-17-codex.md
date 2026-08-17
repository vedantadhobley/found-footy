# Independent codebase audit — 2026-08-17

> **Point-in-time evidence.** This audit describes `rebuild/go` at local commit
> `5a1fec7`, with production still running release `4413a5f`. Use the
> [active issue register](../../todo.md) for current status. Production was
> observed through read-only container and Loki queries; this audit did not
> mutate production.

## Result

The Go rebuild has a sound core architecture. Domain, activity, workflow, and
adapter boundaries are recognizable; Temporal work is deterministic; database
writes are usually retry-safe; memory-heavy containers have hard caps; and the
uncached test suite passes. The rebuild is materially stronger than the Python
system it replaced.

It is not yet a clean platform baseline. The most important remaining defect is
not directory layout: candidate evidence and terminal outcome are separate
best-effort writes. A parent workflow can finish while a candidate row remains
`pending` or never existed. That weakens recovery and discards the tweet
evidence needed to solve FF-003. Configuration also lacks semantic validation,
the API read window grows without a bound, and several resource and lifecycle
contracts depend on today's untracked production values.

Do not perform a big-bang package move. First restore reproducible engineering
gates, then repair the correctness invariants, then split oversized files along
the boundaries the fixes establish. Moving Temporal workflow code before those
boundaries are stable adds replay and registration risk without fixing a user
problem.

## Scope and method

This was a code-first audit. Existing audits and plans were used as hypotheses,
not accepted as current truth.

- Read the project authority map, target design, as-built ledgers, active issue
  register, schema, Compose files, build files, and archived Python behavior.
- Read the Go tree across `cmd/`, `internal/domain`, `internal/activity`,
  `internal/workflow`, `internal/infra`, configuration, observability, scripts,
  and test harnesses.
- Revalidated every unresolved finding in the 2026-08-13 and 2026-08-15 audit
  intake against the current branch.
- Ran the full uncached Go suite, race suite, vet, module verification, Compose
  parsing, formatting inventory, module-tidy diff, lint target, and a short-test
  coverage sample.
- Read the live production release identity and startup state. Queried the prior
  48 hours of found-footy logs for discovery, Twitter, ffmpeg, API-Football,
  NATS, authentication, and browser failure evidence.
- Compared project deployment and identity choices with the current
  `vedanta-dhobley` decisions and drafts. Cross-project proposals are recorded
  as coordination gates, not silently established here.

The audit did not load-test Twitter, download public clips, mutate a database,
exercise VNC login, or redeploy production.

## Verification baseline

| Check | Result | Meaning |
|---|---|---|
| Full `go test -count=1 ./...` | Pass | Unit, Temporal, scenario, and Postgres integration paths passed uncached. |
| `go test -race ./...` | Pass | No race was detected in the exercised paths. |
| `go vet ./...` | Pass | Standard static checks passed. |
| `go mod verify` | Pass | Downloaded module contents match their hashes. |
| Dev/prod Compose parse | Pass | Both files are syntactically valid with the current environment. |
| `make lint` | Fail before analysis | Mutable `golangci-lint:latest-alpine` now requires v2 config; the repository carries v1 syntax. |
| `gofmt -l` | 31 files | Formatting is not enforced by the current commit or push gates. |
| `go mod tidy -diff` | Non-empty | Two used modules are classified as indirect instead of direct. |
| Short-test coverage sample | Uneven | Core domain and workflow packages are strong; binary composition, NATS, S3, Temporal, and discovery activity surfaces are weak or integration-only. |

The hooks run `test-short` before commit and the full suite before push. They do
not enforce format, tidy, lint, or vet. Passing tests therefore do not imply a
reproducible clean tree.

## Production evidence

The read-only production check found the API, Twitter service, and both worker
replicas healthy on immutable release `4413a5f`. No active fixture was present
during the current-release sample.

The longer Loki sample contained:

- 1,673 successful Twitter searches and 390 search warnings. Most warnings were
  historical per-event DNS misses followed by shared-service fallback, before
  the current fleet lifecycle release.
- ffmpeg dense extraction killed at about 100 seconds on August 15 and 16,
  confirming FF-005 rather than a semaphore-wait theory.
- one production event browser entering `twitter.auth_expired` on August 16.
  Dev event browsers showed the same stale cookie. No Twitter rate-limit,
  interstitial, HTTP 429, or `browser_failed` event appeared in that query.
- clean-shutdown `nats_disconnected` warnings with an empty error, which pollute
  failure metrics and logs.
- two live worker processes each configured with LLM concurrency `2`, so the
  current aggregate equals joi's four slots. That safety value exists in the
  untracked production environment, not in a checked release invariant.

Raw counts span several code generations. They are evidence for failure modes,
not current-release error rates.

## Accepted findings

### P1 correctness

#### FF-034 — candidate evidence and terminal state are not one invariant

`EventWorkflow` waits for `StoreCandidate` before it starts each video child,
but deliberately starts the child when that write fails. Later,
`recordOutcome` performs a synchronous activity and discards its error.
`RecordCandidateOutcome` uses `UPDATE`, accepts zero affected rows, and describes
that case as a no-op. The parent can therefore complete with a missing candidate
row or a durable `pending` row.

This is the observed “pending after parent workflow” class. It is also the
architectural blocker for FF-003: tweet text, username, discovery age, query,
attempt, and event context are stored separately from the video and vision
contracts, which pass only a URL and video-derived data.

The fix should introduce a workflow-owned `CandidateEvidence` contract and an
idempotent terminal UPSERT. Observation persistence must not block clip launch;
terminal persistence must be durable before the parent reports completion.
Recovery must distinguish observed, in-flight, and terminal candidates without
inferring state from a best-effort side table.

### P2 correctness and operability

| ID | Finding | Evidence and required boundary |
|---|---|---|
| FF-035 | Startup configuration is parsed but not semantically validated. | `DISCOVERY_MAX_ATTEMPTS > 20` violates the schema after attempt 20; negative values can produce successful zero-search runs; non-positive fleet capacity can wait until timeout. The template also contains obsolete and unconsumed keys. Add per-binary validation plus a checked env/Compose contract. |
| FF-036 | The API full window is unbounded and assembled through N+1 reads. | `ListByState(completed)` has no cutoff or limit. Shared URLs act as durable tombstones, so clip-bearing completed fixtures are never pruned. Separate the public read window from durable history, then batch fixture/event/video assembly. |
| FF-037 | Expensive work shares process-local admission and one Temporal queue. | LLM waiters occupy general activity slots; dense hashing shares a lane with probe/frame work; aggregate LLM safety depends on `2 × 2` in live env. Split work queues and ffmpeg lanes. Global inference admission belongs at the shared inference service. |
| FF-038 | Firefox fleet admission and ownership are not centralized. | Count-then-create has a capacity race; provisioning is sequential; malformed labels can evade reaping; the worker holds the raw Docker socket. Keep event-scoped names for diagnosis, but place capacity, leases, and Docker access in one controller reached through HTTP. |
| FF-039 | Process lifecycle and observability contracts differ by binary. | Twitter bypasses shared bootstrap, lacks Prometheus and signal-driven graceful shutdown; API shutdown config is overridden by bootstrap's hardcoded closer deadline; `error_class` is usually empty because callers emit only `err`; readiness is liveness-only. Converge lifecycle semantics without forcing identical internals. |
| FF-040 | Live fixture reconciliation does not own every mutable field atomically. | Active polling refreshes status, clock, score, winner, and penalty, but not team display fields or league name/country/round/season/kickoff. Staging and active polls can both activate the same row and duplicate audit events. Define mutable ownership and use an atomic state transition. |
| FF-041 | Stored perceptual hashes have no version or minimum viable length. | Empty or short successful hash sequences can be promoted while structurally unable to match a 30-frame window. Stored bytes omit algorithm, interval, and preprocessing version. Version and validate the hash contract before FF-005 changes preprocessing. |
| FF-042 | The engineering toolchain is not reproducible. | Lint uses a mutable latest image and is currently broken; Air is installed at latest; 31 files fail format; module classification drift exists; hooks omit these gates. Pin tool versions, migrate lint config, format/tidy once, then enforce non-mutating checks. |
| FF-043 | The public API has an unused mandatory NATS dependency. | `cmd/api` connects to NATS and refuses startup on failure but never publishes or subscribes. Remove the dependency until the read surface actually needs it. |
| FF-046 | Ancillary persistence blocks the serialized workflow consumer. | Candidate outcomes, staging deletes, live-feed publish, popularity, promotion, and supersession call `.Get` inside selector callbacks. Preserve serialization only for in-memory dedup state; move independently retryable effects behind explicit futures or a durable persistence boundary. |
| FF-048 | Share minting lacks a database uniqueness invariant. | Check-then-insert is usually serialized by one event workflow but is not safe under activity timeout/retry or recovery. Add `(event_id, asset_id)` uniqueness and atomic insert semantics after ordered migrations exist. |

### P3 bounded improvements

| ID | Finding | Completion boundary |
|---|---|---|
| FF-044 | Recovery retries `StartWorkflow` and `Describe` for every triggered-but-not-complete event each 30-second cycle, including healthy runs. | Replace polling churn with a durable scheduled supervisor or a next-check lease. |
| FF-045 | Dormant surfaces and oversized files obscure real ownership. | Remove superseded alias/resolver, webhook/outbox, session, tracing, and registration residue after a caller/schema proof; split large composition and pipeline files in place. |
| FF-047 | Empty tracked-team state still burns lookahead vendor calls whose results are discarded. | Short-circuit before fixture discovery and emit one explicit degraded-state signal. |
| FF-049 | Documentation is routed but not fully segmented to the shared size standard. | Split the current 618-line orchestration ledger. Route the 2,869-line Python functional spec and 604-line video-dedup proposal through topic indexes without changing frozen claims. |

Existing FF-008, FF-009, FF-010, FF-011, FF-013, and FF-024 remain valid.
FF-013 should establish ordered migrations before FF-048 or other schema
constraints land. FF-011's retry-unsafe popularity update remains independent
of the candidate redesign.

## Prior-audit disposition

| Prior source | Disposition |
|---|---|
| `AUD-0815-MUTABLE` | Confirmed as FF-040; event assist/minute/detail refresh is already fixed, fixture metadata remains. |
| `AUD-0815-FLEET-TOCTOU`, `AUD-0813-P2-7` | Confirmed and consolidated into FF-038. Current sequential provisioning mitigates the capacity race. |
| `AUD-0815-SHARE-TOCTOU` | Confirmed as FF-048; latent under normal serialization. |
| `AUD-0815-ROT` | Confirmed as FF-045. |
| `AUD-0813-P2-1` | Confirmed as FF-036. |
| `AUD-0813-P2-5` | Confirmed as FF-046. |
| `AUD-0813-P2-6`, `P2-9` | Confirmed as FF-037. |
| `AUD-0813-P2-8` | Confirmed as part of FF-034. |
| `AUD-0813-P2-13`, `P3-16` | Confirmed as part of FF-039. |
| `AUD-0813-P3-2`, `P3-13` | Confirmed as FF-041. |
| `AUD-0813-P3-6` | Confirmed as FF-044. |
| `AUD-0813-P3-7` | Confirmed as FF-040. |
| `AUD-0813-P3-9` | Confirmed as FF-047. |
| `AUD-0813-P3-10` | Superseded by the alias-resolver removal; residue belongs to FF-045. |
| `AUD-0813-P3-12` | Confirmed as part of FF-035. |
| `AUD-0813-P3-17` | Closed: the configured value now reaches Temporal's worker stop timeout. The separate API closer issue is FF-039. |
| `AUD-0813-P3-4` | No bug accepted. Dedup winner quality and public rank serve different policies; document before changing either. |
| `AUD-0813-P3-14` | True but bounded at current event sizes. Measure before replacing the simpler full rebalance. |
| `AUD-0813-CF-153` | Operator contract, not a software defect yet. Auth expiry was observed; capture a full VNC write-back and fleet-propagation exercise on the next real expiry. |
| `AUD-0813-CF-175`, `CF-179`, `CF-SLO`, `CF-SCORE` | Product or measurement decisions, not current correctness bugs. Preserve as deferred decisions. |
| `AUD-DESIGN-COVERAGE` | Do not impose a global percentage. Add boundary-specific tests as each finding is fixed. |
| `AUD-DESIGN-LOG-CATALOG` | Reject for now; typed vocabulary source is adequate. |
| `AUD-DESIGN-TRACING` | Defer until a concrete cross-service diagnostic requires propagation. Remove the empty stub under FF-045 if still unused. |
| `AUD-TWITTER-RATE-LIMIT` | No supporting event in the 48-hour production sample. Keep classification telemetry; do not design speculative backoff. |

## Structure assessment

Keeping `_test.go` files beside production files is standard Go practice. It
supports white-box tests, package-local helpers, and the normal `go test ./...`
toolchain. Move test fixtures to `testdata/` when needed, and split very large
test files by behavior, but do not create a mirrored top-level unit-test tree.

The current package taxonomy is good enough to preserve:

```text
cmd -> bootstrap/composition
workflow -> activity contracts
activity -> domain ports + adapters
infra -> external systems
domain -> deterministic policy
```

The problems are boundary leaks and file size, not the top-level taxonomy.
Apply these local changes after the related functional fix:

1. Make `cmd/worker` a small entry point and move composition and schedule
   reconciliation into `internal/app/worker`.
2. Put workflow inputs and `CandidateEvidence` in a neutral contract package.
   Do not place workflow input types in an activity package to break cycles.
3. Split monitor activities into activation, reconciliation, completion, and
   emission files inside the same package.
4. Split the event pipeline into candidate intake, validation/dedup, and durable
   effects while keeping one explicit state owner.
5. Split large test files by the same behaviors; keep them beside the package.
6. Delete empty speculative packages and stale package docs only after caller
   searches prove they are unused.

The documentation normalization is real but incomplete. The active register,
decision index, and audit routing are now bounded and authoritative.
`docs/orchestration.md`, the Python functional reference, and the video-dedup
proposal still exceed the workspace's near-500-line standard. FF-049 owns that
segmentation so it does not get mixed into runtime changes or silently lost.

Do not rename registered workflows or activities as part of cosmetic cleanup.
Use explicit stable Temporal registration names before a move that could change
runtime identity, and replay representative histories before rollout.

## Cross-project standardization boundary

Found-footy should be a pilot consumer of workspace standards, not the place
where they are invented implicitly.

- Adopt stable `(project, env, service)` labels when the dhobley validator lands.
  Container names remain diagnostic instance names; automation must use labels.
- Keep the current four-segment semantic NATS subjects until dhobley resolves
  the subject/envelope identity question. Do not add service identity to the
  subject locally.
- Align metrics identity labels and primary-port exposure with the recorded
  Prometheus decision as part of FF-039.
- Do not convert the two Compose files into a base-plus-overrides layout until
  the dhobley pilot contract is signed off. The direction is reasonable, but it
  remains a cross-project choice.
- Registry-built production images, standard Compose metadata, host-port
  validation, and workspace path migration belong to the shared rollout. Track
  found-footy's adoption there rather than creating a private variant here.
- The Firefox controller is a project-local service boundary because it owns
  per-event browsers and Docker access. Its reusable lesson is the identity and
  lease pattern, not a workspace-wide browser service.

Known adoption differences are explicit:

| Current found-footy state | Workspace direction | Disposition |
|---|---|---|
| Compose has no `com.vedanta.project/env/service` labels. | Stable service identity is label-based; names identify instances. | Adopt through the shared validator pilot; never add name parsing. |
| Dev publishes Temporal `7233` on the host. | Host ports require an exception; service HTTP uses Caddy and the proxy network. | Remove the binding if host tools do not need it, otherwise record the exception centrally. |
| Dev and prod are two complete Compose files using one `.env`. | The standardization work is evaluating a base plus environment overrides and explicit env files. | Wait for the shared contract; do not create a found-footy-only variant. |
| Production builds application images from local source. | The recorded destination is immutable registry images. | Migrate with the shared registry rollout, preserving the existing release-identity checks. |
| Metrics use a separate `:8080` listener and omit workspace identity labels. | The recorded metrics contract uses the primary HTTP surface and standard identity. | Resolve under FF-039; workers still need an operations-only HTTP surface. |
| Worker Docker access depends on host group `984`. | Host-specific socket ownership should not leak into application workers. | Remove it when FF-038 moves Docker access behind the fleet controller. |

## Implementation order

1. **Restore trust in the tree:** FF-042, then FF-035 and FF-043. These are
   small, low-risk slices that make later refactors measurable.
2. **Repair durable correctness:** FF-034, FF-013, FF-048, FF-011, and FF-040.
   Candidate terminal state comes before semantic validation.
3. **Bound reads and recovery:** FF-036, FF-044, FF-009, FF-010, FF-008, and
   FF-024.
4. **Isolate expensive work:** FF-037 and FF-038, coordinated with the shared
   inference admission direction.
5. **Improve candidate quality:** FF-003, then FF-041, FF-005, and FF-004.
   Hash versioning and a regression corpus must precede preprocessing changes.
6. **Converge and simplify:** FF-039, FF-046, FF-047, FF-045, then FF-049.
   Split files while deleting proven residue; avoid a standalone rearrangement
   project.

Each slice should include code, invariant-level tests, affected as-built docs,
and an append-only decision only when behavior or architecture actually changes.
Production deployment and any production data repair remain separate,
explicitly approved operations.
