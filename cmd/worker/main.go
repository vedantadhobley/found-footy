// Command worker is the Temporal worker binary — registers workflows and
// activities and processes tasks from the found-footy task queue. See §5
// orchestration + §16.5 Phase O for the workflows this binary hosts.
//
// Phase S2.4: opens the pg pool at startup, blocks until SIGINT/SIGTERM,
// closes the pool cleanly on shutdown. Workflow/activity registration
// lands in Phase O when the domain layer is ready to be plugged in.
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	ingestactivity "github.com/vedantadhobley/found-footy/internal/activity/ingest"
	monitoractivity "github.com/vedantadhobley/found-footy/internal/activity/monitor"
	"github.com/vedantadhobley/found-footy/internal/bootstrap"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
	eventinfra "github.com/vedantadhobley/found-footy/internal/infra/event"
	"github.com/vedantadhobley/found-footy/internal/infra/llm"
	"github.com/vedantadhobley/found-footy/internal/infra/nats"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
	"github.com/vedantadhobley/found-footy/internal/infra/s3"
	"github.com/vedantadhobley/found-footy/internal/infra/syndication"
	"github.com/vedantadhobley/found-footy/internal/infra/temporal"
	"github.com/vedantadhobley/found-footy/internal/infra/twitter"
	"github.com/vedantadhobley/found-footy/internal/infra/wikidata"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
	ffwf "github.com/vedantadhobley/found-footy/internal/workflow"
)

// gitSHA, builtAt are baked in at build time via -ldflags per §11
// deploy tracking. Empty defaults for direct `go run` invocations.
var (
	gitSHA  = "dev"
	builtAt = "unknown"
)

func main() {
	bootstrap.Run("worker", gitSHA, builtAt, func(ctx context.Context, deps *bootstrap.Deps) error {
		pgIns := pg.RegisterMetrics(deps.Metrics, deps.Log)
		pool, err := pg.New(ctx, deps.Cfg.Postgres, pgIns)
		if err != nil {
			return err
		}
		deps.RegisterCloser("pg", func(_ context.Context) error {
			pool.Close()
			return nil
		})

		natsIns := nats.RegisterMetrics(deps.Metrics, deps.Log)
		nc, err := nats.New(ctx, deps.Cfg.NATS, natsIns)
		if err != nil {
			return err
		}
		deps.RegisterCloser("nats", func(_ context.Context) error {
			nc.Close()
			return nil
		})

		s3Ins := s3.RegisterMetrics(deps.Metrics, deps.Log)
		s3c, err := s3.New(ctx, deps.Cfg.S3, s3Ins)
		if err != nil {
			return err
		}
		_ = s3c // consumed by the video pipeline in Phase O
		// s3 client has no explicit Close (no persistent connection); no
		// closer needed — leaving it out is intentional and symmetric
		// with the aws-sdk-go-v2 client's lifecycle.

		llmIns := llm.RegisterMetrics(deps.Metrics, deps.Log)
		llmClient, err := llm.NewClient(ctx, deps.Cfg.LLM, llmIns)
		if err != nil {
			return err
		}
		deps.RegisterCloser("llm", func(_ context.Context) error {
			llmClient.Close()
			return nil
		})
		_ = llmClient // consumed by vision + RAG activities in Phase O

		afIns := apifootball.RegisterMetrics(deps.Metrics, deps.Log)
		afClient, err := apifootball.NewClient(ctx, deps.Cfg.APIFootball, afIns)
		if err != nil {
			return err
		}
		// No closer — http.Client has no persistent state to drain.

		wdIns := wikidata.RegisterMetrics(deps.Metrics, deps.Log)
		wdClient, err := wikidata.NewClient(deps.Cfg.Wikidata, wdIns)
		if err != nil {
			return err
		}
		_ = wdClient // consumed by RAG alias activity in Phase O

		syndIns := syndication.RegisterMetrics(deps.Metrics, deps.Log)
		syndClient, err := syndication.NewClient(deps.Cfg.Syndication, syndIns)
		if err != nil {
			return err
		}
		_ = syndClient // consumed by tweet-content activities in Phase O

		// Option 1 wire (2026-07-17): point Go DiscoveryWorkflow at a
		// running Twitter service via S7's HTTP client. The dev
		// twitter container currently runs a Go stub (no HTTP surface)
		// so this NewClient call FAILS in dev — we tolerate that so
		// the worker still boots. When someone stands up the Python
		// twitter service in dev (or when T ships a real Go
		// implementation), the client goes live automatically.
		// Discovery activities check for nil and surface a clear
		// "twitter client not wired" error via Temporal retries.
		twitterIns := twitter.RegisterMetrics(deps.Metrics, deps.Log)
		var twitterClient *twitter.Client
		if c, err := twitter.NewClient(ctx, deps.Cfg.Twitter, twitterIns); err == nil {
			twitterClient = c
		} else {
			deps.Log.Emit(ctx, logging.LevelWarn, vocabulary.ModuleInfraTwitter, vocabulary.ActionTwitterConnectFailed,
				"twitter service unreachable at startup — Discovery searches will fail until it's up",
				logging.String("base_url", deps.Cfg.Twitter.BaseURL),
				logging.Err(err),
			)
		}

		tempIns := temporal.RegisterMetrics(deps.Metrics, deps.Log)
		tempClient, err := temporal.NewClient(ctx, deps.Cfg.Temporal, tempIns)
		if err != nil {
			return err
		}
		deps.RegisterCloser("temporal-client", func(_ context.Context) error {
			tempClient.Close()
			return nil
		})

		w := temporal.NewWorker(tempClient, tempIns, worker.Options{})

		// Phase O1d — IngestWorkflow + its four activities.
		// Repos + adapters constructed above are injected into the
		// Activities struct; workflow + activities registered under
		// their default (short function name) identifiers.
		fixtureRepo := pg.NewFixtureRepo(pool)
		aliasRepo := pg.NewAliasRepo(pool)
		eventRepo := pg.NewEventRepo(pool)
		teamRepo := pg.NewTeamRepo(pool)

		ingestActs := &ingestactivity.Activities{
			APIFootball:           afClient,
			FixtureRepo:           fixtureRepo,
			AliasRepo:             aliasRepo,
			TeamRepo:              teamRepo,
			TrackedLeagueIDs:      deps.Cfg.APIFootball.TrackedLeagueIDs,
			TopFlightCacheHours:   deps.Cfg.APIFootball.TopFlightCacheHours,
			FetchWindowFutureDays: deps.Cfg.APIFootball.FetchWindowFutureDays,
			ActivationWindow:      deps.Cfg.Workflows.ActivationWindow,
			RetentionDays:         deps.Cfg.Workflows.RetentionDays,
		}
		w.RegisterWorkflow(ffwf.IngestWorkflow)
		w.RegisterActivity(ingestActs)

		// Phase O3/a — event composer for dual-write to pg event_log +
		// NATS. See decisions.md 2026-07-16 for the NATS-scope narrowing.
		composerIns := eventinfra.RegisterMetrics(deps.Metrics, deps.Log)
		composer, err := eventinfra.New(pool, nc, composerIns)
		if err != nil {
			return err
		}

		// Phase O3/b — spawner for DiscoveryWorkflow, bundled with the
		// event_downstream_workflows row insert in the same activity.
		spawner := monitoractivity.NewTemporalSpawner(tempClient, 10*time.Second)

		// Phase O3/c + 2026-07-17 Option-1 wire: Discovery activities
		// call the Twitter service for real search, then mark the
		// event_downstream_workflows row complete. Only assign
		// Twitter when the concrete pointer is non-nil — assigning a
		// nil *twitter.Client to an interface field makes the field
		// non-nil (holding a nil pointer), which would defeat the
		// SearchTweets nil-check and cause a nil-deref panic.
		discoveryActs := &discoveryactivity.Activities{
			Pool: pool,
		}
		if twitterClient != nil {
			discoveryActs.Twitter = twitterClient
		}

		// Phase O2 — the two poll workflows share one activities struct.
		// Shares fixtureRepo + eventRepo with the rest of the worker.
		// Now clock left nil → real wall clock in prod (per the
		// injectable-clock discipline for scenario testing).
		monitorActs := &monitoractivity.Activities{
			APIFootball:      afClient,
			FixtureRepo:      fixtureRepo,
			EventRepo:        eventRepo,
			Composer:         composer,
			Spawner:          spawner,
			ActivationWindow: deps.Cfg.Workflows.ActivationWindow,
		}
		w.RegisterWorkflow(ffwf.ActivePollWorkflow)
		w.RegisterWorkflow(ffwf.StagingPollWorkflow)
		w.RegisterWorkflow(ffwf.DiscoveryWorkflow)
		w.RegisterActivity(monitorActs)
		w.RegisterActivity(discoveryActs)

		if err := w.Start(ctx); err != nil {
			return err
		}
		// Worker shutdown MUST run before its downstream deps close so
		// draining activities can still use pg/nats/s3. LIFO order
		// (temporal-worker registered last → drained first) gives us this.
		deps.RegisterCloser("temporal-worker", func(_ context.Context) error {
			w.Stop()
			return nil
		})

		// Phase O1e/b — register the daily IngestWorkflow schedule.
		// Idempotent: subsequent worker restarts hit ErrScheduleAlreadyRunning
		// and treat it as success. Manual updates via `temporal schedule
		// update` are safe; this code will not overwrite them.
		if err := ensureIngestSchedule(ctx, tempClient, deps); err != nil {
			return err
		}

		// Phase O2 — register the two poll workflow schedules. Both
		// idempotent (ErrScheduleAlreadyRunning → success). Independent
		// Temporal Schedules so ops can `temporal schedule update`
		// either cadence at runtime without a redeploy.
		if err := ensureActivePollSchedule(ctx, tempClient, deps); err != nil {
			return err
		}
		if err := ensureStagingPollSchedule(ctx, tempClient, deps); err != nil {
			return err
		}

		<-ctx.Done()
		return nil
	})
}

// ensureIngestSchedule registers the daily IngestWorkflow Temporal
// Schedule if it doesn't exist. Empty input means the workflow
// self-configures its anchor + defaults (see internal/workflow/ingest.go
// IngestWorkflowInput docstring). RetentionDays=14 is the plan §5 W1
// default — sent explicitly so the workflow prunes 14-day-old
// completed fixtures each daily run.
func ensureIngestSchedule(ctx context.Context, tempClient *temporal.Client, deps *bootstrap.Deps) error {
	const scheduleID = "ingest-scheduled-daily"

	_, err := tempClient.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID: scheduleID,
		Spec: client.ScheduleSpec{
			CronExpressions: []string{"5 0 * * *"}, // 00:05 UTC daily
		},
		Action: &client.ScheduleWorkflowAction{
			ID:        "ingest-scheduled",
			Workflow:  ffwf.IngestWorkflow,
			TaskQueue: tempClient.TaskQueue(),
			Args: []any{ffwf.IngestWorkflowInput{
				RetentionDays: 14,   // plan §5 W1 default retention
				FetchFuture:   true, // scheduled runs get the full future-days window
			}},
		},
		Overlap: enums.SCHEDULE_OVERLAP_POLICY_SKIP,
	})
	if err != nil {
		if errors.Is(err, sdktemporal.ErrScheduleAlreadyRunning) {
			deps.Log.Emit(ctx, logging.LevelInfo,
				vocabulary.ModuleInfraTemporal, vocabulary.ActionTemporalScheduleAlreadyExists,
				"IngestWorkflow schedule already registered",
				logging.String("schedule_id", scheduleID),
			)
			return nil
		}
		deps.Log.Emit(ctx, logging.LevelError,
			vocabulary.ModuleInfraTemporal, vocabulary.ActionTemporalScheduleFailed,
			"failed to create IngestWorkflow schedule",
			logging.String("schedule_id", scheduleID),
			logging.Err(err),
		)
		return fmt.Errorf("create ingest schedule: %w", err)
	}
	deps.Log.Emit(ctx, logging.LevelInfo,
		vocabulary.ModuleInfraTemporal, vocabulary.ActionTemporalScheduleCreated,
		"IngestWorkflow schedule registered",
		logging.String("schedule_id", scheduleID),
		logging.String("cron", "5 0 * * *"),
	)
	return nil
}

// ensureActivePollSchedule registers the ActivePollWorkflow Temporal
// Schedule if it doesn't exist. Uses an interval spec (cron doesn't
// support sub-minute resolution). Overlap SKIP: if the prior cycle is
// still running when the next tick fires, we skip — better than
// double-fanning-out reconcile activities.
//
// Interval sourced from config.Workflows.ActiveFixturePollInterval
// (default 30s). The schedule ID is intentionally NOT interval-
// dependent — if config changes, the existing schedule keeps running
// under its Temporal-state settings until an operator deletes + recreates
// or runs `temporal schedule update`. That's intentional (schedule
// config lives in Temporal state, not code).
func ensureActivePollSchedule(ctx context.Context, tempClient *temporal.Client, deps *bootstrap.Deps) error {
	const scheduleID = "active-poll-scheduled"

	_, err := tempClient.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID: scheduleID,
		Spec: client.ScheduleSpec{
			Intervals: []client.ScheduleIntervalSpec{
				{Every: deps.Cfg.Workflows.ActiveFixturePollInterval},
			},
		},
		Action: &client.ScheduleWorkflowAction{
			ID:        "active-poll-scheduled",
			Workflow:  ffwf.ActivePollWorkflow,
			TaskQueue: tempClient.TaskQueue(),
			Args:      []any{ffwf.ActivePollWorkflowInput{}},
		},
		Overlap: enums.SCHEDULE_OVERLAP_POLICY_SKIP,
	})
	if err != nil {
		if errors.Is(err, sdktemporal.ErrScheduleAlreadyRunning) {
			deps.Log.Emit(ctx, logging.LevelInfo,
				vocabulary.ModuleInfraTemporal, vocabulary.ActionTemporalScheduleAlreadyExists,
				"ActivePollWorkflow schedule already registered",
				logging.String("schedule_id", scheduleID),
			)
			return nil
		}
		deps.Log.Emit(ctx, logging.LevelError,
			vocabulary.ModuleInfraTemporal, vocabulary.ActionTemporalScheduleFailed,
			"failed to create ActivePollWorkflow schedule",
			logging.String("schedule_id", scheduleID),
			logging.Err(err),
		)
		return fmt.Errorf("create active-poll schedule: %w", err)
	}
	deps.Log.Emit(ctx, logging.LevelInfo,
		vocabulary.ModuleInfraTemporal, vocabulary.ActionTemporalScheduleCreated,
		"ActivePollWorkflow schedule registered",
		logging.String("schedule_id", scheduleID),
		logging.String("interval", deps.Cfg.Workflows.ActiveFixturePollInterval.String()),
	)
	return nil
}

// ensureStagingPollSchedule registers the StagingPollWorkflow Temporal
// Schedule if it doesn't exist. Cron-driven (default `*/15 * * * *`).
// Tunable at runtime via `temporal schedule update
// staging-poll-scheduled --cron ...`.
func ensureStagingPollSchedule(ctx context.Context, tempClient *temporal.Client, deps *bootstrap.Deps) error {
	const scheduleID = "staging-poll-scheduled"
	cronExpr := deps.Cfg.Workflows.StagingPollCron

	_, err := tempClient.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID: scheduleID,
		Spec: client.ScheduleSpec{
			CronExpressions: []string{cronExpr},
		},
		Action: &client.ScheduleWorkflowAction{
			ID:        "staging-poll-scheduled",
			Workflow:  ffwf.StagingPollWorkflow,
			TaskQueue: tempClient.TaskQueue(),
			Args:      []any{ffwf.StagingPollWorkflowInput{}},
		},
		Overlap: enums.SCHEDULE_OVERLAP_POLICY_SKIP,
	})
	if err != nil {
		if errors.Is(err, sdktemporal.ErrScheduleAlreadyRunning) {
			deps.Log.Emit(ctx, logging.LevelInfo,
				vocabulary.ModuleInfraTemporal, vocabulary.ActionTemporalScheduleAlreadyExists,
				"StagingPollWorkflow schedule already registered",
				logging.String("schedule_id", scheduleID),
			)
			return nil
		}
		deps.Log.Emit(ctx, logging.LevelError,
			vocabulary.ModuleInfraTemporal, vocabulary.ActionTemporalScheduleFailed,
			"failed to create StagingPollWorkflow schedule",
			logging.String("schedule_id", scheduleID),
			logging.Err(err),
		)
		return fmt.Errorf("create staging-poll schedule: %w", err)
	}
	deps.Log.Emit(ctx, logging.LevelInfo,
		vocabulary.ModuleInfraTemporal, vocabulary.ActionTemporalScheduleCreated,
		"StagingPollWorkflow schedule registered",
		logging.String("schedule_id", scheduleID),
		logging.String("cron", cronExpr),
	)
	return nil
}
