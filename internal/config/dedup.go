// DedupConfig — env-tunable perceptual-dedup parameters. The original
// single-window policy was calibrated on synthetic transforms, then the
// tiered policy was calibrated on production v2 hashes and manually reviewed
// clips: a strict 27/30 local route plus a sustained 45/50 route. All values
// remain tunable because live match-day evidence, not a generic dHash
// convention, owns the safe frontier.
package config

// DedupConfig configures perceptual (dHash) video deduplication.
type DedupConfig struct {
	// FrameIntervalSecs is the dense-sampling interval (seconds between
	// hashed frames). 0.1 s → ~10 frames/sec; finer than 0.25 s to give the
	// sliding window better sub-interval temporal alignment.
	FrameIntervalSecs float64 `env:"DEDUP_FRAME_INTERVAL_SECS" envDefault:"0.1"`

	// MaxHamming is the primary route's max per-frame bit-difference (of 64).
	// Production v2 calibration sets 12: 27 of 30 aligned frames must pass.
	MaxHamming int `env:"DEDUP_MAX_HAMMING" envDefault:"12"`

	// MinRunFrames is both the primary matching-window length and minimum
	// readable-hash admission floor. 30 @ 0.1 s = three seconds.
	MinRunFrames int `env:"DEDUP_MIN_RUN_FRAMES" envDefault:"30"`

	// MaxGapFrames is how many mismatched frames the primary window tolerates,
	// so isolated compression or temporal-alignment noise does not shatter a
	// real run.
	MaxGapFrames int `env:"DEDUP_MAX_GAP_FRAMES" envDefault:"3"`

	// LongMaxHamming is the sustained route's per-frame threshold. Its looser
	// value is allowed only across the longer evidence window below.
	LongMaxHamming int `env:"DEDUP_LONG_MAX_HAMMING" envDefault:"16"`

	// LongMinRunFrames requires five seconds at the default sample interval.
	// It does not raise hash admission: 30–49-frame clips retain only the
	// primary route instead of becoming content rejections.
	LongMinRunFrames int `env:"DEDUP_LONG_MIN_RUN_FRAMES" envDefault:"50"`

	// LongMaxGapFrames yields the calibrated 45-of-50 sustained route.
	LongMaxGapFrames int `env:"DEDUP_LONG_MAX_GAP_FRAMES" envDefault:"5"`
}
