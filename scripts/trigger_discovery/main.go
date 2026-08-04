// scripts/trigger_discovery/main.go — dev-only trigger for EventWorkflow.
//
// End-to-end smoke test of the O3/d pipeline. Insert a placeholder row
// into event_downstream_workflows (Monitor would normally do this in the
// same activity as the flag flip), then spawn EventWorkflow with
// the deterministic ID for the given event.
//
// Env:
//   EVENT_ID   — required. UUID of the event to run Discovery against.
//   ATTEMPTS   — optional. Override discoveryMaxAttempts for shorter smoke tests.
//                Reads via a workflow-input override — kept simple for now.
//
// Run:
//   docker exec -e EVENT_ID=<uuid> found-footy-dev-worker \
//     sh -c 'cd /src && go run ./scripts/trigger_discovery'
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.temporal.io/sdk/client"

	ffwf "github.com/vedantadhobley/found-footy/internal/workflow"
)

func main() {
	rawEventID := os.Getenv("EVENT_ID")
	if rawEventID == "" {
		fatal("missing", fmt.Errorf("EVENT_ID env var required"))
	}
	eventID, err := uuid.Parse(rawEventID)
	if err != nil {
		fatal("parse EVENT_ID", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// Read event context from pg to populate EventWorkflowInput.
	pgURL := os.Getenv("PG_URL")
	if pgURL == "" {
		pgURL = "postgres://ffuser:ffpass@postgres:5432/found_footy"
	}
	conn, err := pgx.Connect(ctx, pgURL)
	if err != nil {
		fatal("pg connect", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var (
		fixtureID  int64
		playerName string
		teamID     int64
		teamName   string
		minute     int
	)
	err = conn.QueryRow(ctx, `
		SELECT fixture_id, player_name, team_id, team_name, minute
		FROM events WHERE id = $1
	`, eventID).Scan(&fixtureID, &playerName, &teamID, &teamName, &minute)
	if err != nil {
		fatal("query event", err)
	}
	fmt.Printf("event: %s | fixture=%d | player=%q | team=%s (%d) | minute=%d\n",
		eventID, fixtureID, playerName, teamName, teamID, minute)

	// Insert placeholder event_downstream_workflows row (Monitor would
	// normally do this in the same activity as the flag flip). ON
	// CONFLICT DO NOTHING so re-runs during dev iteration are idempotent.
	workflowID := "discovery-smoke-" + eventID.String()
	_, err = conn.Exec(ctx, `
		INSERT INTO event_downstream_workflows
		    (event_id, workflow_type, workflow_id, started_at, completed_at)
		VALUES ($1, 'discovery', $2, NOW(), NULL)
		ON CONFLICT (event_id, workflow_type, workflow_id) DO NOTHING
	`, eventID, workflowID)
	if err != nil {
		fatal("insert downstream row", err)
	}

	// Fire the workflow. Different ID from the production
	// "discovery-{event_id}" so we don't collide with any prior spawn.
	hostport := os.Getenv("TEMPORAL_HOSTPORT")
	if hostport == "" {
		hostport = "temporal:7233"
	}
	c, err := client.Dial(client.Options{HostPort: hostport, Namespace: "default"})
	if err != nil {
		fatal("temporal dial", err)
	}
	defer c.Close()

	taskQueue := os.Getenv("TEMPORAL_TASK_QUEUE")
	if taskQueue == "" {
		taskQueue = "found-footy"
	}
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                taskQueue,
		WorkflowExecutionTimeout: 15 * time.Minute,
	}, ffwf.EventWorkflow, ffwf.EventWorkflowInput{
		EventID:    eventID,
		FixtureID:  fixtureID,
		PlayerName: playerName,
		TeamName:   teamName,
		TeamID:     teamID,
		Minute:     minute,
	})
	if err != nil {
		fatal("execute workflow", err)
	}
	fmt.Printf("workflow started: id=%s run=%s\n", workflowID, run.GetRunID())

	// Wait for completion + print result.
	var out ffwf.EventWorkflowOutput
	if err := run.Get(ctx, &out); err != nil {
		fatal("workflow errored", err)
	}
	buf, _ := json.MarshalIndent(out, "", "  ")
	fmt.Printf("workflow completed:\n%s\n", string(buf))
}

func fatal(label string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", label, err)
	os.Exit(1)
}
