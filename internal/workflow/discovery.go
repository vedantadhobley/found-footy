// DiscoveryWorkflow — MVP stub. Spawned by Monitor's ReconcileFixture
// via DownstreamSpawner when an event's downstream_triggered flag is
// flipped (2026-07-16 decision: Temporal-direct spawn + register-on-
// flip, not NATS-triggered). The stub logs its input, marks its
// event_downstream_workflows row completed, and returns. No Twitter
// search yet — that's the T phase (Twitter port) + a later O3/d
// activity that wires real Discovery work into the stub.
//
// Deterministic workflow ID convention: "discovery-{event_id}" so the
// row inserted by Monitor pairs 1:1 with the Temporal WorkflowID
// under RejectDuplicate policy. Activity retries after partial-
// success crashes hit "WorkflowExecutionAlreadyStarted" which is
// swallowed as success (see the spawner impl).
package workflow

import (
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
)

// DiscoveryWorkflowInput re-exports the shared type so callers that
// only import internal/workflow don't need a second import for the
// spawn payload. See internal/activity/discovery/types.go for the
// canonical declaration.
type DiscoveryWorkflowInput = discoveryactivity.DiscoveryWorkflowInput

// DiscoveryWorkflowOutput reports the run outcome for observability.
// Since the stub does no real work yet, Completed is trivially true
// once the completion-marking activity returns.
type DiscoveryWorkflowOutput struct {
	EventID   uuid.UUID `json:"event_id"`
	Completed bool      `json:"completed"`
}

// DiscoveryWorkflow calls SearchTweets (via the Twitter service —
// currently Python's twitter/ container behind S7's HTTP client),
// then marks its event_downstream_workflows row complete with the
// count of tweets found. Video download / validation / share
// creation lands in V/a and beyond — this workflow just proves
// end-to-end that Monitor → Discovery → real Twitter search works.
//
// MVP query construction: "player teamname goal". No Wikidata team-
// alias RAG yet — that's a follow-up phase. For unknown-player
// events (Player.Known()==false at Monitor's spawn time), the
// workflow logs + skips the search.
func DiscoveryWorkflow(ctx workflow.Context, in DiscoveryWorkflowInput) (DiscoveryWorkflowOutput, error) {
	log := workflow.GetLogger(ctx)
	log.Info("DiscoveryWorkflow started",
		"event_id", in.EventID,
		"fixture_id", in.FixtureID,
		"player", in.PlayerName,
		"team", in.TeamName,
		"minute", in.Minute,
	)

	outcomeClass := "no_search_unknown_player"
	var tweetsFound int

	// Skip Twitter search if the player is unknown — no useful query
	// to build and Twitter results would be noise.
	if in.PlayerName != "" {
		searchCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 2 * time.Minute, // Python's default search timeout
			HeartbeatTimeout:    30 * time.Second,
			RetryPolicy: &temporal.RetryPolicy{
				InitialInterval:    2 * time.Second,
				BackoffCoefficient: 2,
				MaximumAttempts:    3, // Twitter service rate limits + auth expiry are the common failures
			},
		})
		query := in.PlayerName + " " + in.TeamName + " goal"
		var searchOut discoveryactivity.SearchTweetsOutput
		if err := workflow.ExecuteActivity(searchCtx,
			(*discoveryactivity.Activities).SearchTweets,
			discoveryactivity.SearchTweetsInput{
				EventID:       in.EventID,
				FixtureID:     in.FixtureID,
				Query:         query,
				MaxAgeMinutes: 5,
			}).Get(searchCtx, &searchOut); err != nil {
			log.Warn("SearchTweets failed", "err", err, "query", query)
			outcomeClass = "search_failed"
		} else {
			tweetsFound = searchOut.Count
			if tweetsFound == 0 {
				outcomeClass = "no_tweets_found"
			} else {
				outcomeClass = "tweets_found"
			}
			log.Info("SearchTweets succeeded",
				"query", query,
				"count", tweetsFound,
			)
		}
	}

	// Mark the event_downstream_workflows row completed on exit.
	// Row was inserted by Monitor pre-spawn; this UPDATEs it.
	// See decisions.md 2026-07-16 spawn rule + completion-contract.md.
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumAttempts:    5,
		},
	})
	var completeOut discoveryactivity.MarkDownstreamCompleteOutput
	if err := workflow.ExecuteActivity(actCtx,
		(*discoveryactivity.Activities).MarkDownstreamComplete,
		discoveryactivity.MarkDownstreamCompleteInput{
			EventID:      in.EventID,
			WorkflowType: "discovery",
			WorkflowID:   workflow.GetInfo(ctx).WorkflowExecution.ID,
			OutcomeClass: outcomeClass,
		}).Get(actCtx, &completeOut); err != nil {
		// Row-update failure is not fatal to the workflow logic —
		// the outbox catch-up worker (future) could reconcile.
		// Log via the workflow logger; return the error so Temporal
		// records the workflow as failed for observability.
		log.Warn("MarkDownstreamComplete failed", "err", err)
		return DiscoveryWorkflowOutput{EventID: in.EventID, Completed: false}, err
	}

	log.Info("DiscoveryWorkflow completed",
		"event_id", in.EventID,
		"outcome", outcomeClass,
		"tweets_found", tweetsFound,
	)
	return DiscoveryWorkflowOutput{EventID: in.EventID, Completed: true}, nil
}
