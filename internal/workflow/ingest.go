// ingest.go — the daily IngestWorkflow. Runs at 00:05 UTC via a
// Temporal Schedule (registered separately at worker startup) and
// orchestrates the four activities in internal/activity/ingest.
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

// IngestWorkflowInput narrows what the caller (Scheduler or a manual
// trigger) passes. Zero values on the optional fields mean "skip
// that step" — e.g. an ad-hoc re-ingest can leave RetentionThreshold
// zero to avoid pruning.
type IngestWorkflowInput struct {
	// FetchWindowFrom + FetchWindowTo bracket the kickoff window
	// api-sports.io returns fixtures for. Both must be set.
	// Typical daily schedule: [today-1d, today+3d].
	FetchWindowFrom time.Time
	FetchWindowTo   time.Time

	// ActivationWindow: staging fixtures with kickoff within this
	// duration of now get auto-activated during categorization.
	// Typical: 30 * time.Minute.
	ActivationWindow time.Duration

	// RetentionThreshold: completed fixtures older than this get
	// pruned. Zero value = skip pruning. Typical: now - 14 days.
	RetentionThreshold time.Time
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
// startup. Called once daily by a Temporal Schedule.
//
// Execution order is sequential (each step depends on the prior's
// output). No parallel branches — daily ingest is not throughput-
// bounded, and sequencing keeps failure attribution simple.
func IngestWorkflow(ctx workflow.Context, in IngestWorkflowInput) (IngestWorkflowOutput, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("IngestWorkflow started",
		"from", in.FetchWindowFrom.Format(time.RFC3339),
		"to", in.FetchWindowTo.Format(time.RFC3339),
	)

	out := IngestWorkflowOutput{}

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

	// ── Step 1: fetch fixtures for window ──
	var fetchOut ingest.FetchFixturesOutput
	if err := workflow.ExecuteActivity(ctx,
		"FetchFixturesForWindow",
		ingest.FetchFixturesInput{From: in.FetchWindowFrom, To: in.FetchWindowTo},
	).Get(ctx, &fetchOut); err != nil {
		return out, err
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
			ActivationWindow: in.ActivationWindow,
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
	// Zero value threshold = skip. Used by ad-hoc re-ingests that
	// shouldn't touch retention.
	if !in.RetentionThreshold.IsZero() {
		var pruneOut ingest.PruneOldFixturesOutput
		if err := workflow.ExecuteActivity(ctx,
			"PruneOldFixtures",
			ingest.PruneOldFixturesInput{Threshold: in.RetentionThreshold},
		).Get(ctx, &pruneOut); err != nil {
			return out, err
		}
		out.PrunedFixtures = pruneOut.Deleted
		logger.Info("pruned", "count", out.PrunedFixtures)
	}

	logger.Info("IngestWorkflow complete", "output", out)
	return out, nil
}
