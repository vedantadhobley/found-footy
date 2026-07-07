// ingest.go — the daily IngestWorkflow. Runs at 00:05 UTC via a
// Temporal Schedule (registered separately at worker startup) and
// orchestrates the four (five with by-IDs) activities in
// internal/activity/ingest.
//
// Determinism rules for workflows (do not violate):
//   • Never call time.Now() — use workflow.Now(ctx)
//   • Never call log.Print / fmt.Println — use workflow.GetLogger(ctx)
//   • Never spawn goroutines directly — use workflow.Go
//   • Never read env / files / random — do all I/O in activities
//
// Every side effect lives in an activity; this file is pure
// orchestration + branching.
package workflow

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/vedantadhobley/found-footy/internal/activity/ingest"
)

// Defaults injected by the workflow when the caller passes zero.
// The schedule spec typically leaves everything zero and lets these
// take effect; manual triggers override individual fields.
const (
	defaultActivationWindow = 30 * time.Minute
	defaultRetentionDays    = 14
	fetchWindowPastDays     = 1  // fetch fixtures back this many days from anchor
	fetchWindowFutureDays   = 3  // ...and forward this many days
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

	// ManualFixtureIDs, when non-empty, switches the fetch path from
	// FetchFixturesForWindow to FetchFixturesByIDs. Bypasses the
	// 3-day window entirely. Cap: 20 IDs per call (api-sports.io
	// limit — the workflow does NOT chunk).
	ManualFixtureIDs []int64

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
	Fetched         int
	Staging         int
	Active          int
	Completed       int
	ExistingAliases int
	InsertedAliases int
	PrunedFixtures  int
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
	activationWindow := in.ActivationWindow
	if activationWindow == 0 {
		activationWindow = defaultActivationWindow
	}

	logger.Info("IngestWorkflow started",
		"anchor", anchor.Format(time.RFC3339),
		"manual_date_override", in.ManualDate != nil,
		"manual_ids_count", len(in.ManualFixtureIDs),
		"retention_days", in.RetentionDays,
	)

	// Default activity options. Individual steps can override via
	// workflow.WithActivityOptions when their profile differs.
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

	// ── Step 1: fetch fixtures ──
	// Two paths — by-window (normal) or by-IDs (manual re-ingest).
	var fetchOut ingest.FetchFixturesOutput
	if len(in.ManualFixtureIDs) > 0 {
		if err := workflow.ExecuteActivity(ctx,
			"FetchFixturesByIDs",
			ingest.FetchFixturesByIDsInput{IDs: in.ManualFixtureIDs},
		).Get(ctx, &fetchOut); err != nil {
			return out, err
		}
	} else {
		from := anchor.AddDate(0, 0, -fetchWindowPastDays)
		to := anchor.AddDate(0, 0, fetchWindowFutureDays)
		if err := workflow.ExecuteActivity(ctx,
			"FetchFixturesForWindow",
			ingest.FetchFixturesInput{From: from, To: to},
		).Get(ctx, &fetchOut); err != nil {
			return out, err
		}
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
	}

	// ── Step 4: prune old completed fixtures ──
	// RetentionDays == 0 → skip. Threshold computed from the anchor
	// so manual-date-override runs prune relative to the anchor,
	// not workflow.Now — matters for re-ingest scenarios where you
	// want the retention math consistent with the fetch math.
	if in.RetentionDays > 0 {
		threshold := anchor.AddDate(0, 0, -in.RetentionDays)
		var pruneOut ingest.PruneOldFixturesOutput
		if err := workflow.ExecuteActivity(ctx,
			"PruneOldFixtures",
			ingest.PruneOldFixturesInput{Threshold: threshold},
		).Get(ctx, &pruneOut); err != nil {
			return out, err
		}
		out.PrunedFixtures = pruneOut.Deleted
		logger.Info("pruned", "count", out.PrunedFixtures, "threshold_days", in.RetentionDays)
	}

	logger.Info("IngestWorkflow complete", "output", out)
	return out, nil
}
