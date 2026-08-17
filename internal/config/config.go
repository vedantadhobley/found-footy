// Package config parses environment variables into typed structs via
// caarlos0/env. See §9 adapter Config blocks in docs/design/rebuild-plan.md.
//
// The pattern:
//
//   - Each adapter package owns its own Config struct with env tags.
//   - This package composes them into a single top-level Config the
//     binaries call Load() on at startup.
//   - Config composes one focused struct per adapter or runtime boundary.
package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
)

// Config is the top-level configuration composed from per-adapter
// sub-configs. Populated by Load(); binaries pass the relevant slice
// into each adapter's constructor.
//
// Add new sub-configs when a new runtime boundary needs configuration.
type Config struct {
	Observability ObservabilityConfig
	Postgres      PGConfig
	NATS          NATSConfig
	S3            S3Config
	Temporal      TemporalConfig
	LLM           LLMConfig
	APIFootball   APIFootballConfig
	Syndication   SyndicationConfig
	Twitter       TwitterConfig
	FirefoxFleet  FirefoxFleetConfig
	FFmpeg        FFmpegConfig

	// Cross-workflow orchestration values (activation window, polling
	// schedules, retention). Shared by ingest and the two poll workflows.
	Workflows WorkflowsConfig

	// EventWorkflow tuning (attempts, spacing, age filter, per-
	// attempt timeout). Not folded into Workflows because these are
	// Discovery-specific rather than cross-workflow orchestration.
	Discovery DiscoveryConfig

	// Perceptual video-dedup tuning (dense-frame interval + dHash match
	// thresholds). Consumed by the ffmpeg adapter (interval) + the video
	// domain's Match (hamming + consecutive). Env-tunable for retuning on
	// real clusters without a rebuild.
	Dedup DedupConfig

	// Per-candidate video pipeline: local scratch root, Garage staging/
	// assets prefixes, and the pre-hashing hard-filter thresholds.
	Video VideoConfig

	// Clip validation: vision-model soccer/screen gate + clock
	// verification tuning. Endpoint/model come from LLM above.
	Vision VisionConfig

	// Public read-API HTTP surface — bind address and timeouts.
	API APIConfig

	// Eventing/producer layer: the NATS envelope `source` identity
	// (found-footy-dev / -prod), stamped on every live-feed message.
	// See decisions.md 2026-08-14.
	Event EventConfig
}

// Load parses the process environment into a Config.
//
// Returns a wrapped error if any required var is missing or any parse
// fails. Callers should treat Load errors as fatal — a mis-configured
// binary should refuse to start rather than run half-initialized.
func Load() (*Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return nil, fmt.Errorf("config: parse env: %w", err)
	}
	return &cfg, nil
}
