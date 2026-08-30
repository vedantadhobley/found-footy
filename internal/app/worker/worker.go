// Package worker composes the production worker's adapters, Temporal worker,
// activity registrations, workflow registrations, and schedules.
package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"

	"github.com/vedantadhobley/found-footy/migrations"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	fleetactivity "github.com/vedantadhobley/found-footy/internal/activity/fleet"
	ingestactivity "github.com/vedantadhobley/found-footy/internal/activity/ingest"
	livefeedactivity "github.com/vedantadhobley/found-footy/internal/activity/livefeed"
	monitoractivity "github.com/vedantadhobley/found-footy/internal/activity/monitor"
	retentionactivity "github.com/vedantadhobley/found-footy/internal/activity/retention"
	twittermaintenanceactivity "github.com/vedantadhobley/found-footy/internal/activity/twittermaintenance"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	visionactivity "github.com/vedantadhobley/found-footy/internal/activity/vision"
	"github.com/vedantadhobley/found-footy/internal/bootstrap"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
	eventinfra "github.com/vedantadhobley/found-footy/internal/infra/event"
	"github.com/vedantadhobley/found-footy/internal/infra/ffmpeg"
	"github.com/vedantadhobley/found-footy/internal/infra/firefoxfleet"
	"github.com/vedantadhobley/found-footy/internal/infra/llm"
	"github.com/vedantadhobley/found-footy/internal/infra/nats"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
	"github.com/vedantadhobley/found-footy/internal/infra/s3"
	"github.com/vedantadhobley/found-footy/internal/infra/syndication"
	"github.com/vedantadhobley/found-footy/internal/infra/temporal"
	"github.com/vedantadhobley/found-footy/internal/infra/twitter"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
	ffwf "github.com/vedantadhobley/found-footy/internal/workflow"
)

// Run composes and serves the Temporal worker until ctx is canceled.
func Run(ctx context.Context, deps *bootstrap.Deps) error {
	pgIns := pg.RegisterMetrics(deps.Metrics, deps.Log)
	pool, err := pg.New(ctx, deps.Cfg.Postgres, pgIns)
	if err != nil {
		return err
	}
	deps.RegisterCloser("pg", func(_ context.Context) error {
		pool.Close()
		return nil
	})
	// The dedicated migrate command owns schema mutation. Worker startup is a
	// read-only gate on the exact checksummed ledger and required objects.
	if err := pool.VerifyMigrations(ctx, migrations.FS); err != nil {
		return err
	}

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
	_ = s3c // consumed by the video pipeline wired below
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

	afIns := apifootball.RegisterMetrics(deps.Metrics, deps.Log)
	afClient, err := apifootball.NewClient(ctx, deps.Cfg.APIFootball, afIns)
	if err != nil {
		return err
	}
	// No closer — http.Client has no persistent state to drain.

	syndIns := syndication.RegisterMetrics(deps.Metrics, deps.Log)
	syndClient, err := syndication.NewClient(deps.Cfg.Syndication, syndIns)
	if err != nil {
		return err
	}
	_ = syndClient // consumed by video activities wired below

	// Client construction validates static config but performs no readiness
	// probe. Twitter and per-event browsers can start or recover independently;
	// each Search observes current service state under Temporal retry (FF-016).
	twitterIns := twitter.RegisterMetrics(deps.Metrics, deps.Log)
	twitterClient, err := twitter.NewClient(deps.Cfg.Twitter, twitterIns)
	if err != nil {
		return err
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

	// Concurrency caps (audit-2026-08-05 Tier-2 #8): zero-value
	// Options defaults to ~1000 concurrent activities, an OOM risk on
	// this memory-budgeted host under multi-match load. Bound both
	// from config (defaults mirror Python's 30 / 10).
	w := temporal.NewWorker(tempClient, tempIns, worker.Options{
		MaxConcurrentActivityExecutionSize:     deps.Cfg.Temporal.MaxConcurrentActivities,
		MaxConcurrentWorkflowTaskExecutionSize: deps.Cfg.Temporal.MaxConcurrentWorkflowTasks,
	})

	// Repos + adapters constructed above are injected into the
	// Activities struct; workflow + activities registered under
	// their default (short function name) identifiers.
	fixtureRepo := pg.NewFixtureRepo(pool)
	aliasRepo := pg.NewAliasRepo(pool)
	eventRepo := pg.NewEventRepo(pool)
	teamRepo := pg.NewTeamRepo(pool)
	assetRepo := pg.NewAssetRepo(pool)
	shareRepo := pg.NewShareRepo(pool)
	placementRepo := pg.NewPlacementRepo(pool)

	ingestActs := &ingestactivity.Activities{
		APIFootball:           afClient,
		FixtureRepo:           fixtureRepo,
		AliasRepo:             aliasRepo,
		TeamRepo:              teamRepo,
		TrackedLeagueIDs:      deps.Cfg.APIFootball.TrackedLeagueIDs,
		TopFlightCacheHours:   deps.Cfg.APIFootball.TopFlightCacheHours,
		FetchWindowFutureDays: deps.Cfg.APIFootball.FetchWindowFutureDays,
		ActivationWindow:      deps.Cfg.Workflows.ActivationWindow,
		CompletedFixtureDates: deps.Cfg.History.CompletedFixtureDates,
	}
	w.RegisterWorkflow(ffwf.IngestWorkflow)
	w.RegisterActivity(ingestActs)
	w.RegisterActivity(&retentionactivity.Activities{Fixtures: fixtureRepo, Assets: assetRepo})

	// The spawner starts EventWorkflow and registers its downstream
	// event_downstream_workflows row insert in the same activity.
	spawner := monitoractivity.NewTemporalSpawner(
		tempClient,
		10*time.Second,
		monitoractivity.ConservativeEventStaleAfter(
			deps.Cfg.Discovery.AttemptSpacing,
			deps.Cfg.Discovery.QueryTimeout,
		),
	)

	// Discovery activities call the Twitter service for real search, then mark the
	// event_downstream_workflows row complete. Only assign
	// Twitter when the concrete pointer is non-nil — assigning a
	// nil *twitter.Client to an interface field makes the field
	// non-nil (holding a nil pointer), which would defeat the
	// SearchTweets nil-check and cause a nil-deref panic.
	discoveryActs := &discoveryactivity.Activities{
		Pool:       pool,
		Downstream: eventRepo,
		// #162 — Discovery tunables from env-driven config, exposed
		// to the workflow via GetDiscoveryConfig activity. Zero-value
		// safety: GetDiscoveryConfig falls back to hardcoded defaults
		// per-field if any of these are unset (tests / mis-wired).
		MaxAttempts:            deps.Cfg.Discovery.MaxAttempts,
		MaxUnavailableAttempts: deps.Cfg.Discovery.MaxUnavailableAttempts,
		AttemptSpacing:         deps.Cfg.Discovery.AttemptSpacing,
		MaxAgeMinutes:          deps.Cfg.Discovery.MaxAgeMinutes,
		QueryTimeout:           deps.Cfg.Discovery.QueryTimeout,
		// Dedup thresholds surfaced to EventWorkflow's in-code video.Match.
		MaxHamming:       deps.Cfg.Dedup.MaxHamming,
		MinRunFrames:     deps.Cfg.Dedup.MinRunFrames,
		MaxGapFrames:     deps.Cfg.Dedup.MaxGapFrames,
		LongMaxHamming:   deps.Cfg.Dedup.LongMaxHamming,
		LongMinRunFrames: deps.Cfg.Dedup.LongMinRunFrames,
		LongMaxGapFrames: deps.Cfg.Dedup.LongMaxGapFrames,
		FleetEnabled:     deps.Cfg.FirefoxFleet.Enabled,
	}
	discoveryActs.Twitter = twitterClient

	// Per-candidate video activities run DownloadAndStage and
	// HashVideo. The ffmpeg client is constructed here,
	// now that an activity consumes it; the syndication + s3 clients from
	// above are reused.
	ffmpegIns := ffmpeg.RegisterMetrics(deps.Metrics, deps.Log)
	ffmpegClient, err := ffmpeg.NewClient(deps.Cfg.FFmpeg, ffmpegIns)
	if err != nil {
		return err
	}
	videoActs := &videoactivity.Activities{
		Syndication:       syndClient,
		FFmpeg:            ffmpegClient,
		S3:                s3c,
		ScratchDir:        deps.Cfg.Video.ScratchDir,
		StagingPrefix:     deps.Cfg.Video.StagingPrefix,
		Thresholds:        videoactivity.ThresholdsFromConfig(deps.Cfg.Video.HardFilter),
		FrameIntervalSecs: deps.Cfg.Dedup.FrameIntervalSecs,
		MinHashFrames:     deps.Cfg.Dedup.MinRunFrames,
	}

	// Clip validation applies the soccer/screen gate and clock check.
	// Reuses the ffmpeg + s3 clients and the shared LLM client (the LLM's
	// only consumer today; point LLM_ENDPOINT_URL at the vision node).
	visionActs := &visionactivity.Activities{
		FFmpeg:     ffmpegClient,
		S3:         s3c,
		LLM:        llmClient,
		ScratchDir: deps.Cfg.Video.ScratchDir,
		Cfg:        deps.Cfg.Vision,
	}

	// EventWorkflow consumer-queue persistence promotes
	// staging→assets + insert asset/share/rank, collapse-bump, staging cleanup.
	persistActs := &videoactivity.PersistActivities{
		S3:           s3c,
		Assets:       assetRepo,
		Shares:       shareRepo,
		Placements:   placementRepo,
		Bucket:       deps.Cfg.S3.Bucket,
		AssetsPrefix: deps.Cfg.Video.AssetsPrefix,
	}

	// The two poll workflows share one activities struct.
	// Shares fixtureRepo + eventRepo with the rest of the worker.
	// Now clock left nil → real wall clock in prod (per the
	// injectable-clock discipline for scenario testing).
	monitorActs := &monitoractivity.Activities{
		APIFootball:         afClient,
		FixtureRepo:         fixtureRepo,
		EventRepo:           eventRepo,
		Spawner:             spawner,
		ActivationWindow:    deps.Cfg.Workflows.ActivationWindow,
		TerminalGracePeriod: deps.Cfg.Workflows.TerminalGracePeriod,
		FleetEnabled:        deps.Cfg.FirefoxFleet.Enabled,
	}

	// #160 — per-event Firefox fleet. Constructed only when enabled;
	// disabled → nil Fleet → Provision/ReleaseFirefox no-op and the
	// workflows' FleetEnabled guards keep the path dark. Requires the
	// Docker socket mounted into the worker (compose).
	var firefoxFleet *firefoxfleet.Fleet
	if deps.Cfg.FirefoxFleet.Enabled {
		firefoxFleet, err = firefoxfleet.New(deps.Cfg.FirefoxFleet)
		if err != nil {
			return fmt.Errorf("firefox fleet: %w", err)
		}
		deps.RegisterCloser("firefox-fleet", func(_ context.Context) error {
			return firefoxFleet.Close()
		})
	}
	// LiveEvents backs ReapOrphanedFirefox — the StagingPoll periodic sweep
	// diffs the labeled containers against this live-event set. eventRepo is
	// non-nil regardless of FleetEnabled; the activity's own nil-Fleet guard
	// keeps the reaper dark when the fleet is off.
	fleetActs := &fleetactivity.Activities{Fleet: firefoxFleet, LiveEvents: eventRepo}

	// N3 — live-feed NATS producer + its publish-activity boundary. Same
	// nc as the live-feed transport; source stamps dev/prod onto the envelope.
	natsPub, err := eventinfra.NewPublisher(nc, deps.Cfg.Event.Environment)
	if err != nil {
		return fmt.Errorf("nats publisher: %w", err)
	}
	livefeedActs := &livefeedactivity.Activities{Pub: natsPub}
	twitterMaintenanceActs := &twittermaintenanceactivity.Activities{Twitter: twitterClient}

	w.RegisterWorkflow(ffwf.ActivePollWorkflow)
	w.RegisterWorkflow(ffwf.StagingPollWorkflow)
	w.RegisterWorkflow(ffwf.TwitterMaintenanceWorkflow)
	w.RegisterWorkflow(ffwf.EventWorkflow)
	w.RegisterWorkflow(ffwf.VideoWorkflow)
	w.RegisterActivity(monitorActs)
	w.RegisterActivity(discoveryActs)
	w.RegisterActivity(videoActs)
	w.RegisterActivity(visionActs)
	w.RegisterActivity(persistActs)
	w.RegisterActivity(fleetActs)
	w.RegisterActivity(livefeedActs)
	w.RegisterActivity(twitterMaintenanceActs)

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

	// Register the daily IngestWorkflow schedule.
	// Idempotent: subsequent worker restarts hit ErrScheduleAlreadyRunning
	// and treat it as success. Manual updates via `temporal schedule
	// update` are safe; this code will not overwrite them.
	if err := ensureIngestSchedule(ctx, tempClient, deps); err != nil {
		return err
	}
	if err := ensureTwitterMaintenanceSchedule(ctx, tempClient, deps); err != nil {
		return err
	}

	// Register the two poll workflow schedules. Both are
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
}

// ensureTwitterMaintenanceSchedule registers the fixture-independent auth and
// live-search DOM canary. The static fallback browser owns this traffic; the
// per-event fleet remains zero-warm.
func ensureTwitterMaintenanceSchedule(
	ctx context.Context,
	tempClient *temporal.Client,
	deps *bootstrap.Deps,
) error {
	const scheduleID = "twitter-maintenance-scheduled"
	cronExpr := deps.Cfg.Workflows.TwitterMaintenanceCron

	_, err := tempClient.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID: scheduleID,
		Spec: client.ScheduleSpec{
			CronExpressions: []string{cronExpr},
		},
		Action: &client.ScheduleWorkflowAction{
			ID:        "twitter-maintenance-scheduled",
			Workflow:  ffwf.TwitterMaintenanceWorkflow,
			TaskQueue: tempClient.TaskQueue(),
			Args:      []any{ffwf.TwitterMaintenanceWorkflowInput{}},
		},
		Overlap: enums.SCHEDULE_OVERLAP_POLICY_SKIP,
	})
	if err != nil {
		if errors.Is(err, sdktemporal.ErrScheduleAlreadyRunning) {
			deps.Log.Emit(ctx, logging.LevelInfo,
				vocabulary.ModuleInfraTemporal, vocabulary.ActionTemporalScheduleAlreadyExists,
				"TwitterMaintenanceWorkflow schedule already registered",
				logging.String("schedule_id", scheduleID),
			)
			return nil
		}
		deps.Log.Emit(ctx, logging.LevelError,
			vocabulary.ModuleInfraTemporal, vocabulary.ActionTemporalScheduleFailed,
			"failed to create TwitterMaintenanceWorkflow schedule",
			logging.String("schedule_id", scheduleID),
			logging.Err(err),
		)
		return fmt.Errorf("create twitter maintenance schedule: %w", err)
	}
	deps.Log.Emit(ctx, logging.LevelInfo,
		vocabulary.ModuleInfraTemporal, vocabulary.ActionTemporalScheduleCreated,
		"TwitterMaintenanceWorkflow schedule registered",
		logging.String("schedule_id", scheduleID),
		logging.String("cron", cronExpr),
	)
	return nil
}

// ensureIngestSchedule registers the daily IngestWorkflow Temporal
// Schedule if it doesn't exist. Empty input means the workflow
// self-configures its anchor + defaults (see internal/workflow/ingest.go
// IngestWorkflowInput docstring). The shared history policy is read by an
// activity at execution time rather than frozen into this create-only action.
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
				FetchFuture: true, // scheduled runs get the full future-days window
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
// or runs `temporal schedule update`. FF-009 tracks startup reconciliation.
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
