// DedupConfig — env-tunable perceptual-dedup parameters. Defaults set
// 2026-07-28 from an empirical bake-off on real footage (see decisions.md):
// dHash + offset-tolerant sliding window, 0.1 s sampling, a 30-frame (3 s)
// matching window that tolerates up to 3 mismatched frames, per-frame hamming
// ≤ 10. All env-tunable because the *final* calibration happens on live
// match-day data — these are validated-safe starting points, not proven
// optima.
package config

// DedupConfig configures perceptual (dHash) video deduplication.
type DedupConfig struct {
	// FrameIntervalSecs is the dense-sampling interval (seconds between
	// hashed frames). 0.1 s → ~10 frames/sec; finer than 0.25 s to give the
	// sliding window better sub-interval temporal alignment.
	FrameIntervalSecs float64 `env:"DEDUP_FRAME_INTERVAL_SECS" envDefault:"0.1"`

	// MaxHamming is the max per-frame bit-difference (of 64) for two frames
	// to count as the same frame. True matches sit at ≤10, different footage
	// at ≥23 — a wide gap.
	MaxHamming int `env:"DEDUP_MAX_HAMMING" envDefault:"10"`

	// MinRunFrames is the required length of the matching window. 30 @ 0.1 s
	// = a 3-second match — a strong "same clip" signal that different footage
	// never reaches. A precision-favoring choice; the safe failure direction
	// (too strict → an occasional duplicate shown, not a real clip lost).
	MinRunFrames int `env:"DEDUP_MIN_RUN_FRAMES" envDefault:"30"`

	// MaxGapFrames is how many mismatched frames the window tolerates, so one
	// bad frame doesn't shatter a real run. Different footage tops out at
	// exactly this value, so it stays far below MinRunFrames.
	MaxGapFrames int `env:"DEDUP_MAX_GAP_FRAMES" envDefault:"3"`
}
