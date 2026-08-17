// EventWorkflow — the per-goal orchestrator. Runs a PRODUCER (the discovery
// phase: Twitter search + candidate collection) concurrently with a CONSUMER
// (spawn VideoWorkflow children → dedup → vision → promote → rank) —
// #164c + #165, shipped. Renamed from DiscoveryWorkflow (Option 2 rename,
// decisions.md 2026-08-03: the workflow became the event orchestrator, so
// "Discovery" undersold it; the discovery *phase* — config + activities —
// keeps its accurate name).
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
// event_search_candidates for downstream video processing and post-hoc
// query-quality learning.
//
// Deterministic workflow ID convention: "event-{event_id}" so
// the row inserted by Monitor pairs 1:1 with the Temporal WorkflowID
// under RejectDuplicate policy. (The pg event_downstream_workflows
// workflow_type value stays "discovery" — the internal label for the
// event's downstream workflow, filtered by EventsAwaitingDiscovery.)
// Activity retries after partial-
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
	fleetactivity "github.com/vedantadhobley/found-footy/internal/activity/fleet"
	querybuilder "github.com/vedantadhobley/found-footy/internal/domain/discovery"
)

// EventWorkflowInput re-exports the shared type so callers that
// only import internal/workflow don't need a second import for the
// spawn payload. See internal/activity/discovery/types.go for the
// canonical declaration.
type EventWorkflowInput = discoveryactivity.EventWorkflowInput

// EventWorkflowOutput reports the run outcome for observability.
type EventWorkflowOutput struct {
	EventID         uuid.UUID `json:"event_id"`
	Completed       bool      `json:"completed"`
	AttemptsRun     int       `json:"attempts_run"`
	CandidatesFound int       `json:"candidates_found"`
	AssetsKept      int       `json:"assets_kept"` // verified + unverified clips surfaced
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

// EventWorkflow orchestrates the full candidate collection cycle:
// fetch team aliases → build query → run 10 attempts of /search with
// accumulated exclude_urls → persist each candidate → mark row complete.
func EventWorkflow(ctx workflow.Context, in EventWorkflowInput) (EventWorkflowOutput, error) {
	log := workflow.GetLogger(ctx)
	log.Info("EventWorkflow started",
		"event_id", in.EventID,
		"fixture_id", in.FixtureID,
		"team_id", in.TeamID,
		"player", in.PlayerName,
		"team", in.TeamName,
		"minute", in.Minute,
	)

	out := EventWorkflowOutput{EventID: in.EventID}

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

	// #160: when the Firefox fleet is enabled, the monitor provisioned a
	// dedicated per-event instance at debounce count=1 (warm by now).
	// Derive its deterministic address (no registry) and target searches
	// at it. Empty (fleet off) → SearchTweets uses the shared service.
	instanceAddr := ""
	if cfgOut.FleetEnabled {
		instanceAddr = fleetactivity.InstanceAddr(in.EventID)
	}

	// D4b guard — Monitor's debounce should hold events until player
	// is known. If it fires empty, log + mark complete with a distinct
	// outcome_class so we can grep Loki for pipeline bugs.
	if in.PlayerName == "" {
		out.OutcomeClass = "unknown_player"
		return finalizeEvent(ctx, in, out, log, cfgOut.FleetEnabled)
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
		if temporal.IsCanceledError(err) {
			return out, err
		}
		log.Warn("FetchTeamAliases failed — falling back to TeamName only",
			"team_id", in.TeamID, "err", err)
	}

	// Step 2: build the query. Canonical name falls back to
	// in.TeamName (api-football team.name) if team_aliases row is
	// unresolved — the builder always emits the quoted canonical + a
	// derived abbreviation, so we always have something to search on.
	canonicalName := aliasesOut.CanonicalName
	if canonicalName == "" {
		canonicalName = in.TeamName
	}
	// Resolved aliases are DISCONNECTED (nil). The Wikipedia→Wikidata alias set
	// is contaminated (generic/junk/wrong-entity tokens: FC Cincinnati →
	// holdings/llc/knifey, Toronto FC → York-United york9) and measured
	// net-negative on a live match day — the junk OR terms crowd official clips
	// out of the live-search scroll budget (0 official WITH aliases → 6 WITHOUT
	// for Cincinnati). Canonical name + derived abbrev carry the team. The
	// resolver + team_aliases.aliases column stay in place but unused, pending a
	// Phase-2 teardown. See decisions.md 2026-08-16.
	query, err := querybuilder.Build(in.PlayerName, canonicalName, nil)
	if err != nil {
		log.Warn("query builder rejected input", "err", err,
			"player", in.PlayerName, "canonical", canonicalName,
			"alias_count", len(aliasesOut.Aliases))
		out.OutcomeClass = "empty_query"
		return finalizeEvent(ctx, in, out, log, cfgOut.FleetEnabled)
	}
	log.Info("query built", "query", query, "length", len(query))

	// Step 3: the pipeline — a PRODUCER coroutine (the discovery search loop,
	// spawning a VideoWorkflow child per new candidate) running concurrently
	// with the CONSUMER (the serialized Selector queue: dedup → vision →
	// promote → rank). Temporal owns completion: the consumer returns when
	// search is done AND nothing is in flight — no idle timeout.
	p := newPipeline(ctx, in, pipelineConfig{
		maxHamming: cfgOut.MaxHamming, minRun: cfgOut.MinRunFrames, maxGaps: cfgOut.MaxGapFrames,
	}, log)

	// exclude_urls + seenTweetIDs are workflow-local so retries/replays
	// deterministically rebuild the same accumulation.
	excludeURLs := make([]string, 0, 64)
	seenTweetIDs := make(map[string]struct{}, 64)

	workflow.Go(ctx, func(gctx workflow.Context) {
		var producerErr error
		defer func() {
			p.finishSearch(producerErr)
		}()

		searchOptions := workflow.WithActivityOptions(gctx, workflow.ActivityOptions{
			StartToCloseTimeout: cfgOut.QueryTimeout,
			HeartbeatTimeout:    30 * time.Second,
			RetryPolicy:         &temporal.RetryPolicy{InitialInterval: 2 * time.Second, BackoffCoefficient: 2, MaximumAttempts: 3},
		})
		storeOptions := workflow.WithActivityOptions(gctx, workflow.ActivityOptions{
			StartToCloseTimeout: discoveryPGShortActivityTTL,
			RetryPolicy:         &temporal.RetryPolicy{InitialInterval: time.Second, BackoffCoefficient: 2, MaximumAttempts: discoveryPGRetryAttempts},
		})

		for attempt := 1; attempt <= cfgOut.MaxAttempts; attempt++ {
			var searchOut discoveryactivity.SearchTweetsOutput
			if err := workflow.ExecuteActivity(searchOptions,
				(*discoveryactivity.Activities).SearchTweets,
				discoveryactivity.SearchTweetsInput{
					EventID: in.EventID, FixtureID: in.FixtureID, Query: query,
					ExcludeURLs: excludeURLs, MaxAgeMinutes: cfgOut.MaxAgeMinutes,
					InstanceAddr: instanceAddr,
				}).Get(searchOptions, &searchOut); err != nil {
				if temporal.IsCanceledError(err) {
					producerErr = err
					return
				}
				log.Warn("SearchTweets attempt failed", "attempt", attempt, "err", err)
			} else {
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
							EventID: in.EventID, FixtureID: in.FixtureID, SearchAttempt: attempt,
							Query: query, TweetURL: v.TweetURL, TweetText: v.TweetText,
							VideoPageURL: v.VideoPageURL, DurationSeconds: v.DurationSeconds,
							Username: v.Username, AgeMinutesAtDiscovery: v.AgeMinutes,
						}).Get(storeOptions, &storeOut); err != nil {
						if temporal.IsCanceledError(err) {
							producerErr = err
							return
						}
						log.Warn("StoreCandidate failed", "tweet_url", v.TweetURL, "err", err)
					} else if storeOut.Inserted {
						out.CandidatesFound++
					}
					// Spawn the per-candidate Video child (candidate persistence
					// is post-hoc learning; the pipeline processes the clip
					// regardless of the StoreCandidate result).
					p.spawnChild(gctx, v.TweetURL)
				}
				log.Info("attempt complete", "attempt", attempt, "videos_returned", searchOut.Count,
					"cumulative_candidates", out.CandidatesFound, "stop_reason", searchOut.StopReason)
			}
			out.AttemptsRun = attempt
			if attempt < cfgOut.MaxAttempts {
				if err := workflow.Sleep(gctx, cfgOut.AttemptSpacing); err != nil {
					producerErr = err
					return
				}
			}
		}
	})

	// The consumer propagates cancellation instead of repeatedly awaiting an
	// already-canceled context. That tight loop caused FF-015's Temporal
	// deadlock detector failures.
	if err := p.run(); err != nil {
		return out, err
	}

	// Live assets only — supersede removes losers from p.assets, so this is the
	// surviving count (verified+unverified are cumulative promote counts, which
	// now over-count by the number superseded).
	out.AssetsKept = len(p.assets)
	switch {
	case out.AssetsKept > 0:
		out.OutcomeClass = "assets_surfaced"
	case p.spawned > 0:
		out.OutcomeClass = "candidates_no_assets"
	default:
		out.OutcomeClass = "no_candidates"
	}
	log.Info("event pipeline complete",
		"spawned", p.spawned, "passed", p.passed, "duplicates", p.duplicates,
		"verified", p.verified, "unverified", p.unverified, "superseded", p.superseded,
		"assets_kept", out.AssetsKept, "rejected", p.rejectedClips, "failed", p.failed)
	return finalizeEvent(ctx, in, out, log, cfgOut.FleetEnabled)
}

// finalizeEvent is the exit ramp — marks the
// event_downstream_workflows row completed with the outcome_class,
// returns the workflow output. Called from every exit path so the
// checklist row always transitions to completed.
func finalizeEvent(
	ctx workflow.Context,
	in EventWorkflowInput,
	out EventWorkflowOutput,
	logger log.Logger,
	fleetEnabled bool,
) (EventWorkflowOutput, error) {
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

	// #160: release this event's Firefox instance on normal completion.
	// Best-effort — the row is already marked complete, and the reaper
	// sweeps an orphan, so a failed release must not fail the event.
	// No-op when the fleet is disabled (ReleaseFirefox short-circuits on a
	// nil Fleet). The VAR-cancel + pre-trigger-decay releases live on the
	// monitor side (a cancelled workflow never reaches here).
	if fleetEnabled {
		relCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: discoveryPGShortActivityTTL,
			RetryPolicy: &temporal.RetryPolicy{
				InitialInterval:    time.Second,
				BackoffCoefficient: 2,
				MaximumAttempts:    discoveryPGRetryAttempts,
			},
		})
		if err := workflow.ExecuteActivity(relCtx, "ReleaseFirefox",
			fleetactivity.ReleaseFirefoxInput{EventID: in.EventID}).Get(relCtx, nil); err != nil {
			logger.Warn("ReleaseFirefox failed (reaper will sweep)", "event_id", in.EventID, "err", err)
		}
	}

	out.Completed = true
	logger.Info("EventWorkflow finished",
		"event_id", in.EventID,
		"attempts_run", out.AttemptsRun,
		"candidates_found", out.CandidatesFound,
		"outcome", out.OutcomeClass,
	)
	return out, nil
}
