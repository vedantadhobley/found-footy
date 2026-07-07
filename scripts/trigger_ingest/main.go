// scripts/trigger_ingest/main.go — dev-only trigger for IngestWorkflow.
//
// Purpose: end-to-end verification of the O1 wire-up. Fires one real
// IngestWorkflow execution against the dev worker with a bounded
// window (typically today only) and waits for completion. Exercises
// the entire chain: workflow → apifootball → pg fixtures + team_aliases.
//
// Run:
//
//	docker exec found-footy-dev-worker sh -c 'cd /src && go run ./scripts/trigger_ingest'
//
// The worker needs the current image built — `docker compose -f
// docker-compose.dev.yml build worker` first if activity code changed.
//
// Consumes ~1 api-sports.io request (well within the 7500/day quota).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"go.temporal.io/sdk/client"

	ffwf "github.com/vedantadhobley/found-footy/internal/workflow"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	hostport := os.Getenv("TEMPORAL_HOSTPORT")
	if hostport == "" {
		hostport = "temporal:7233"
	}

	c, err := client.Dial(client.Options{HostPort: hostport, Namespace: "default"})
	if err != nil {
		fatal("temporal dial", err)
	}
	defer c.Close()

	now := time.Now().UTC()
	in := ffwf.IngestWorkflowInput{
		// Tight window: today only. Cheap; still exercises the real path.
		FetchWindowFrom:  now.Truncate(24 * time.Hour),
		FetchWindowTo:    now.Truncate(24 * time.Hour).Add(24 * time.Hour),
		ActivationWindow: 30 * time.Minute,
		// Retention nowhere close to real fixtures — safe no-op.
		RetentionThreshold: now.Add(-365 * 24 * time.Hour),
	}
	inJSON, _ := json.MarshalIndent(in, "", "  ")
	fmt.Printf("Triggering IngestWorkflow with input:\n%s\n\n", inJSON)

	we, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        fmt.Sprintf("ingest-manual-%d", time.Now().Unix()),
		TaskQueue: "found-footy",
	}, ffwf.IngestWorkflow, in)
	if err != nil {
		fatal("ExecuteWorkflow", err)
	}
	fmt.Printf("WorkflowID: %s\nRunID:      %s\n\nWaiting for completion...\n", we.GetID(), we.GetRunID())

	var out ffwf.IngestWorkflowOutput
	if err := we.Get(ctx, &out); err != nil {
		fatal("workflow execution", err)
	}

	outJSON, _ := json.MarshalIndent(out, "", "  ")
	fmt.Printf("\n✓ IngestWorkflow COMPLETED:\n%s\n", outJSON)
}

func fatal(msg string, err error) {
	fmt.Fprintf(os.Stderr, "\n✗ FAILED at %q: %v\n", msg, err)
	os.Exit(1)
}
