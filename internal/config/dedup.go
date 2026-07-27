// DedupConfig — env-tunable perceptual-dedup parameters. All three are
// empirically tuned (Python's dense:0.25 sampling, MAX_HAMMING_DISTANCE=10,
// MIN_CONSECUTIVE_MATCHES=3) and exposed as env so we retune on real
// clusters without a rebuild. FrameIntervalSecs is passed to the ffmpeg
// adapter's ExtractDenseFrames; MaxHamming + MinConsecutive drive
// domain/video.Match.
package config

// DedupConfig configures perceptual (dHash) video deduplication.
type DedupConfig struct {
	// FrameIntervalSecs is the dense-sampling interval (seconds between
	// hashed frames). Frame i of the resulting sequence is at i*interval.
	FrameIntervalSecs float64 `env:"DEDUP_FRAME_INTERVAL_SECS" envDefault:"0.25"`

	// MaxHamming is the max bit-difference (of 64) for two frames to count
	// as the same frame.
	MaxHamming int `env:"DEDUP_MAX_HAMMING" envDefault:"10"`

	// MinConsecutive is how many consecutive frames must match (at a
	// consistent offset) for two clips to be judged the same video.
	MinConsecutive int `env:"DEDUP_MIN_CONSECUTIVE" envDefault:"3"`
}
