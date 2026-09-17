# Direct-support selection rollout — 2026-09-13

## Release and scope

The user approved pushing and deploying
`8c836e0f41cc0e168ca4fe0ff3076dfca8c56793`, then separately approved its four
production migrations. `origin/feat/event-update` was verified at that SHA.
The normal commit and pre-push gates passed without bypassing hooks.

The release combines [accepted validation retention (FF-093)](../decisions/2026-09-12-accepted-validation-follows-exact-assets.md),
[reversible selection (FF-081)](../decisions/2026-09-12-selection-commits-with-incoming-placement.md)
and [direct-support popularity](../decisions/2026-09-13-popularity-counts-direct-support.md).
Incoming placement, restoration, routing, scores and shares commit together.
Public popularity counts each accepted source once per directly matching keeper;
scores are not additive across an event. Quality comparison, dHash thresholds,
clock admission, search strings, FF-078 visibility and API/NATS contracts stay
unchanged. This does not solve whole-video replacement or picture-quality policy.

Old workflow histories retain their recorded behavior. No historical rescore,
workflow reset, replay, repair, frontend change or production config edit was
performed. The new path does not fabricate missing historical acceptance.

## Execution

Ferrán Torres's event `1fd53d9a-f03e-4a23-a2c1-804c802cfd95` in fixture
`1552757` completed at 19:09:05 UTC, after fifteen usable searches, with no
pending candidates. The release waited for discovery and browser work to drain.
It did not cancel workflows or stop event browsers to clear the deployment gate.

The main checkout contained unrelated crop, clock, quality and repair work.
The release therefore used the clean detached checkout
`/tmp/found-footy-release-8c836e0.2HNy7y` and an unchanged project `.env` symlink.
That checkout remains available for the deployed Compose project. Prior release
checkout and images were retained; unrelated research did not enter the images.

The unchanged `make migrate-prod` and `make deploy-prod` ran sequentially.
Invocation-only wrappers capped builds at 4 GiB, used sequential legacy Compose
builds, and ran the isolated Twitter storage smoke before recreation. The
migrator retained its 1 GiB cap; the non-root permissions smoke used 128 MiB.
Browser and running `EventWorkflow` checks guarded both migration and recreation.
The repo's existing browser checks remained enabled.

The migration gate committed and verified this chain at 19:16:47 UTC:

| Migration | Checksum prefix |
|---|---|
| `20260912_01_retain_accepted_validation.sql` | `157ca37876ae` |
| `20260912_02_add_reversible_selection.sql` | `1e193cfac17f` |
| `20260912_03_index_event_validation_lookup.sql` | `045b9821b0e7` |
| `20260913_01_enable_direct_support.sql` | `f97e97d795e1` |

The resulting schema hash is
`5f1ea2f578c0c4410eb34c7ad3b1c5d84f59fb938c10b7c9e575b1f5f791c35b`.
The chain creates the validation and selection-receipt stores, adds an event
lookup index, and permits direct-support receipt versions. It does not rewrite
historical scores, shares, candidates or receipts. Older application images
cannot simply restart against this ledger: startup schema verification requires
a matching release. Do not remove migration ledger rows to bypass that gate.

Application build identity: `2026-09-13T19:16:47Z`. Both workers, API and static
Twitter started at 19:19:26 UTC (15:19 Eastern). The release exited zero with
`release verified` for the exact SHA. VNC remained stopped. Postgres, Temporal
Postgres, Temporal, Temporal UI and Garage retained their container IDs and
startup times. No production media, volume or image was deleted.

## Verification

- All four application processes exposed the expected SHA, image tag and build
  timestamp, with zero restarts. Both worker and API health checks passed.
- The schema ledger contained all four expected names/checksums and the expected
  final schema hash. The release migration gate and application startup checks
  passed.
- Static Twitter reported healthy, verified and idle. Cookie reload, live
  authentication and cookie backup succeeded after restart, without reported
  errors.
- The isolated storage smoke verified disposable search profiles, persistent
  cookie binding and the manual login profile. It removed only its own test
  containers and volumes. Worker socket/scratch permission checks passed.
- Selected application startup logs contained no WARN/ERROR/FATAL records.
  Active fixture poll timestamps advanced to 19:24 UTC after deployment.
- Targeted REST reads returned the active Brest–PSG, Sassuolo–Juventus and
  Sociedad–Atletico fixtures. Ferrán's event remained `phase: complete`.
  Its four active, unreclaimed shares had popularity 21/1/1/1; the unchanged
  FF-078 filter returned the 21-support clip, `s_70660c27fd14`.
- No running discovery workflow or scoped event browser existed at the first
  post-release check. Both new evidence tables were still empty at 19:24 UTC,
  consistent with no natural accepted candidate on the new path yet.

## Initial quiet-window checkpoint

At the 19:34 UTC follow-up, repeated read-only checks still found no event first
seen after the application restart. Both new evidence tables remained empty.
The three live fixtures had current poll timestamps at 19:34:30 UTC; Sassuolo–
Juventus had reached halftime, while Brest–PSG and Sociedad–Atletico remained
in the first half. Application logs since restart contained no WARN/ERROR/FATAL
records. This confirms continued polling, not natural execution of selection.

The next accepted event must be checked for both Temporal version markers,
own event/MD5 validation records and `direct-restoration-support-v2` receipts.
Reconcile exact-source observations with each selected clip's direct count;
do not sum selected popularity as an event total. Distinguish an ordinary
placement receipt from a nonempty `Plan.Restored`, and a successful backend
publish from confirmed frontend receipt. None of those natural cases was
observed in this follow-up, so they remain unvalidated.

## Natural-event validation — 21:10–21:17 UTC

Eight post-release goals completed naturally. Every history records version 1
of both `ff-093-accepted-validation-evidence` and `ff-081-reversible-selection`,
fifteen usable searches, `assets_surfaced`, and no outage-budget exhaustion.
The last workflow, Simeone 90+5′, completed at 21:12:32 UTC. All 734 candidate
rows were terminal: 153 accepted, 462 rejected and 119 failed downloads.

| Fixture | Goal | Accepted MD5s | Selected clips | Public popularity, in rank order |
|---|---|---:|---:|---|
| Sassuolo–Juventus `1550124` | Esposito 65′ | 5 | 1 | 22 |
| Sassuolo–Juventus | Zhegrova 82′ | 5 | 1 | 29 |
| Sassuolo–Juventus | Bowie 89′ | 7 | 3 | 22 |
| Sassuolo–Juventus | Zhegrova 90+1′ | 6 | 3 | 17 |
| Sassuolo–Juventus | Adzic 90+4′ | 7 | 3 | 8, 10 |
| Sociedad–Atletico `1570380` | Grimaldo 84′ | 3 | 1 | 6 |
| Sociedad–Atletico | Jonathan David 90′ | 5 | 1 | 20 |
| Sociedad–Atletico | Simeone 90+5′ | 3 | 2 | 1, 13 |

There are 41 accepted validation records, one per retained MD5, including nine
never-public variants. All 153 accepted sources have observed and credited
asset identities and their observed asset's own validation. No event/fixture/MD5
scope mismatch exists. Every record retains three frames and the actual model
ID `gemma-4-12b`; JSON records range from 1,355 to 1,820 bytes. One accepted
Adzic evaluation records successful vision activity attempt 3. This proves
accepted-evaluation retry provenance, not a placement-receipt replay.

All 122 selection receipts use `direct-restoration-support-v2`, without a skip:
41 new-variant placements and 81 later exact-MD5 placements. Latest receipt
scores agree with current asset rows. An independent, read-only calculation
decoded the retained big-endian hashes, applied each fixed window at every
possible offset (12/27-of-30 or 16/45-of-50), enforced event, own-verification
and hash-version scope, and counted exact observations once per matching keeper.
All fifteen selected clips' scores match. No observed variant supports multiple
current keepers, and no receipt contains a restoration; those cases remain open.

The API returns ten clips after unchanged singleton filtering. Adzic's 8 precedes
10 because the 8-support clip is timestamp-verified; Simeone's verified singleton
precedes the unverified 13-support clip and remains visible under FF-078. These
are intended verification-priority outcomes, not failed popularity sorting.

Durable Temporal history records all 122 placement activities and 130
`PublishEventUpdate` activities completed: one publication per placement plus
one completion update per event. No placement, publication or terminal vision
activity failure occurred. This proves backend publication success and REST
consistency, not reception or rendering by a particular React client.

The retained stdout subset contains 81 placement measurements: median 29 ms,
p95 53 ms, maximum 100 ms, including workflow-observed scheduling/retries.
This is a partial sample, not all 122 placements or an isolated planner CPU
benchmark. Combined before/result receipt JSON totals 267,182 bytes; the largest
receipt uses 4,851 bytes. Larger event graphs and Temporal payload growth still
need measurement.

### Remaining upstream failures

- All 119 terminal download failures are classified `cdn_download/forbidden`
  (Twitter CDN HTTP 403), the existing FF-089 work. These are candidate failures,
  not 119 proven unique lost clips.
- Ten unavailable Twitter observations across four goals recovered; none
  consumed the fifteen-usable-search budget. Browser errors were
  `NS_BINDING_ABORTED` or `NS_ERROR_ABORT`, not observed HTTP 429 responses.
- Worker logs retain eight 60-second model request timeouts. All vision
  activities ultimately returned an evaluation; no candidate ended `vision_error`.
  Local admission waits reached 81.9 seconds before one timed-out request.
- Four API transport warnings around 20:48–20:49 UTC include two Docker DNS
  lookup failures and two request deadlines. This overlaps some aborted browser
  searches, but does not establish a shared cause or Twitter downtime.

## Remaining acceptance

FF-093 now has natural acceptance, never-public, exact-follower and successful
vision-retry evidence. Placement receipt replay remains unobserved naturally.
FF-081 still needs natural restoration, multi-keeper shared support and its exact
recurrence, frontend receipt/rendering, and larger-event placement/object-check/
Temporal-history measurements. Ordinary direct scoring is verified above;
whole-video replacement and picture-quality policy remain separate open work.

No synthetic production search was started to manufacture that evidence. The
[issue register](../todo.md) retains the broader quality-policy work and the
explicit boundary against automatic historical repair.

## Historical repair shortlist — 21:25 UTC

A read-only follow-up rechecked the known restoration leads, not every historical
event. No selection commit or score repair was applied to old events.

- **Maitland-Niles 90+6′, Everton–United, September 6:** event
  `01dedc54-da16-4e3c-9539-ac43c81ffd77` still has its unsupported original
  `s_19a9f2fb7ea2` superseded. Its original and the two potential shared-support
  variants returned fresh Garage HEAD 200s at expected sizes: original asset
  `32b50be4-0ad5-50a5-8ebe-f590cc4b57a4` (1,556,401 bytes), bridge
  `3227d1d5-c2d8-56fa-8dca-7238929b6040` (3,570,403 bytes; four sightings), and
  bridge `d0ef0afe-7c92-50e1-875d-15a35c78df7d` (1,861,844 bytes; one sighting).
  Retained hashes reproduce the two bridges' direct matches to both clips.
  The restored original has one proven source; the conditional score of six
  depends on the two bridges' missing own acceptance categories. Thus restoring
  the original alone would not make it public under FF-078. Review/revalidation
  must precede any claim that six is an established historical score.
- **Mastantuono 30′, Venezia–Fiorentina, September 11:** the prior repair remains
  intact at 22/14, with correct share `s_1b640c847760` active. No second restoration
  is needed. The previously identified first-goal clip remains rank 1; excluding
  wrong-event footage would be a separate content repair, not an automatic
  consequence of the new graph. Conditional direct scoring gives 22/19, but
  ten observations lack their variant's own validation, so this is not a safe
  score update to apply blindly.
- **Older simulation leads:** all retained assets for Messi 83′ (August 29),
  Fermín 71′, Lee Kang-In 70′, Welbeck 50′ and Marquinhos 90+5′ are now marked
  reclaimed. Olise's August 28 originals and Donnarumma's original are also
  marked reclaimed; Schade's original remains SQL-unreclaimed but its last
  direct HEAD returned 404. No fresh missing-object recovery was attempted.
  The selection implementation cannot recreate those media bytes.

Blind historical rescoring would mix a policy change with evidence loss:
Maitland-Niles's main keeper has stored support 32, known-only direct support 23,
and conditional direct support 31; Mastantuono's current 22/14 becomes 17/9
under known-only counting. These are not arithmetic corruption findings.
The new runtime cannot infer missing old validation from a keeper's category.
The next useful historical step is a bounded review of the three available
Maitland-Niles files, followed by an explicit repair preview and approval.

### Maitland-Niles visual review

The user approved reviewing those three originals. Read-only Garage GETs
downloaded them to ignored local review storage. Each file matched its stored
byte count and MD5. An isolated, network-disabled decoder generated one-second
contact sheets across each clip, plus denser opening-action frames. This was
manual frame inspection, not a new production vision evaluation or an
effective-frame-rate measurement.

| Asset | Decoded duration | Reported video properties | Observed content |
|---|---|---|---|
| Original `32b50be4…` | 7.06 s | 1206×680, about 60 fps | Starts immediately before the strike at 95:09, includes the goal and celebration; `+FOOT DIRECT` overlay |
| Longer bridge `3227d1d5…` | 14.21 s | 1280×720, 29.97 fps | Buildup, the same strike and a longer celebration; ESPN overlay |
| Short bridge `d0ef0afe…` | 9.02 s | 1280×720, 60 fps | Earlier buildup through the strike and first celebration; prominent `TRENDEPL` overlay |

All three contain the same broadcast goal action, not three distinct angles or
an earlier goal. The visible 95:xx clock is consistent with the stored 90+6′
event. The original does include the strike: its short duration is not evidence
that it contains only celebrations. No phone-screen framing is apparent in
these samples. Reported resolution and frame rate alone do not establish a
picture-quality winner.

Readable clocks raise a separate historical-evidence question: the original's
share is recorded unverified, while the never-public bridges have no retained
own validation. The old model responses are unavailable, so this review cannot
explain that classification or reconstruct their missing results. Human-visible
clock evidence is not a substitute for an actual new evaluation record.

The review supports treating these as correct-event recovery candidates. It
does not establish that restoring another same-angle clip improves the public
selection. The current main keeper was not visually reviewed in this pass.
Before any repair, preview the affected event with explicitly recorded fresh
validation; inspect the current keeper as part of that comparison. Recompute
selection, direct support and visibility together. A new verified classification
would change eligible support, so the earlier conditional score of six must not
be written as an unconditional result. No production validation, selection,
score, share, object or notification was changed.

### Fresh-validation preview

The approved [follow-up review](../design/audits/maitland-niles-selection-preview-2026-09-13.md)
fetched and digest-checked all ten originals, inspected the current keeper,
and ran ten sequential calls through the unchanged production validation code.
Filling the four never-public variants' missing own evidence with these fresh
accepted results, while preserving existing share acceptance, yields public
scores 31/6 and restores only `s_19a9f2fb7ea2`. Both clips show the same goal;
their direct hashes still miss the required windows. This is not proof of a
new angle or a quality improvement.

One previously accepted variant received a fresh false rejection after the model
converted a `+5` allowance into a `05:00` secondary clock. An all-fresh overwrite
was therefore not planned. The bounded restoration remains a preview requiring
separate production approval; no fresh result, score or selection was persisted.

### Approved historical restoration

The subsequent [one-event repair](./maitland-niles-restoration-2026-09-13.md)
committed at 22:32 UTC with separate approval. It retained the four fresh
never-public evaluations, preserved existing share validation, and restored the
original at public rank 2 with support six behind the current keeper's 31.
Independent REST and public SSE checks passed. No deployment or global rescore
occurred; the false fresh clock rejection remains separate FF-094 work.

## Follow-up — September 15

Read-only production SQL, scoped container inspection and retained worker and
Temporal histories were checked from 16:54 UTC. No production mutation,
replay, model request or deployment was performed.

### Selection and settlement

- Both workers, API and Twitter still run the `8c836e0` images started on
  September 13 at 19:19:26 UTC. All four have zero restarts and no recorded OOM.
- Since that restart, 46 newly observed events have completed their discovery
  checklist: 39 `assets_surfaced`, three `candidates_no_assets`, three
  `event_removed`, and one `no_candidates`. The 43 non-cancelled discoveries
  each completed fifteen usable searches. Eighteen unavailable observations
  did not consume that budget. One event removed after discovery completion
  still has the historical `assets_surfaced` outcome; it has no active share.
- All 3,138 candidate rows are terminal: 658 accepted (68 promoted, 546
  duplicate, 44 superseded), 2,003 rejected and 477 failed. All accepted rows
  have observed/routing identities and their own exact asset's validation.
  The separate 38 old pending-after-parent rows remain from August; none is a
  post-release recurrence.
- Natural accepted evidence now contains 157 evaluations across 41 events.
  Four manual Maitland-Niles evaluations bring the complete table to 161;
  do not include that repair in natural acceptance counts. Every retained
  evaluator identifier remains `vision-evaluate-v1`.
- The new events have 496 `direct-restoration-support-v2` receipts. All 68
  selected-root scores in their latest receipts match the asset rows, and
  those assets remain roots. This checks persisted result consistency, not an
  independent recomputation of every dHash support edge or picture quality.
  No natural receipt contains a nonempty `Plan.Restored`; automatic restoration
  still lacks natural acceptance evidence.
- No discovery workflow or scoped event browser was running at the inspection.
  Genoa–Sudtirol was being live-polled. The known Cincinnati–DC fixture
  `1490439`, now scheduled for October 21, remains active and polled (FF-088).

### Vision timeout burst — September 14

The new failure diagnostics exposed a material instance of the existing
FF-037 admission/deadline problem. Ninety-two candidate outcomes failed vision
across six events in Leeds–Newcastle and Villarreal–Betis. This is not 92
distinct lost videos: six retained histories contain 32 exhausted validation
activities covering 30 event/MD5 pairs and 22 distinct MD5s across the fixtures.
Twenty-eight activities exhausted with StartToClose timeout; four exhausted
with a model-request failure. Exact followers inherit those outcomes, producing
72 `activity/timeout/start_to_close` and 20 `model_request/timeout` candidate rows.

| Event | Failed candidate rows | Exhausted validation activities |
|---|---:|---:|
| Calvert-Lewin 34′, subsequently removed | 37 | 13 |
| Miley 32′ | 31 | 13 |
| Gerard Moreno 29′ | 10 | 2 |
| Cucho Hernández 39′ | 12 | 2 |
| Natan 43′ | 1 | 1 |
| Calvert-Lewin 45+1′ | 1 | 1 |

Final activity failures span 19:39:18–19:57:21 UTC. Across both workers,
19:30–20:20 UTC contains 161 request-phase failure records, with local semaphore
waits as high as 179.281 seconds and HTTP attempts up to about 60 seconds.
The activity's StartToClose budget is 180 seconds. These measurements show
that local waiting can consume almost the whole activity budget; they do not
establish how much time the gateway spent queuing versus performing inference,
or prove a new selection regression caused the load.

The user subsequently confirmed on September 15 that other workloads on the
LLM node occupied its slots during this burst. Treat that as operator-reported
shared-capacity contention, not evidence of a regression in this rollout.
The user accepts the constraint for now and expects future hardware to improve
capacity; no admission/deadline implementation is requested in response to this
incident. Keep FF-037 deferred and retain the diagnostics. The exhausted
validation outcomes remain real; additional capacity is not a guarantee against
future contention.

Two failed event/MD5 pairs later obtained accepted evidence. The remaining
absence of accepted evidence does not prove unique goal footage was lost:
some could be wrong-event clips, duplicates or later rejects. All five surviving
events have active shares, while the removed event has none. All six checked
histories have no terminal placement or event-publication failure. This is not
a check of receipt/rendering by React.

The other 385 failed candidates are `cdn_download/forbidden` (FF-089). The
clock-normalization proposal (FF-094) is still unimplemented: the old prompt,
secondary addition and allowance/unpinned branches remain in production.
Diagnostics and direct-support selection are deployed; clock repair and
admission/deadline repair are separate pending work.
