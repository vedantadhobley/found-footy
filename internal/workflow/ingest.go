// ingest.go — the daily IngestWorkflow. Runs at 00:05 UTC via a
// Temporal Schedule (registered separately at worker startup) and
// orchestrates the four (five with by-IDs) activities in
// internal/activity/ingest.
//
// Determinism rules for workflows (do not violate):
//   - Never call time.Now() — use workflow.Now(ctx)
//   - Never call log.Print / fmt.Println — use workflow.GetLogger(ctx)
//   - Never spawn goroutines directly — use workflow.Go
//   - Never read env / files / random — do all I/O in activities
//
// Every side effect lives in an activity; this file is pure
// orchestration + branching.
package workflow

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/vedantadhobley/found-footy/internal/activity/ingest"
	livefeedactivity "github.com/vedantadhobley/found-footy/internal/activity/livefeed"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	"github.com/vedantadhobley/found-footy/internal/domain/video"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// fmtIngestMissedIDsMsg — shared message shape for the errors slice,
// so operators can grep for the "gave up after N attempts" pattern.
func fmtIngestMissedIDsMsg(count, attempts int) string {
	return fmt.Sprintf("FetchFixturesByIDs: %d IDs missed after %d attempts", count, attempts)
}

// Manual-IDs targeted-retry policy. If FetchFixturesByIDs returns
// FailedIDs, we re-run the activity with JUST those IDs; loop up
// to ingestManualIDsMaxAttempts times with linear backoff. Ingest
// runs daily — recovery in-cycle beats waiting 24h. Kept as workflow-
// local constants (not shared cross-workflow) so they stay near their
// only caller.
const (
	ingestManualIDsMaxAttempts    = 3
	ingestManualIDsBackoffInitial = 5 * time.Second
)

// IngestWorkflowInput matches plan §5 W1 shape + the ActivationWindow
// addition (user-approved during Phase D per decisions.md 2026-07-07).
//
// All fields optional. The workflow computes its own window from
// workflow.Now(ctx) when nothing is overridden — which is what the
// daily schedule invocation passes.
type IngestWorkflowInput struct {
	// ManualDate overrides the anchor date the workflow uses to
	// compute the fetch window + retention cutoff. nil = use
	// workflow.Now(ctx) (the scheduled path). Set to a specific
	// day to re-ingest that day (e.g. after a data-source fix).
	ManualDate *time.Time

	// ManualFixtureIDs, when non-empty, switches the fetch path from the
	// by-date FetchFixturesForDay scan to FetchFixturesByIDs. Bypasses the
	// tracked-teams filter + the date lookahead. Any size — the adapter chunks internally
	// at apifootball.IDsBatchLimit; the workflow retries failed chunks
	// via the ingestManualIDsFetchRetry loop below (targeted at only
	// the IDs that didn't come back).
	ManualFixtureIDs []int64

	// FetchFuture controls whether the by-window fetch path also pulls
	// FetchWindowFutureDays days beyond the anchor. Default false
	// (manual triggers reingest a single day). The daily Temporal
	// Schedule sets this to TRUE alongside RetentionDays=14 so
	// scheduled runs get the full "today + N future days" window.
	// Ignored when ManualFixtureIDs is non-empty (that path is by-ID,
	// not date-based).
	FetchFuture bool

	// ActivationWindow: staging fixtures with kickoff within this
	// duration get auto-activated during categorization. Zero →
	// defaults to 30 * time.Minute.
	ActivationWindow time.Duration

	// RetentionDays: completed fixtures older than
	// (anchor - RetentionDays * 24h) get pruned. Zero → skip
	// pruning (used by ad-hoc re-ingests). The daily schedule spec
	// sends 14 explicitly.
	RetentionDays int
}

// IngestWorkflowOutput surfaces counts from each activity so the
// scheduler-side observer / Temporal UI can see landing metrics
// without joining logs. Errors aggregates per-fixture / per-team
// context strings from every activity that had non-fatal failures
// (a fixture that failed to reconcile, a team whose alias upsert
// hit a pg error) — the workflow itself completes successfully but
// operators see WHAT failed and WHY without joining logs.
type IngestWorkflowOutput struct {
	// TrackedTeamsRefreshed=true iff RefreshTrackedTeamsIfStale did a
	// full re-fetch on this run (false if the cache was still fresh).
	TrackedTeamsRefreshed bool
	// TrackedTeamsCount reflects the cache size AFTER a refresh; 0 on
	// cache-hit runs (Ingest doesn't re-read the cache size for
	// observability when it didn't touch it).
	TrackedTeamsCount int

	Fetched int
	// FilteredOut is the count of fixtures the API returned but the
	// tracked-teams filter dropped. Load-bearing signal: 0 usually
	// means the filter is empty (bug) or the vendor returned nothing.
	FilteredOut int

	Staging         int
	Active          int
	Completed       int
	ExistingAliases int
	InsertedAliases int

	PrunedFixtures  int
	ReclaimedEvents int
	Errors          []string
}

// IngestWorkflow — the workflow function. Registered at worker
// startup. Called once daily by a Temporal Schedule (empty input)
// OR by manual trigger (populated input).
//
// Execution order is sequential (each step depends on the prior's
// output). No parallel branches — daily ingest is not throughput-
// bounded, and sequencing keeps failure attribution simple.
func IngestWorkflow(ctx workflow.Context, in IngestWorkflowInput) (IngestWorkflowOutput, error) {
	logger := workflow.GetLogger(ctx)
	out := IngestWorkflowOutput{}

	// Resolve anchor + defaults. All Now() reads go through
	// workflow.Now — deterministic across replays.
	anchor := workflow.Now(ctx)
	if in.ManualDate != nil {
		anchor = *in.ManualDate
	}

	// Default activity options need to be set before the first
	// ExecuteActivity call, including the config accessor below.
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 60 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	// Read config once at start of workflow — activation window +
	// retention come from env-driven WorkflowsConfig, so we can't
	// hardcode them here. Manual triggers can override individual
	// values via the input.
	var cfgOut ingest.GetIngestConfigOutput
	if err := workflow.ExecuteActivity(ctx,
		"GetIngestConfig",
		ingest.GetIngestConfigInput{},
	).Get(ctx, &cfgOut); err != nil {
		return out, fmt.Errorf("read ingest config: %w", err)
	}
	activationWindow := in.ActivationWindow
	if activationWindow == 0 {
		activationWindow = cfgOut.ActivationWindow
	}
	// RetentionDays: caller override takes precedence; zero from
	// caller falls back to config; if config also 0, prune skipped.
	retentionDays := in.RetentionDays
	if retentionDays == 0 {
		retentionDays = cfgOut.RetentionDays
	}

	logger.Info("IngestWorkflow started",
		"anchor", anchor.Format(time.RFC3339),
		"manual_date_override", in.ManualDate != nil,
		"manual_ids_count", len(in.ManualFixtureIDs),
		"fetch_future", in.FetchFuture,
		"retention_days", retentionDays,
	)

	// ── Step 0: refresh tracked-teams cache if stale ──
	// Skipped on the manual-IDs path — that path targets specific
	// fixtures by ID, so no filter is applied downstream. Otherwise
	// we ensure the tracked-teams filter has fresh data before fetch.
	if len(in.ManualFixtureIDs) == 0 {
		// Longer timeout: the refresh loops over ~6 leagues × 2 API
		// calls each. Each call can take 1-5s under load, so 60s is
		// tight for the worst case — bump to 120s.
		refreshCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 120 * time.Second,
			RetryPolicy:         ao.RetryPolicy,
		})
		var refreshOut ingest.RefreshTrackedTeamsIfStaleOutput
		if err := workflow.ExecuteActivity(refreshCtx,
			"RefreshTrackedTeamsIfStale",
			ingest.RefreshTrackedTeamsIfStaleInput{},
		).Get(ctx, &refreshOut); err != nil {
			// Non-fatal — if refresh fails, fetch will use whatever's in
			// the cache (possibly stale, possibly empty which triggers
			// fail-open behavior in the fetch activity).
			logger.Warn("RefreshTrackedTeamsIfStale failed; continuing with existing cache",
				"error", err)
			out.Errors = append(out.Errors, "RefreshTrackedTeamsIfStale: "+err.Error())
		} else {
			out.TrackedTeamsRefreshed = refreshOut.Refreshed
			out.TrackedTeamsCount = refreshOut.TotalTeams
			out.Errors = append(out.Errors, refreshOut.Errors...)
			if refreshOut.Refreshed {
				logger.Info("tracked teams refreshed",
					"total", refreshOut.TotalTeams,
					"per_league", refreshOut.PerLeagueCounts,
				)
				// A partial refresh preserved prior rows for leagues that
				// failed/emptied (audit P1-1) — surface it loudly rather than
				// letting a silently-degraded refresh pass as a clean one.
				if len(refreshOut.PreservedLeagues) > 0 {
					logger.Warn("tracked teams: partial refresh — preserved prior rows for unreachable leagues",
						"preserved_per_league", refreshOut.PreservedLeagues,
						"errors", refreshOut.Errors,
					)
				}
			}
		}
	}

	// ── Step 1: fetch fixtures ──
	// Two paths — by-window (normal) or by-IDs (manual re-ingest).
	var fetchOut ingest.FetchFixturesOutput
	if len(in.ManualFixtureIDs) > 0 {
		// Targeted-retry loop: on partial failure, re-request just
		// the IDs that didn't come back. Cap at ingestManualIDsMaxAttempts;
		// backoff linearly. Aggregate successful fixtures across attempts.
		remaining := in.ManualFixtureIDs
		var accumulated []apifootball.APIFixture // typed via ingest package alias below
		for attempt := 1; attempt <= ingestManualIDsMaxAttempts && len(remaining) > 0; attempt++ {
			var byIDsOut ingest.FetchFixturesByIDsOutput
			if err := workflow.ExecuteActivity(ctx,
				"FetchFixturesByIDs",
				ingest.FetchFixturesByIDsInput{IDs: remaining},
			).Get(ctx, &byIDsOut); err != nil {
				// Catastrophic failure of the activity itself (Temporal
				// retry policy already exhausted). Bail — nothing more
				// we can salvage in-workflow.
				return out, err
			}
			accumulated = append(accumulated, byIDsOut.Fixtures...)
			remaining = byIDsOut.FailedIDs
			if len(remaining) == 0 {
				break
			}
			logger.Warn("partial by-ID fetch; retrying failed IDs",
				"attempt", attempt,
				"got", len(byIDsOut.Fixtures),
				"remaining", len(remaining),
			)
			if err := workflow.Sleep(ctx, time.Duration(attempt)*ingestManualIDsBackoffInitial); err != nil {
				return out, err
			}
		}
		if len(remaining) > 0 {
			// Exhausted attempts; log the persistent misses but proceed
			// with what we got — categorize still valuable for the
			// fetched slice.
			logger.Warn("by-ID fetch exhausted retries; proceeding without missing IDs",
				"missed", len(remaining))
			out.Errors = append(out.Errors,
				fmtIngestMissedIDsMsg(len(remaining), ingestManualIDsMaxAttempts))
		}
		fetchOut = ingest.FetchFixturesOutput{Fixtures: accumulated, Count: len(accumulated)}
	} else {
		// By-date scan with smart lookahead. Mirrors Python's
		// ingest_workflow flow (archive/src/workflows/ingest_workflow.py):
		//
		//   1. Always fetch anchor day
		//   2. If FetchFuture: also fetch anchor+1 (tomorrow — needed for
		//      timezone coverage of "today" across all locales)
		//   3. If tomorrow has fixtures: also fetch anchor+2 (far-side
		//      timezone cover)
		//   4. If tomorrow is empty: scan anchor+2 through anchor+MaxLookahead
		//      for the next non-empty day, then also fetch that day + 1
		//      (near-side + far-side timezone cover for the next fixture)
		//
		// Dedupe by fixture ID across days.
		var accumulated []apifootball.APIFixture
		var totalFilteredOut int
		seenIDs := map[int64]struct{}{}

		appendUnique := func(dayOut ingest.FetchFixturesForDayOutput) {
			totalFilteredOut += dayOut.FilteredOut
			for _, f := range dayOut.Fixtures {
				if _, dup := seenIDs[f.Fixture.ID]; dup {
					continue
				}
				seenIDs[f.Fixture.ID] = struct{}{}
				accumulated = append(accumulated, f)
			}
		}

		fetchDay := func(d time.Time, label string) (ingest.FetchFixturesForDayOutput, error) {
			var dOut ingest.FetchFixturesForDayOutput
			err := workflow.ExecuteActivity(ctx,
				"FetchFixturesForDay",
				ingest.FetchFixturesForDayInput{Date: d},
			).Get(ctx, &dOut)
			if err != nil {
				return dOut, fmt.Errorf("FetchFixturesForDay(%s, %s): %w",
					label, d.Format("2006-01-02"), err)
			}
			return dOut, nil
		}

		// 1. Anchor day (always).
		anchorOut, err := fetchDay(anchor, "anchor")
		if err != nil {
			return out, err
		}
		appendUnique(anchorOut)
		if anchorOut.TrackedCacheEmpty {
			// #174 (audit Tier-2 #6): the tracked-teams cache was empty, so
			// the fetch failed closed (ingested nothing) instead of flooding
			// the world. Loud ERROR so the refresh gap doesn't pass silently.
			logger.Error("tracked-teams cache EMPTY — fetch failed closed, ingesting nothing; check RefreshTrackedTeamsIfStale")
			out.Errors = append(out.Errors, "tracked-teams cache empty — fetch failed closed, ingested nothing")
		}

		if in.FetchFuture {
			// cfgOut was already loaded at workflow start; use its
			// MaxLookaheadDays here.
			maxLookahead := cfgOut.MaxLookaheadDays
			if maxLookahead < 2 {
				maxLookahead = 2 // must at least cover anchor+1 + anchor+2
			}

			// 2. Anchor+1 (always when FetchFuture).
			tomorrow := anchor.AddDate(0, 0, 1)
			tomorrowOut, err := fetchDay(tomorrow, "tomorrow")
			if err != nil {
				return out, err
			}
			appendUnique(tomorrowOut)

			// 3 or 4: branch on tomorrow's count.
			if tomorrowOut.Count > 0 {
				// Standard path — tomorrow has fixtures, cover far side.
				dayAfter := anchor.AddDate(0, 0, 2)
				dayAfterOut, err := fetchDay(dayAfter, "day-after")
				if err != nil {
					return out, err
				}
				appendUnique(dayAfterOut)
				logger.Info("standard 3-day fetch complete",
					"anchor_count", anchorOut.Count,
					"tomorrow_count", tomorrowOut.Count,
					"day_after_count", dayAfterOut.Count,
				)
			} else {
				// Lookahead path — tomorrow is empty, scan forward.
				logger.Info("tomorrow empty; entering lookahead scan",
					"anchor_count", anchorOut.Count,
					"max_lookahead", maxLookahead,
				)
				foundDay := time.Time{}
				var foundCount int
				for d := 2; d <= maxLookahead; d++ {
					candidate := anchor.AddDate(0, 0, d)
					candOut, err := fetchDay(candidate, fmt.Sprintf("lookahead+%d", d))
					if err != nil {
						return out, err
					}
					appendUnique(candOut)
					if candOut.Count > 0 {
						foundDay = candidate
						foundCount = candOut.Count
						break
					}
				}
				if !foundDay.IsZero() {
					// Cover the far side of the found day too.
					afterFound := foundDay.AddDate(0, 0, 1)
					afterOut, err := fetchDay(afterFound, "after-found")
					if err != nil {
						return out, err
					}
					appendUnique(afterOut)
					logger.Info("lookahead fetch complete",
						"found_day", foundDay.Format("2006-01-02"),
						"found_count", foundCount,
						"after_count", afterOut.Count,
					)
				} else {
					logger.Warn("no upcoming fixtures found within lookahead",
						"max_lookahead", maxLookahead,
					)
				}
			}
		}

		fetchOut = ingest.FetchFixturesOutput{Fixtures: accumulated, Count: len(accumulated)}
		out.FilteredOut = totalFilteredOut
	}
	out.Fetched = fetchOut.Count
	logger.Info("fetched fixtures", "count", out.Fetched)

	// ── Step 2: categorize + upsert ──
	// Longer timeout: DB-bound over potentially 100s of fixtures.
	catCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 120 * time.Second,
		RetryPolicy:         ao.RetryPolicy,
	})
	var catOut ingest.CategorizeOutput
	if err := workflow.ExecuteActivity(catCtx,
		"CategorizeAndUpsertFixtures",
		ingest.CategorizeInput{
			Fixtures:         fetchOut.Fixtures,
			ActivationWindow: activationWindow,
		},
	).Get(ctx, &catOut); err != nil {
		return out, err
	}
	out.Staging = catOut.Staging
	out.Active = catOut.Active
	out.Completed = catOut.Completed
	out.Errors = append(out.Errors, catOut.Errors...)
	logger.Info("categorized",
		"staging", out.Staging,
		"active", out.Active,
		"completed", out.Completed,
		"errors", len(catOut.Errors),
	)

	// ── Step 2.5: fixture.update for new/changed fixtures (N6) ──
	// Best-effort — a lost batch heals on the consumer's next window refetch.
	if len(catOut.ChangedIDs) > 0 {
		emitCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 15 * time.Second,
			RetryPolicy:         ao.RetryPolicy,
		})
		if err := workflow.ExecuteActivity(emitCtx, "PublishFixtureBatch",
			livefeedactivity.FixtureBatchInput{UpdateIDs: catOut.ChangedIDs}).Get(emitCtx, nil); err != nil {
			logger.Warn("PublishFixtureBatch failed (heals on refetch)", "error", err)
			out.Errors = append(out.Errors, "PublishFixtureBatch: "+err.Error())
		}
	}

	// ── Step 3: alias placeholders ──
	// Only if the categorize step surfaced team refs.
	if len(catOut.TeamRefs) > 0 {
		var aliasOut ingest.EnsureAliasPlaceholdersOutput
		if err := workflow.ExecuteActivity(ctx,
			"EnsureAliasPlaceholders",
			ingest.EnsureAliasPlaceholdersInput{Teams: catOut.TeamRefs},
		).Get(ctx, &aliasOut); err != nil {
			return out, err
		}
		out.ExistingAliases = aliasOut.Existing
		out.InsertedAliases = aliasOut.Inserted
		out.Errors = append(out.Errors, aliasOut.Errors...)
		logger.Info("alias placeholders",
			"existing", out.ExistingAliases,
			"inserted", out.InsertedAliases,
			"errors", len(aliasOut.Errors),
		)
		// Alias RESOLUTION (Wikipedia→Wikidata) was removed 2026-08-16 — the
		// query builder now derives the team acronym deterministically at goal
		// time from the canonical name (decisions.md). Only the canonical-name
		// placeholder above (EnsureAliasPlaceholders) survives.
	}

	// ── Step 4: retention (two-part, #176) ──
	// RetentionDays == 0 → skip. Threshold from the anchor (not
	// workflow.Now) so manual-date re-ingests keep the retention math
	// consistent with the fetch math.
	//
	// Part 1 (PruneOldFixtures activity): hard-delete clip-LESS aged
	// fixtures + return the clip-BEARING aged events needing reclaim.
	// Part 2 (DestroyEvent loop): revoke each such event's shares (→ the
	// #167 redirect 410s) + delete its Garage bytes, KEEPING the rows as
	// tombstones so no shared URL ever 404s (rebuild-plan §3 URL-stability,
	// revised — see decisions.md 2026-08-11). The GB video bytes are the
	// real leak; the KB rows are the price of URL-stability.
	if retentionDays > 0 {
		threshold := anchor.AddDate(0, 0, -retentionDays)
		var pruneOut ingest.PruneOldFixturesOutput
		if err := workflow.ExecuteActivity(ctx,
			"PruneOldFixtures",
			ingest.PruneOldFixturesInput{Threshold: threshold},
		).Get(ctx, &pruneOut); err != nil {
			return out, err
		}
		out.PrunedFixtures = pruneOut.Deleted
		logger.Info("pruned clipless", "count", out.PrunedFixtures, "threshold_days", retentionDays)

		// Reclaim Garage bytes for clip-bearing aged events. Sequential:
		// a daily job over a bounded worklist, and DestroyEvent is
		// idempotent (revoke skips already-removed shares; S3 delete no-ops
		// on missing keys). DestroyEvent returns aggregate delete failures so
		// its activity policy retries every known key. An exhausted reclaim is
		// logged + recorded but does not abort unrelated ingest work; FF-024's
		// bounded object reconciliation remains the final abnormal-failure net.
		if len(pruneOut.ReclaimEventIDs) > 0 {
			reclaimCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
				StartToCloseTimeout: 2 * time.Minute,
				RetryPolicy:         ao.RetryPolicy,
			})
			for _, evID := range pruneOut.ReclaimEventIDs {
				if err := workflow.ExecuteActivity(reclaimCtx, "DestroyEvent",
					videoactivity.DestroyEventInput{
						EventID: evID,
						Reason:  string(video.RemovalPolicy),
					}).Get(reclaimCtx, nil); err != nil {
					logger.Warn("retention reclaim failed", "event_id", evID.String(), "error", err)
					out.Errors = append(out.Errors, "DestroyEvent(retention): "+err.Error())
					continue
				}
				out.ReclaimedEvents++
			}
			logger.Info("reclaimed clip bytes",
				"events", out.ReclaimedEvents, "candidates", len(pruneOut.ReclaimEventIDs))
		}
	}

	logger.Info("IngestWorkflow complete", "output", out)
	return out, nil
}
