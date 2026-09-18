// Command found-footy-ops runs explicit, guarded one-shot production operations.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/firefoxfleet"
	pginfra "github.com/vedantadhobley/found-footy/internal/infra/pg"
)

var (
	gitSHA  = "dev"
	builtAt = "unknown"
)

const operationTimeout = 4 * time.Hour

// main applies the command's process exit boundary.
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "found-footy-ops:", err)
		os.Exit(1)
	}
}

// run routes one operator subcommand without opening external dependencies first.
func run(args []string) error {
	if len(args) == 0 {
		return usageError()
	}
	switch args[0] {
	case "replay-event":
		return replayEvent(args[1:])
	case "version":
		fmt.Printf("git_sha=%s built_at=%s\n", gitSHA, builtAt)
		return nil
	default:
		return usageError()
	}
}

// usageError returns the complete bounded command surface.
func usageError() error {
	return fmt.Errorf("use replay-event --event-id <uuid> --operation <slug> --mode <fresh-search|retry-transient|recover> [--apply], or version")
}

// replayEvent plans or applies one idempotent completed-event recovery.
func replayEvent(args []string) error {
	flags := flag.NewFlagSet("replay-event", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	rawEventID := flags.String("event-id", "", "existing event UUID")
	operation := flags.String("operation", "", "stable lowercase operation slug")
	rawMode := flags.String("mode", string(pginfra.EventReplayRecover), "fresh-search, retry-transient, or recover")
	apply := flags.Bool("apply", false, "commit preparation and start Temporal")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	eventID, err := uuid.Parse(*rawEventID)
	if err != nil {
		return fmt.Errorf("parse --event-id: %w", err)
	}
	mode := pginfra.EventReplayMode(*rawMode)
	workflowID, err := pginfra.StableEventReplayWorkflowID(eventID, *operation)
	if err != nil {
		return err
	}

	cfg, err := config.LoadFor(config.BinaryOps)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	postgresConfig, err := pgx.ParseConfig(cfg.Postgres.DSN)
	if err != nil {
		return fmt.Errorf("parse Postgres config: %w", err)
	}
	postgresConfig.ConnectTimeout = cfg.Postgres.ConnectTimeout
	conn, err := pgx.ConnectConfig(ctx, postgresConfig)
	if err != nil {
		return fmt.Errorf("connect Postgres: %w", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	request := pginfra.EventReplayRequest{
		EventID: eventID, WorkflowID: workflowID, Operation: *operation,
		Mode: mode,
	}
	store := pginfra.NewEventReplayStore(conn)
	plan, err := store.PlanEventReplay(ctx, request)
	if err != nil {
		return err
	}
	if err := printJSON("EVENT_REPLAY_PLAN", plan); err != nil {
		return err
	}
	if !*apply {
		fmt.Println("EVENT_REPLAY_DRY_RUN: pass --apply to register and start this exact operation")
		return nil
	}

	prepared, err := store.PrepareEventReplay(ctx, request)
	if err != nil {
		return err
	}
	if err := printJSON("EVENT_REPLAY_PREPARED", prepared); err != nil {
		return err
	}
	if prepared.Completed {
		result, err := store.ReadEventReplayResult(ctx, eventID, workflowID)
		if err != nil {
			return err
		}
		if err := printJSON("EVENT_REPLAY_ALREADY_COMPLETE", result); err != nil {
			return err
		}
		return verifyReplayResult(result)
	}

	if cfg.FirefoxFleet.Enabled {
		fleet, err := firefoxfleet.New(cfg.FirefoxFleet)
		if err != nil {
			return err
		}
		defer func() { _ = fleet.Close() }()
		addr, err := fleet.Provision(ctx, eventID)
		if err != nil {
			return fmt.Errorf("provision Firefox: %w", err)
		}
		if err := waitBrowser(ctx, addr); err != nil {
			return err
		}
		fmt.Printf("EVENT_REPLAY_BROWSER_READY event=%s address=%s\n", eventID, addr)
	}

	temporalClient, err := client.Dial(client.Options{
		HostPort: cfg.Temporal.HostPort, Namespace: cfg.Temporal.Namespace,
	})
	if err != nil {
		return fmt.Errorf("connect Temporal: %w", err)
	}
	defer temporalClient.Close()
	run, err := temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                    workflowID,
		TaskQueue:             cfg.Temporal.TaskQueue,
		WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
	}, "EventWorkflow", prepared.Input)
	if err != nil {
		var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
		if !errors.As(err, &alreadyStarted) {
			return fmt.Errorf("start EventWorkflow: %w", err)
		}
		run = temporalClient.GetWorkflow(ctx, workflowID, "")
	}
	fmt.Printf("EVENT_REPLAY_STARTED workflow=%s run=%s\n", workflowID, run.GetRunID())
	if err := run.Get(ctx, nil); err != nil {
		return fmt.Errorf("workflow %s: %w", workflowID, err)
	}
	result, err := store.ReadEventReplayResult(ctx, eventID, workflowID)
	if err != nil {
		return err
	}
	if err := printJSON("EVENT_REPLAY_COMPLETE", result); err != nil {
		return err
	}
	return verifyReplayResult(result)
}

// verifyReplayResult requires Temporal completion to agree with durable state.
func verifyReplayResult(result pginfra.EventReplayResult) error {
	if !result.ChecklistCompleted || result.PendingCandidates != 0 {
		return fmt.Errorf(
			"durable replay is incomplete: checklist=%t pending_candidates=%d",
			result.ChecklistCompleted, result.PendingCandidates,
		)
	}
	return nil
}

// waitBrowser protects search budget from asynchronous Firefox startup.
func waitBrowser(parent context.Context, addr string) error {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	httpClient := &http.Client{Timeout: 5 * time.Second}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr+"/status", nil)
		if err != nil {
			return fmt.Errorf("build browser readiness request: %w", err)
		}
		resp, err := httpClient.Do(req)
		if err == nil {
			var status struct {
				State string `json:"state"`
				Busy  bool   `json:"busy"`
			}
			decodeErr := json.NewDecoder(resp.Body).Decode(&status)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && decodeErr == nil &&
				status.State == "healthy" && !status.Busy {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("browser readiness: %w", ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

// printJSON emits one greppable, machine-readable operation record.
func printJSON(label string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %s: %w", label, err)
	}
	fmt.Printf("%s %s\n", label, raw)
	return nil
}
