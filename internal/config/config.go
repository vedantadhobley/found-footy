// Package config parses environment variables into typed structs via
// caarlos0/env. See §9 adapter Config blocks in docs/design/rebuild-plan.md.
//
// The pattern:
//
//   - Each adapter package owns its own Config struct with env tags.
//   - This package composes them into a single top-level Config the
//     binaries call Load() on at startup.
//   - Sub-configs are added to Config as each adapter lands in Phase S.
package config

import (
	"fmt"

	"github.com/caarlos0/env/v11"
)

// Config is the top-level configuration composed from per-adapter
// sub-configs. Populated by Load(); binaries pass the relevant slice
// into each adapter's constructor.
//
// New sub-configs get added as each adapter lands in Phase S. Order
// here matches the adapter inventory in rebuild-plan.md §9.
type Config struct {
	Observability ObservabilityConfig
	Postgres      PGConfig
	NATS          NATSConfig
	S3            S3Config
	Temporal      TemporalConfig
	LLM           LLMConfig
	APIFootball   APIFootballConfig
	Wikidata      WikidataConfig
	Wikipedia     WikipediaConfig
	Syndication   SyndicationConfig
	Twitter       TwitterConfig
	FFmpeg        FFmpegConfig

	// Cross-workflow orchestration values (activation window, staging
	// poll interval, retention). Consumed by both IngestWorkflow and
	// MonitorWorkflow — kept centralized to preserve the 2×
	// coupling invariant between ActivationWindow and StagingPollInterval.
	Workflows WorkflowsConfig

	// DiscoveryWorkflow tuning (attempts, spacing, age filter, per-
	// attempt timeout). Not folded into Workflows because these are
	// Discovery-specific rather than cross-workflow orchestration.
	Discovery DiscoveryConfig

	// Perceptual video-dedup tuning (dense-frame interval + dHash match
	// thresholds). Consumed by the ffmpeg adapter (interval) + the video
	// domain's Match (hamming + consecutive). Env-tunable for retuning on
	// real clusters without a rebuild.
	Dedup DedupConfig

	// V-phase per-candidate pipeline: local scratch root, Garage staging/
	// assets prefixes, and the pre-hashing hard-filter thresholds.
	Video VideoConfig

	// V-phase clip validation: vision-model soccer/screen gate + clock
	// verification tuning. Endpoint/model come from LLM above.
	Vision VisionConfig
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
