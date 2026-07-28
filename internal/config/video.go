// VideoConfig + HardFilterConfig — env-driven settings for the V-phase
// per-candidate pipeline (rung 3). VideoConfig covers the local scratch
// root + the Garage staging/assets key prefixes; HardFilterConfig is the
// metadata gate applied before any hashing. All env-tunable so thresholds
// retune on real data without a rebuild.
package config

// VideoConfig configures the internal/activity/video pipeline.
type VideoConfig struct {
	// ScratchDir is the worker-local root for a candidate's temp download.
	// Wiped per candidate. A real disk path (clips are up to ~100 MB — not
	// tmpfs).
	ScratchDir string `env:"VIDEO_SCRATCH_DIR" envDefault:"/scratch"`

	// StagingPrefix / AssetsPrefix are the Garage key prefixes. Raw bytes
	// land under staging/<fixture>/<event>/<tweet>.mp4 after download;
	// survivors get promoted to assets/<fixture>/<event>/<asset>.mp4 later.
	StagingPrefix string `env:"VIDEO_STAGING_PREFIX" envDefault:"staging"`
	AssetsPrefix  string `env:"VIDEO_ASSETS_PREFIX" envDefault:"assets"`

	// HardFilter is the metadata gate (below).
	HardFilter HardFilterConfig
}

// HardFilterConfig — the pre-hashing metadata gate. Defaults are the
// video-dedup.md tuned values, with the aspect max loosened 1.80 → 1.82
// (2026-07-27): real broadcast rips with the scorebug cropped land at
// ~1.81-1.82, and a two-goal aspect sample (Dybala/Chiesa) showed 1.80
// clipped the largest cluster of legit landscape clips. The tight band
// still rejects portrait phone-of-TV recordings + the social remix layer
// AND keeps aspect consistent so dHash dedup stays reliable.
type HardFilterConfig struct {
	MinDurationSecs float64 `env:"HARDFILTER_MIN_DURATION_SECS" envDefault:"5"`
	MaxDurationSecs float64 `env:"HARDFILTER_MAX_DURATION_SECS" envDefault:"90"`
	MinAspectRatio  float64 `env:"HARDFILTER_MIN_ASPECT" envDefault:"1.75"`
	MaxAspectRatio  float64 `env:"HARDFILTER_MAX_ASPECT" envDefault:"1.82"`
	MinShortEdge    int     `env:"HARDFILTER_MIN_SHORT_EDGE" envDefault:"600"`
	MinFrameRate    float64 `env:"HARDFILTER_MIN_FRAMERATE" envDefault:"20"`
}
