# Architectural decision compatibility index

> **Compatibility surface.** All decisions through 2026-08-16 moved to the
> [frozen archive](./decisions/archive-through-2026-08-16.md). The headings
> below intentionally preserve the old `decisions.md#...` anchors. Start at
> the [decision routing index](./decisions/README.md) for current work.

Do not add decision bodies here. New decisions are individual files under
`docs/decisions/`; add one compatibility heading here only when an old-style
`decisions.md#...` anchor is needed.

## 2026-08-16 — Post-cutover authority moves from the rebuild plan to as-built truth

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-16--post-cutover-authority-moves-from-the-rebuild-plan-to-as-built-truth)

## 2026-08-16 — Firefox fleet ownership follows the Compose-selected network (FF-001)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-16--firefox-fleet-ownership-follows-the-compose-selected-network-ff-001)

## 2026-08-16 — Alias resolver TORN OUT (Wikipedia→Wikidata pipeline deleted)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-16--alias-resolver-torn-out-wikipediawikidata-pipeline-deleted)

## 2026-08-16 — Twitter query: disconnect resolved aliases, fix abbrev, strip generational suffix

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-16--twitter-query-disconnect-resolved-aliases-fix-abbrev-strip-generational-suffix)

## 2026-08-15 — Twitter search query: distinctive-terms, not OR-everything

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-15--twitter-search-query-distinctive-terms-not-or-everything)

## 2026-08-15 — Vision clock-reject records the detected minute (#181)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-15--vision-clock-reject-records-the-detected-minute-181)

## 2026-08-15 — Twitter cookie write-back: dir mount + group perms (two silent layers)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-15--twitter-cookie-write-back-dir-mount--group-perms-two-silent-layers)

## 2026-08-15 — LLM_CHAT_CONCURRENCY_CAP=2 (per-process cap × worker replicas)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-15--llm_chat_concurrency_cap2-per-process-cap--worker-replicas)

## 2026-08-15 — #199 event mutable-field refresh on reconcile (late assists, VAR minute)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-15--199-event-mutable-field-refresh-on-reconcile-late-assists-var-minute)

## 2026-08-15 — #181 per-candidate discovery outcomes persisted (surfacing forensics)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-15--181-per-candidate-discovery-outcomes-persisted-surfacing-forensics)

## 2026-08-15 — NATS subject scheme: environment is a subject token (`found-footy.<env>.<topic>`)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-15--nats-subject-scheme-environment-is-a-subject-token-found-footyenvtopic)

## 2026-08-15 — Free-text `/search` endpoint + assist capture (competition / team / scorer / assist)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-15--free-text-search-endpoint--assist-capture-competition--team--scorer--assist)

## 2026-08-14 — last_activity_at is DERIVED at read time (supersedes the event-based-bump entry below)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-14--last_activity_at-is-derived-at-read-time-supersedes-the-event-based-bump-entry-below)

## 2026-08-14 — last_activity_at is event-based, not poll-based (frontend recency sort)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-14--last_activity_at-is-event-based-not-poll-based-frontend-recency-sort)

## 2026-08-14 — Composer decoupled to event_log-only (N2/N8); Kind→LogType rename deferred

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-14--composer-decoupled-to-event_log-only-n2n8-kindlogtype-rename-deferred)

## 2026-08-14 — NATS producer rebuild: the 3-subject live-feed model (supersedes the 2026-08-04 eventing shape)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-14--nats-producer-rebuild-the-3-subject-live-feed-model-supersedes-the-2026-08-04-eventing-shape)

## 2026-08-14 — Fixture DTO round-2: league country/round + penalty, and the winner P2-2 fix

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-14--fixture-dto-round-2-league-countryround--penalty-and-the-winner-p2-2-fix)

## 2026-08-14 — Read API no longer hard-depends on Temporal at boot

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-14--read-api-no-longer-hard-depends-on-temporal-at-boot)

## 2026-08-13 — Event `phase` on the read API: the layer-2 semantic contract (frontend handoff)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-13--event-phase-on-the-read-api-the-layer-2-semantic-contract-frontend-handoff)

## 2026-08-13 — Ingest partial-refresh no longer wipes unreachable leagues (audit P1-1)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-13--ingest-partial-refresh-no-longer-wipes-unreachable-leagues-audit-p1-1)

## 2026-08-13 — Schema drift guard, not migration files (audit P0-3)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-13--schema-drift-guard-not-migration-files-audit-p0-3)

## 2026-08-13 — Fleet orphan reaper + running-only cap + shared-service fallback (audit P0-5)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-13--fleet-orphan-reaper--running-only-cap--shared-service-fallback-audit-p0-5)

## 2026-08-13 — Drop the dead tweet_intent / vector / source_type surface (audit P0-4)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-13--drop-the-dead-tweet_intent--vector--source_type-surface-audit-p0-4)

## 2026-08-13 — Heartbeat, unified: shared time-based Keepalive across all four long activities (audit P0-1/2, P1-3; corrects #184)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-13--heartbeat-unified-shared-time-based-keepalive-across-all-four-long-activities-audit-p0-12-p1-3-corrects-184)

## 2026-08-13 — Track MLS (league 253); tracked-leagues consolidated to the code default

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-13--track-mls-league-253-tracked-leagues-consolidated-to-the-code-default)

## 2026-08-12 — Stale dev-DB enum silently disabled the share-state half of dedup consolidation

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-12--stale-dev-db-enum-silently-disabled-the-share-state-half-of-dedup-consolidation)

## 2026-08-12 — ffmpeg dense-extract: wire the heartbeat + give it a dedicated timeout (#184)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-12--ffmpeg-dense-extract-wire-the-heartbeat--give-it-a-dedicated-timeout-184)

## 2026-08-12 — Twitter search auth-verify trusts the login redirect, not a UI element (#185)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-12--twitter-search-auth-verify-trusts-the-login-redirect-not-a-ui-element-185)

## 2026-08-12 — Twitter scaling: per-event Firefox fleet replaces the scaler-pool (#160, ship-dark)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-12--twitter-scaling-per-event-firefox-fleet-replaces-the-scaler-pool-160-ship-dark)

## 2026-08-11 — LLM path: joi.luv gateway + gemma pin; concurrency cap 2→4 (stopgap)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-11--llm-path-joiluv-gateway--gemma-pin-concurrency-cap-24-stopgap)

## 2026-08-11 — Retention revises URL-stability: reclaim bytes, keep 410 tombstones (#176)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-11--retention-revises-url-stability-reclaim-bytes-keep-410-tombstones-176)

## 2026-08-10 — VAR destroy pipeline: cancel + revoke + reclaim (#172); prune is clip-blind (#176)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-10--var-destroy-pipeline-cancel--revoke--reclaim-172-prune-is-clip-blind-176)

## 2026-08-09 — Pending-clip popularity: count md5-dups in memory (#180)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-09--pending-clip-popularity-count-md5-dups-in-memory-180)

## 2026-08-09 — Category-scoped dedup is post-vision (shipped gate diverged; #171 rescoped)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-09--category-scoped-dedup-is-post-vision-shipped-gate-diverged-171-rescoped)

## 2026-08-07 — Doc-restructure divergence backfill (audit-2026-08-05)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-07--doc-restructure-divergence-backfill-audit-2026-08-05)

## 2026-08-06 — Worker memory: mem_limits, streamed frame extraction, ffmpeg↔mem coupling

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-06--worker-memory-mem_limits-streamed-frame-extraction-ffmpegmem-coupling)

## 2026-08-06 — Twitter scaling: one Firefox per event (supersedes pool + router)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-06--twitter-scaling-one-firefox-per-event-supersedes-pool--router)

## 2026-08-06 — Stale dev DB was the *second* reason the pipeline never produced a clip

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-06--stale-dev-db-was-the-second-reason-the-pipeline-never-produced-a-clip)

## 2026-08-05 — Quality-aware dedup winner selection (metadata score; built, not yet wired)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-05--quality-aware-dedup-winner-selection-metadata-score-built-not-yet-wired)

## 2026-08-05 — Twitter search: uncap the worker's HTTP client (a 10s cap strangled every search)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-05--twitter-search-uncap-the-workers-http-client-a-10s-cap-strangled-every-search)

## 2026-08-05 — Unknown-scorer goals: placeholder at debounce 0 + hard-delete (Python parity; corrects a Go divergence)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-05--unknown-scorer-goals-placeholder-at-debounce-0--hard-delete-python-parity-corrects-a-go-divergence)

## 2026-08-04 — API + eventing shape for cutover (Chi; timezone-agnostic; fixture-level push via JetStream)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-04--api--eventing-shape-for-cutover-chi-timezone-agnostic-fixture-level-push-via-jetstream)

## 2026-08-04 — #164c-b: EventWorkflow producer/consumer engine (the V-phase spine)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-04--164c-b-eventworkflow-producerconsumer-engine-the-v-phase-spine)

## 2026-08-03 — #164c-a: DiscoveryWorkflow → EventWorkflow rename (Option 2)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-03--164c-a-discoveryworkflow--eventworkflow-rename-option-2)

## 2026-08-03 — #164b: consumer-queue persist activities + a combine deviation

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-03--164b-consumer-queue-persist-activities--a-combine-deviation)

## 2026-08-03 — #166 schema revision: dedup moves out of the DB into workflow code

[Full decision](./decisions/archive-through-2026-08-16.md#2026-08-03--166-schema-revision-dedup-moves-out-of-the-db-into-workflow-code)

## 2026-07-28 — V/4 clip validation: vision-LLM soccer/screen gate + clock check (rungs 1–3 shipped)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-28--v4-clip-validation-vision-llm-soccerscreen-gate--clock-check-rungs-13-shipped)

## 2026-07-28 — V-phase dedup: dHash + gap-tolerant window, params empirically validated (pHash rejected)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-28--v-phase-dedup-dhash--gap-tolerant-window-params-empirically-validated-phash-rejected)

## 2026-07-27 — V-phase rung 3b: per-candidate activities (staging-split, pre-download filter)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-27--v-phase-rung-3b-per-candidate-activities-staging-split-pre-download-filter)

## 2026-07-27 — V-phase rung 3a: hard-filter + the aspect band (1.75–1.82, hard gate)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-27--v-phase-rung-3a-hard-filter--the-aspect-band-175182-hard-gate)

## 2026-07-27 — V-phase rung 2: perceptual dHash + offset-tolerant matcher (algorithm parity, not bit parity)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-27--v-phase-rung-2-perceptual-dhash--offset-tolerant-matcher-algorithm-parity-not-bit-parity)

## 2026-07-27 — V-phase rung 1: ffmpeg adapter — single-pass dense extraction, semaphore-capped

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-27--v-phase-rung-1-ffmpeg-adapter--single-pass-dense-extraction-semaphore-capped)

## 2026-07-27 — T/f syndication video download: cookieless-first; geo-restricted broadcaster clips are terminal but redundant

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-27--tf-syndication-video-download-cookieless-first-geo-restricted-broadcaster-clips-are-terminal-but-redundant)

## 2026-07-27 — V-phase orchestration: a per-event workflow owns completion (Temporal), Postgres is a mirror

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-27--v-phase-orchestration-a-per-event-workflow-owns-completion-temporal-postgres-is-a-mirror)

## 2026-07-25 — Video dedup is per-EVENT only; cross-event / per-fixture dedup is dead

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-25--video-dedup-is-per-event-only-cross-event--per-fixture-dedup-is-dead)

## 2026-07-24 — Event primary key is synthesized (team_player_type_seq); the VAR slot-shift is a known, accepted tradeoff

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-24--event-primary-key-is-synthesized-team_player_type_seq-the-var-slot-shift-is-a-known-accepted-tradeoff)

## 2026-07-24 — Club entity selection: reverted name-match, kept Wikipedia-rank (correct for the tracked roster)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-24--club-entity-selection-reverted-name-match-kept-wikipedia-rank-correct-for-the-tracked-roster)

## 2026-07-24 — Tokenizer transliterates via unidecode (romanize, don't drop); Twitter search is stroke-insensitive

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-24--tokenizer-transliterates-via-unidecode-romanize-dont-drop-twitter-search-is-stroke-insensitive)

## 2026-07-23 — Discovery filter: wall-clock-relative sliding window, not server-side time bounds

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-23--discovery-filter-wall-clock-relative-sliding-window-not-server-side-time-bounds)

## 2026-07-23 — Twitter Firefox profile: ephemeral per-container (Python-shape), not shared volume

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-23--twitter-firefox-profile-ephemeral-per-container-python-shape-not-shared-volume)

## 2026-07-22 — Query builder: D3 confirmed (no event vocabulary), sentiment_mode → video_only, own-goal invariant flagged

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-22--query-builder-d3-confirmed-no-event-vocabulary-sentiment_mode--video_only-own-goal-invariant-flagged)

## 2026-07-22 — Playwright-login validation: Twitter blocks Playwright login, raw-Firefox-subprocess fallback confirmed required

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-22--playwright-login-validation-twitter-blocks-playwright-login-raw-firefox-subprocess-fallback-confirmed-required)

## 2026-07-22 — Twitter Dockerfile: one file, WITH_VNC-gated, matches Python's shape

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-22--twitter-dockerfile-one-file-with_vnc-gated-matches-pythons-shape)

## 2026-07-21 — VNC container is opt-in (not always running)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-21--vnc-container-is-opt-in-not-always-running)

## 2026-07-21 — Twitter fleet coordination: filesystem mtime, not pg NOTIFY

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-21--twitter-fleet-coordination-filesystem-mtime-not-pg-notify)

## 2026-07-21 — NATS scope: inter-project only; pg NOTIFY for intra-project pub/sub

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-21--nats-scope-inter-project-only-pg-notify-for-intra-project-pubsub)

## 2026-07-21 — Alias entity resolution: Wikipedia CirrusSearch replaces `wbsearchentities`

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-21--alias-entity-resolution-wikipedia-cirrussearch-replaces-wbsearchentities)

## 2026-07-19 — Team alias pipeline: deterministic Wikidata, no LLM

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-19--team-alias-pipeline-deterministic-wikidata-no-llm)

## 2026-07-18 — Video share ranking derived at read time, no stored rank column

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-18--video-share-ranking-derived-at-read-time-no-stored-rank-column)

## 2026-07-16 — Downstream workflow spawn via Temporal-direct + register-on-flip (chain, not NATS)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-16--downstream-workflow-spawn-via-temporal-direct--register-on-flip-chain-not-nats)

## 2026-07-11 — Fixture completion contract via pluggable per-event workflow checklist

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-11--fixture-completion-contract-via-pluggable-per-event-workflow-checklist)

## 2026-07-11 — Split MonitorWorkflow into ActivePollWorkflow + StagingPollWorkflow

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-11--split-monitorworkflow-into-activepollworkflow--stagingpollworkflow)

## 2026-07-09 — All-lowercase canonical for enums (uniform internal representation)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-09--all-lowercase-canonical-for-enums-uniform-internal-representation)

## 2026-07-09 — Real-data enum audit: card/goal comments + Missed Penalty tracking + vendor casing reality

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-09--real-data-enum-audit-cardgoal-comments--missed-penalty-tracking--vendor-casing-reality)

## 2026-07-09 — Typed enums for API-Sports wire values (Status + EventType + EventDetail)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-09--typed-enums-for-api-sports-wire-values-status--eventtype--eventdetail)

## 2026-07-09 — Cross-workflow config centralized in WorkflowsConfig

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-09--cross-workflow-config-centralized-in-workflowsconfig)

## 2026-07-09 — Ingest regression fix: dynamic top-flight team lookup + per-day fetch + smart lookahead

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-09--ingest-regression-fix-dynamic-top-flight-team-lookup--per-day-fetch--smart-lookahead)

## 2026-07-09 — apifootball adapter: bugfixes + chunk-parallel refactor

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-09--apifootball-adapter-bugfixes--chunk-parallel-refactor)

## 2026-07-09 — API-Football docs archived + frozen reference seeded

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-09--api-football-docs-archived--frozen-reference-seeded)

## 2026-07-08 — Test corpus harness Phase 1a shipped + activity clock injection pattern

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-08--test-corpus-harness-phase-1a-shipped--activity-clock-injection-pattern)

## 2026-07-07 — Symmetric-counter debounce (Go rebuild's improvement over Python)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-07--symmetric-counter-debounce-go-rebuilds-improvement-over-python)

## 2026-07-07 — APIStatus bucketing preserves Python's SUSP/INT/PST=active

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-07--apistatus-bucketing-preserves-pythons-suspintpstactive)

## 2026-07-07 — O1e complete — schedule registered + all §5 W1 divergences realigned

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-07--o1e-complete--schedule-registered--all-5-w1-divergences-realigned)

## 2026-07-07 — O1e/a — IngestWorkflow input reshape complete

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-07--o1ea--ingestworkflow-input-reshape-complete)

## 2026-07-07 — Pre-O1e cleanup — LastPolledAt fix + Errors []string

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-07--pre-o1e-cleanup--lastpolledat-fix--errors-string)

## 2026-07-07 — Ripped `internal/errors/` stub

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-07--ripped-internalerrors-stub)

## 2026-07-07 — Working rule: living docs update in the same commit as code

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-07--working-rule-living-docs-update-in-the-same-commit-as-code)

## 2026-07-07 — Doc retro closure

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-07--doc-retro-closure)

## 2026-07-07 — Temporal adapter divergences from plan §9

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-07--temporal-adapter-divergences-from-plan-9)

## 2026-07-07 — Log-catalog generator (§11.3) not shipped

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-07--log-catalog-generator-113-not-shipped)

## 2026-07-07 — IngestWorkflow divergences from plan §5 W1

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-07--ingestworkflow-divergences-from-plan-5-w1)

## 2026-07-07 — Rebuild architecture divergences from plan §2

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-07--rebuild-architecture-divergences-from-plan-2)

## 2026-07-07 — Fixture activation triggers + staging-poll design

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-07--fixture-activation-triggers--staging-poll-design)

## 2026-07-07 — Workflow renames for Phase O

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-07--workflow-renames-for-phase-o)

## 2026-07-02 — NATS is metadata-plane only; video bytes go over HTTP

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-02--nats-is-metadata-plane-only-video-bytes-go-over-http)

## 2026-07-01 — Workspace NATS as event bus (replaces Postgres LISTEN/NOTIFY)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-01--workspace-nats-as-event-bus-replaces-postgres-listennotify)

## 2026-07-01 — Fresh rebuild in parallel, not incremental refactor

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-01--fresh-rebuild-in-parallel-not-incremental-refactor)

## 2026-07-01 — Postgres over Mongo (rebuild-context reversal)

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-01--postgres-over-mongo-rebuild-context-reversal)

## 2026-07-01 — Garage over MinIO for blob storage

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-01--garage-over-minio-for-blob-storage)

## 2026-07-01 — LLM endpoint abstracted; nexus swap is config-only

[Full decision](./decisions/archive-through-2026-08-16.md#2026-07-01--llm-endpoint-abstracted-nexus-swap-is-config-only)

## 2026-06-30 — Cross-doc linking via markdown, no `[[wiki-links]]`

[Full decision](./decisions/archive-through-2026-08-16.md#2026-06-30--cross-doc-linking-via-markdown-no-wiki-links)

## 2026-06-30 — Brain-stack (Khoj + basic-memory MCP + Obsidian vault) deprecated

[Full decision](./decisions/archive-through-2026-08-16.md#2026-06-30--brain-stack-khoj--basic-memory-mcp--obsidian-vault-deprecated)

## 2026-05 — Caddy fronts all HTTP; host ports dropped

[Full decision](./decisions/archive-through-2026-08-16.md#2026-05--caddy-fronts-all-http-host-ports-dropped)

## 2026-XX — LLM URL switched to Caddy hostname on joi

[Full decision](./decisions/archive-through-2026-08-16.md#2026-xx--llm-url-switched-to-caddy-hostname-on-joi)

## (pre-history) — Scoped deduplication by `timestamp_verified`

[Full decision](./decisions/archive-through-2026-08-16.md#pre-history--scoped-deduplication-by-timestamp_verified)

## (pre-history) — Workflow-ID arrays over counters

[Full decision](./decisions/archive-through-2026-08-16.md#pre-history--workflow-id-arrays-over-counters)

## (pre-history) — `signal-with-start` for serialized `UploadWorkflow`

[Full decision](./decisions/archive-through-2026-08-16.md#pre-history--signal-with-start-for-serialized-uploadworkflow)

## (pre-history) — Twitter alias resolution inside `TwitterWorkflow`

[Full decision](./decisions/archive-through-2026-08-16.md#pre-history--twitter-alias-resolution-inside-twitterworkflow)

## (pre-history) — 5-collection MongoDB design with `fixtures_live` as overwrite buffer

[Full decision](./decisions/archive-through-2026-08-16.md#pre-history--5-collection-mongodb-design-with-fixtures_live-as-overwrite-buffer)

## (pre-history) — Auto-scaling via dedicated scaler container

[Full decision](./decisions/archive-through-2026-08-16.md#pre-history--auto-scaling-via-dedicated-scaler-container)

## (pre-history) — Fire-and-forget child workflows with `ABANDON` parent close policy

[Full decision](./decisions/archive-through-2026-08-16.md#pre-history--fire-and-forget-child-workflows-with-abandon-parent-close-policy)

## (pre-history) — Heartbeat-based timeouts for long-running activities

[Full decision](./decisions/archive-through-2026-08-16.md#pre-history--heartbeat-based-timeouts-for-long-running-activities)
