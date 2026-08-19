// Command replay_clock_rejects reprocesses one fixture's exact historical
// clock-mismatch rejects through the normal EventWorkflow candidate pipeline.
// It is dry-run by default and never performs a fresh Twitter search.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	pginfra "github.com/vedantadhobley/found-footy/internal/infra/pg"
	ffworkflow "github.com/vedantadhobley/found-footy/internal/workflow"
)

const (
	defaultMaxAttempts        = 15
	defaultMaxCandidates      = 50
	defaultWorkflowTimeout    = 2 * time.Hour
	defaultOperationTimeout   = 4 * time.Hour
	replayWorkflowIDPrefix    = "event-replay-ff057-boundary-"
	replayApplyEnvironment    = "REPLAY_APPLY"
	expectedEventsEnvironment = "EXPECTED_EVENT_COUNT"
)

// replayConfig is the complete guarded operation contract parsed from env.
type replayConfig struct {
	FixtureID             int64
	ExpectedEventCount    int
	MaxAttempts           int
	MaxCandidatesPerEvent int
	Apply                 bool
	PGDSN                 string
	TemporalHostPort      string
	TemporalNamespace     string
	TemporalTaskQueue     string
}

// main runs the guarded replay command.
func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "replay_clock_rejects: %v\n", err)
		os.Exit(1)
	}
}

// run plans the fixture replay and applies it only when explicitly enabled.
func run() error {
	cfg, err := loadReplayConfig()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultOperationTimeout)
	defer cancel()

	conn, err := pgx.Connect(ctx, cfg.PGDSN)
	if err != nil {
		return fmt.Errorf("connect Postgres: %w", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	store := pginfra.NewCandidateReplayStore(conn)
	events, err := store.ListCandidateReplayEvents(
		ctx, cfg.FixtureID, pginfra.ClockMismatchRejectReason, replayWorkflowIDPrefix,
	)
	if err != nil {
		return err
	}
	if len(events) != cfg.ExpectedEventCount {
		return fmt.Errorf("fixture %d has %d processed events; expected exactly %d",
			cfg.FixtureID, len(events), cfg.ExpectedEventCount)
	}

	total := 0
	for _, event := range events {
		extra := ""
		if event.Input.Extra != nil {
			extra = fmt.Sprintf("+%d", *event.Input.Extra)
		}
		fmt.Printf("event=%s minute=%d%s type=%s detail=%q player=%q candidates=%d prepared=%t completed=%t\n",
			event.Input.EventID, event.Input.Minute, extra, event.EventType,
			event.Detail, event.Input.PlayerName, event.EligibleCandidates,
			event.AlreadyPrepared, event.Completed)
		if event.EligibleCandidates == 0 {
			return fmt.Errorf("event %s has no exact clock-mismatch candidates", event.Input.EventID)
		}
		if event.EligibleCandidates > cfg.MaxCandidatesPerEvent {
			return fmt.Errorf("event %s selects %d candidates; safety ceiling is %d",
				event.Input.EventID, event.EligibleCandidates, cfg.MaxCandidatesPerEvent)
		}
		total += event.EligibleCandidates
	}
	fmt.Printf("fixture=%d events=%d exact_clock_rejects=%d mode=%s\n",
		cfg.FixtureID, len(events), total, replayMode(cfg.Apply))
	if !cfg.Apply {
		fmt.Printf("dry run only; set %s=true to register and execute the replay\n", replayApplyEnvironment)
		return nil
	}

	temporalClient, err := client.Dial(client.Options{
		HostPort:  cfg.TemporalHostPort,
		Namespace: cfg.TemporalNamespace,
	})
	if err != nil {
		return fmt.Errorf("connect Temporal: %w", err)
	}
	defer temporalClient.Close()

	for index, event := range events {
		workflowID := replayWorkflowIDPrefix + event.Input.EventID.String()
		prepared, err := store.PrepareCandidateReplay(ctx, pginfra.PrepareCandidateReplayInput{
			EventID:      event.Input.EventID,
			WorkflowID:   workflowID,
			ReplayKind:   pginfra.ClockBoundaryReplayKind,
			RejectReason: pginfra.ClockMismatchRejectReason,
			MaxAttempts:  cfg.MaxAttempts,
		})
		if err != nil {
			return err
		}
		fmt.Printf("[%d/%d] prepared event=%s candidates=%d existing=%t completed=%t workflow=%s\n",
			index+1, len(events), event.Input.EventID, prepared.SelectedCandidates,
			prepared.AlreadyPrepared, prepared.Completed, workflowID)
		if prepared.Completed {
			result, err := checkedReplayResult(
				ctx, store, event.Input.EventID, workflowID, prepared.SelectedCandidates,
			)
			if err != nil {
				return err
			}
			fmt.Printf("[%d/%d] already completed workflow=%s outcome=%s replayed=%d\n",
				index+1, len(events), workflowID, result.OutcomeClass, result.ReplayedCandidates)
			continue
		}

		run, err := temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
			ID:                       workflowID,
			TaskQueue:                cfg.TemporalTaskQueue,
			WorkflowExecutionTimeout: defaultWorkflowTimeout,
			WorkflowIDReusePolicy:    enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
		}, ffworkflow.EventWorkflow, event.Input)
		if err != nil {
			var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
			if !errors.As(err, &alreadyStarted) {
				return fmt.Errorf("start workflow %s: %w", workflowID, err)
			}
			run = temporalClient.GetWorkflow(ctx, workflowID, "")
		}
		fmt.Printf("[%d/%d] running workflow=%s run=%s\n", index+1, len(events), workflowID, run.GetRunID())

		var workflowOutput ffworkflow.EventWorkflowOutput
		if err := run.Get(ctx, &workflowOutput); err != nil {
			return fmt.Errorf("workflow %s failed: %w", workflowID, err)
		}
		result, err := checkedReplayResult(
			ctx, store, event.Input.EventID, workflowID, prepared.SelectedCandidates,
		)
		if err != nil {
			return err
		}
		fmt.Printf("[%d/%d] completed workflow=%s outcome=%s replayed=%d assets_kept=%d\n",
			index+1, len(events), workflowID, result.OutcomeClass,
			result.ReplayedCandidates, workflowOutput.AssetsKept)
	}

	return nil
}

// checkedReplayResult requires the durable checklist and every selected row to
// agree with the Temporal completion before the runner advances.
func checkedReplayResult(
	ctx context.Context,
	store *pginfra.CandidateReplayStore,
	eventID uuid.UUID,
	workflowID string,
	expectedCandidates int,
) (pginfra.CandidateReplayResult, error) {
	result, err := store.ReadCandidateReplayResult(ctx, eventID, workflowID)
	if err != nil {
		return result, err
	}
	if !result.ChecklistCompleted || result.PendingCandidates != 0 ||
		result.ReplayedCandidates != expectedCandidates {
		return result, fmt.Errorf(
			"workflow %s verification failed: checklist=%t replayed=%d pending=%d expected=%d",
			workflowID, result.ChecklistCompleted, result.ReplayedCandidates,
			result.PendingCandidates, expectedCandidates,
		)
	}
	return result, nil
}

// loadReplayConfig reads the small configuration surface required by this
// one-shot operation without constructing unrelated worker dependencies.
func loadReplayConfig() (replayConfig, error) {
	var cfg replayConfig
	var err error
	if cfg.FixtureID, err = requiredInt64("FIXTURE_ID"); err != nil {
		return cfg, err
	}
	if cfg.ExpectedEventCount, err = requiredPositiveInt(expectedEventsEnvironment); err != nil {
		return cfg, err
	}
	if cfg.MaxAttempts, err = positiveIntWithDefault("DISCOVERY_MAX_ATTEMPTS", defaultMaxAttempts); err != nil {
		return cfg, err
	}
	if cfg.MaxCandidatesPerEvent, err = positiveIntWithDefault("REPLAY_MAX_CANDIDATES_PER_EVENT", defaultMaxCandidates); err != nil {
		return cfg, err
	}
	cfg.Apply = strings.EqualFold(strings.TrimSpace(os.Getenv(replayApplyEnvironment)), "true")
	cfg.PGDSN = strings.TrimSpace(os.Getenv("PG_DSN"))
	if cfg.PGDSN == "" {
		return cfg, fmt.Errorf("PG_DSN is required")
	}
	cfg.TemporalHostPort = envWithDefault("TEMPORAL_HOSTPORT", "temporal:7233")
	cfg.TemporalNamespace = envWithDefault("TEMPORAL_NAMESPACE", "default")
	cfg.TemporalTaskQueue = envWithDefault("TEMPORAL_TASK_QUEUE", "found-footy")
	return cfg, nil
}

// requiredInt64 parses a required positive int64 environment value.
func requiredInt64(name string) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

// requiredPositiveInt parses a required positive int environment value.
func requiredPositiveInt(name string) (int, error) {
	return positiveInt(name, strings.TrimSpace(os.Getenv(name)))
}

// positiveIntWithDefault parses a positive int or returns the supplied default.
func positiveIntWithDefault(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	return positiveInt(name, raw)
}

// positiveInt validates one already-selected string as a positive int.
func positiveInt(name, raw string) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

// envWithDefault returns a trimmed environment value or its fallback.
func envWithDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

// replayMode renders the mutation mode in operator output.
func replayMode(apply bool) string {
	if apply {
		return "apply"
	}
	return "dry-run"
}
