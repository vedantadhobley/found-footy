// Package config parses environment variables into typed structs via
// caarlos0/env. See §9 adapter Config blocks in docs/design/rebuild-plan.md.
//
// The pattern:
//
//   - Each adapter package owns its own Config struct with env tags.
//   - This package composes them into a single top-level Config the
//     binaries call LoadFor() on at startup.
//   - Config composes one focused struct per adapter or runtime boundary.
package config

import (
	"fmt"
	"os"
	"reflect"
	"sort"

	"github.com/caarlos0/env/v11"
)

// Binary identifies one deployable process and therefore the environment
// sections it is allowed to parse and validate.
type Binary string

const (
	BinaryWorker  Binary = "worker"
	BinaryAPI     Binary = "api"
	BinaryTwitter Binary = "twitter"
)

var binarySections = map[Binary][]string{
	BinaryWorker: {
		"Observability", "Postgres", "NATS", "S3", "Temporal", "LLM",
		"APIFootball", "Syndication", "Twitter", "FirefoxFleet", "FFmpeg",
		"Workflows", "Discovery", "Dedup", "Video", "Vision", "Event",
	},
	BinaryAPI:     {"Observability", "Postgres", "S3", "API"},
	BinaryTwitter: {"TwitterService"},
}

// Config is the top-level configuration composed from per-adapter
// sub-configs. Populated by LoadFor(); binaries pass the relevant slice
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

	// Browser-automation service process settings. This is distinct from
	// Twitter above, which configures the worker's HTTP client.
	TwitterService TwitterServiceConfig

	// Eventing/producer layer: the NATS envelope `source` identity
	// (found-footy-dev / -prod), stamped on every live-feed message.
	// See decisions.md 2026-08-14.
	Event EventConfig
}

// LoadFor parses only the environment sections consumed by binary, then
// validates their semantic and cross-field invariants. An invalid variable for
// another binary cannot prevent this process from starting.
//
// Callers treat every returned error as fatal: invalid configuration is
// rejected before listeners, external connections, or browser processes start.
func LoadFor(binary Binary) (*Config, error) {
	variables, err := variablesFor(binary)
	if err != nil {
		return nil, err
	}
	environment := make(map[string]string, len(variables))
	for _, name := range variables {
		if value, ok := os.LookupEnv(name); ok {
			environment[name] = value
		}
	}

	var cfg Config
	if err := env.ParseWithOptions(&cfg, env.Options{Environment: environment}); err != nil {
		return nil, fmt.Errorf("config: parse %s env: %w", binary, err)
	}
	if err := cfg.ValidateFor(binary); err != nil {
		return nil, fmt.Errorf("config: validate %s: %w", binary, err)
	}
	return &cfg, nil
}

// variablesFor returns the sorted environment-variable contract for binary.
// Reflection keeps this list derived from the same struct tags envconfig uses.
func variablesFor(binary Binary) ([]string, error) {
	sections, ok := binarySections[binary]
	if !ok {
		return nil, fmt.Errorf("config: unknown binary %q", binary)
	}

	typ := reflect.TypeOf(Config{})
	set := make(map[string]struct{})
	for _, section := range sections {
		field, ok := typ.FieldByName(section)
		if !ok {
			return nil, fmt.Errorf("config: binary %s references unknown section %s", binary, section)
		}
		collectVariables(field.Type, set)
	}
	variables := make([]string, 0, len(set))
	for name := range set {
		variables = append(variables, name)
	}
	sort.Strings(variables)
	return variables, nil
}

func collectVariables(typ reflect.Type, variables map[string]struct{}) {
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if name := field.Tag.Get("env"); name != "" {
			variables[name] = struct{}{}
			continue
		}
		if field.Type.Kind() == reflect.Struct {
			collectVariables(field.Type, variables)
		}
	}
}
