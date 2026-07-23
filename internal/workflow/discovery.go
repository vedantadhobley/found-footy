// DiscoveryWorkflow — production Twitter search + candidate collection.
//
// Spawned by Monitor's ReconcileFixture via DownstreamSpawner when an
// event's downstream_triggered flag is flipped (2026-07-16 decision:
// Temporal-direct spawn + register-on-flip, not NATS-triggered).
// Runs a fixed N attempts × M spacing (defaults: 15 × 60 s = 15 min
// lifetime per goal, tunable via config.DiscoveryConfig / DISCOVERY_*
// env vars — see #162). Each attempt is a full /search call; per-event
// exclude_urls accumulate across attempts so the Twitter service's
// consecutive-already-seen scroll stop terminates attempts 2+ quickly.
// Every candidate the search surfaces gets persisted to
// event_search_candidates for downstream V-phase pickup + post-hoc
// query-quality learning.
//
// Deterministic workflow ID convention: "discovery-{event_id}" so
// the row inserted by Monitor pairs 1:1 with the Temporal WorkflowID
// under RejectDuplicate policy. Activity retries after partial-
// success crashes hit "WorkflowExecutionAlreadyStarted" which the
// spawner swallows as success.
package workflow

import (
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	querybuilder "github.com/vedantadhobley/found-footy/internal/domain/discovery"
)

// DiscoveryWorkflowInput re-exports the shared type so callers that
// only import internal/workflow don't need a second import for the
// spawn payload. See internal/activity/discovery/types.go for the
// canonical declaration.
type DiscoveryWorkflowInput = discoveryactivity.DiscoveryWorkflowInput

// DiscoveryWorkflowOutput reports the run outcome for observability.
type DiscoveryWorkflowOutput struct {
	EventID         uuid.UUID `json:"event_id"`
	Completed       bool      `json:"completed"`
	AttemptsRun     int       `json:"attempts_run"`
	CandidatesFound int       `json:"candidates_found"`
	OutcomeClass    string    `json:"outcome_class"`
}

// Small-activity infra constants that stay hardcoded — they bound
// pg-side calls (FetchTeamAliases / StoreCandidate / MarkComplete)
// which are milliseconds in the happy path. Bumping these is a
// worker-restart concern, not an operator-tuning concern, so no env
// surface. The tunables that DO care about operator control
// (MaxAttempts / AttemptSpacing / MaxAgeMinutes / QueryTimeout) live
// in config.DiscoveryConfig and are read at workflow start via
// GetDiscoveryConfig.
const (
	discoveryPGShortActivityTTL = 30 * time.Second
	discoveryPGRetryAttempts    = 5
)

// DiscoveryWorkflow orchestrates the full candidate collection cycle:
// fetch team aliases → build query → run 10 attempts of /search with
// accumulated exclude_urls → persist each candidate → mark row complete.
func DiscoveryWorkflow(ctx workflow.Context, in DiscoveryWorkflowInput) (DiscoveryWorkflowOutput, error) {
	log := workflow.GetLogger(ctx)
	log.Info("DiscoveryWorkflow started",
		"event_id", in.EventID,
		"fixture_id", in.FixtureID,
		"team_id", in.TeamID,
		"player", in.PlayerName,
		"team", in.TeamName,
		"minute", in.Minute,
	)

	out := DiscoveryWorkflowOutput{EventID: in.EventID}

	// Read tunable config once at workflow start via a config activity
	// (Temporal determinism — workflows can't touch env directly).
	// Mirrors ingest.GetIngestConfig pattern. GetDiscoveryConfig always
	// returns a valid output — fallback values inside the activity
	// cover the zero-value Activities case (tests, mis-wired workers).
	cfgCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: discoveryPGShortActivityTTL,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumAttempts:    discoveryPGRetryAttempts,
		},
	})
	var cfgOut discoveryactivity.GetDiscoveryConfigOutput
	if err := workflow.ExecuteActivity(cfgCtx,
		(*discoveryactivity.Activities).GetDiscoveryConfig,
		discoveryactivity.GetDiscoveryConfigInput{},
	).Get(cfgCtx, &cfgOut); err != nil {
		// Hard-fail this — GetDiscoveryConfig is a trivial in-process
		// call, if it errors something is deeply wrong (Activities
		// not registered? Bad codec?). Better to fail the workflow
		// than silently proceed with unknown attempt/spacing.
		return out, err
	}
	log.Info("discovery config loaded",
		"max_attempts", cfgOut.MaxAttempts,
		"attempt_spacing", cfgOut.AttemptSpacing,
		"max_age_minutes", cfgOut.MaxAgeMinutes,
		"query_timeout", cfgOut.QueryTimeout,
	)

	// D4b guard — Monitor's debounce should hold events until player
	// is known. If it fires empty, log + mark complete with a distinct
	// outcome_class so we can grep Loki for pipeline bugs.
	if in.PlayerName == "" {
		out.OutcomeClass = "unknown_player"
		return finalizeDiscovery(ctx, in, out, log)
	}

	// Step 1: fetch team aliases from pg.
	fetchCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: discoveryPGShortActivityTTL,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumAttempts:    discoveryPGRetryAttempts,
		},
	})
	var aliasesOut discoveryactivity.FetchTeamAliasesOutput
	if err := workflow.ExecuteActivity(fetchCtx,
		(*discoveryactivity.Activities).FetchTeamAliases,
		discoveryactivity.FetchTeamAliasesInput{TeamID: in.TeamID},
	).Get(fetchCtx, &aliasesOut); err != nil {
		log.Warn("FetchTeamAliases failed — falling back to TeamName only",
			"team_id", in.TeamID, "err", err)
	}

	// Step 2: build the query. Canonical name falls back to
	// in.TeamName (api-football team.name) if team_aliases row is
	// unresolved — the query builder always tokenizes canonical, so
	// even with empty aliases we get something to search on.
	canonicalName := aliasesOut.CanonicalName
	if canonicalName == "" {
		canonicalName = in.TeamName
	}
	query, err := querybuilder.Build(in.PlayerName, canonicalName, aliasesOut.Aliases)
	if err != nil {
		log.Warn("query builder rejected input", "err", err,
			"player", in.PlayerName, "canonical", canonicalName,
			"alias_count", len(aliasesOut.Aliases))
		out.OutcomeClass = "empty_query"
		return finalizeDiscovery(ctx, in, out, log)
	}
	log.Info("query built", "query", query, "length", len(query))

	// Step 3: 10-attempt search loop with accumulated exclude_urls.
	//
	// exclude_urls is a workflow-local []string so retries + replays
	// deterministically rebuild the same accumulation. Each attempt's
	// candidates that pass the "new to us" check get appended.
	excludeURLs := make([]string, 0, 64)
	seenTweetIDs := make(map[string]struct{}, 64) // dedup within workflow state

	searchOptions := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: cfgOut.QueryTimeout,
		HeartbeatTimeout:    30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2,
			MaximumAttempts:    3, // per-search transient failures only
		},
	})
	storeOptions := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: discoveryPGShortActivityTTL,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumAttempts:    discoveryPGRetryAttempts,
		},
	})

	for attempt := 1; attempt <= cfgOut.MaxAttempts; attempt++ {
		var searchOut discoveryactivity.SearchTweetsOutput
		if err := workflow.ExecuteActivity(searchOptions,
			(*discoveryactivity.Activities).SearchTweets,
			discoveryactivity.SearchTweetsInput{
				EventID:       in.EventID,
				FixtureID:     in.FixtureID,
				Query:         query,
				ExcludeURLs:   excludeURLs,
				MaxAgeMinutes: cfgOut.MaxAgeMinutes,
			}).Get(searchOptions, &searchOut); err != nil {
			// Transient search failure — log and move on to the next
			// attempt after the spacing timer. Don't fail the whole
			// workflow because one attempt didn't land.
			log.Warn("SearchTweets attempt failed",
				"attempt", attempt, "err", err)
		} else {
			// Persist newly-surfaced candidates.
			for _, v := range searchOut.Videos {
				if v.TweetURL == "" {
					continue
				}
				if _, dup := seenTweetIDs[v.TweetURL]; dup {
					continue
				}
				seenTweetIDs[v.TweetURL] = struct{}{}
				excludeURLs = append(excludeURLs, v.TweetURL)

				var storeOut discoveryactivity.StoreCandidateOutput
				if err := workflow.ExecuteActivity(storeOptions,
					(*discoveryactivity.Activities).StoreCandidate,
					discoveryactivity.StoreCandidateInput{
						EventID:               in.EventID,
						FixtureID:             in.FixtureID,
						SearchAttempt:         attempt,
						Query:                 query,
						TweetURL:              v.TweetURL,
						TweetText:             v.TweetText,
						VideoPageURL:          v.VideoPageURL,
						DurationSeconds:       v.DurationSeconds,
						Username:              v.Username,
						AgeMinutesAtDiscovery: v.AgeMinutes,
					}).Get(storeOptions, &storeOut); err != nil {
					// Don't blow up the workflow on a per-candidate
					// insert failure — log + continue. The candidate
					// data is still in seenTweetIDs so retry-time
					// re-discovery won't produce dupes.
					log.Warn("StoreCandidate failed",
						"tweet_url", v.TweetURL, "err", err)
				} else if storeOut.Inserted {
					out.CandidatesFound++
				}
			}
			log.Info("attempt complete",
				"attempt", attempt,
				"videos_returned", searchOut.Count,
				"cumulative_candidates", out.CandidatesFound,
				"stop_reason", searchOut.StopReason,
			)
		}
		out.AttemptsRun = attempt

		// Wait AttemptSpacing before the next attempt. Skip the timer
		// after the last attempt — no point sleeping when we're about
		// to finalize.
		if attempt < cfgOut.MaxAttempts {
			_ = workflow.Sleep(ctx, cfgOut.AttemptSpacing)
		}
	}

	// Classify the outcome for observability. tweets_found /
	// no_tweets_found preserved from the earlier stub for continuity;
	// added attempts_completed_no_candidates to distinguish "10
	// attempts, zero results" (query miss) from "10 attempts, some
	// results" (normal).
	switch {
	case out.CandidatesFound > 0:
		out.OutcomeClass = "candidates_found"
	default:
		out.OutcomeClass = "no_candidates"
	}
	return finalizeDiscovery(ctx, in, out, log)
}

// finalizeDiscovery is the exit ramp — marks the
// event_downstream_workflows row completed with the outcome_class,
// returns the workflow output. Called from every exit path so the
// checklist row always transitions to completed.
func finalizeDiscovery(
	ctx workflow.Context,
	in DiscoveryWorkflowInput,
	out DiscoveryWorkflowOutput,
	logger log.Logger,
) (DiscoveryWorkflowOutput, error) {
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: discoveryPGShortActivityTTL,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumAttempts:    discoveryPGRetryAttempts,
		},
	})
	var completeOut discoveryactivity.MarkDownstreamCompleteOutput
	if err := workflow.ExecuteActivity(actCtx,
		(*discoveryactivity.Activities).MarkDownstreamComplete,
		discoveryactivity.MarkDownstreamCompleteInput{
			EventID:      in.EventID,
			WorkflowType: "discovery",
			WorkflowID:   workflow.GetInfo(ctx).WorkflowExecution.ID,
			OutcomeClass: out.OutcomeClass,
		}).Get(actCtx, &completeOut); err != nil {
		logger.Warn("MarkDownstreamComplete failed", "err", err)
		return out, err
	}

	out.Completed = true
	logger.Info("DiscoveryWorkflow finished",
		"event_id", in.EventID,
		"attempts_run", out.AttemptsRun,
		"candidates_found", out.CandidatesFound,
		"outcome", out.OutcomeClass,
	)
	return out, nil
}
