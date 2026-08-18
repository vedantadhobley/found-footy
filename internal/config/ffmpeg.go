// FFmpegConfig — env-driven settings for the ffmpeg/ffprobe subprocess
// adapter. The concurrency + per-process thread caps are the CPU governor
// on this shared host: total ffmpeg thread budget ≈ MaxProcesses ×
// ThreadsPerProc. All values are env-tunable so we retune from benchmark
// data (per-clip decode cost on real clips) without recompiling.
package config

import "time"

// FFmpegConfig configures the internal/infra/ffmpeg adapter.
type FFmpegConfig struct {
	// Binary locations. Defaults assume both are on PATH (they are in the
	// worker image — ffmpeg 5.1.x ships ffprobe alongside).
	FFmpegPath  string `env:"FFMPEG_PATH" envDefault:"ffmpeg"`
	FFprobePath string `env:"FFPROBE_PATH" envDefault:"ffprobe"`

	// Timeout ceilings a single ffmpeg/ffprobe invocation (SIGKILL on
	// expiry). The activity ctx normally governs; this backstops a runaway.
	// Applies to the fast ops (probe, single-frame extract, faststart) — kept
	// tight so a genuine hang there surfaces quickly.
	Timeout time.Duration `env:"FFMPEG_TIMEOUT" envDefault:"30s"`

	// DenseTimeout ceilings dense frame extraction (ExtractDenseFrames) — the
	// one CPU-heavy, potentially-long op: a ~90s clip at 0.1s sampling is ~900
	// frames in a single decode pass, which under concurrent match load can
	// exceed the tight Timeout above. Kept just under HashVideo's 2-min
	// StartToClose so ffmpeg trips its own clean timeout before Temporal fires
	// a blunter activity timeout (#184, decisions.md 2026-08-12).
	DenseTimeout time.Duration `env:"FFMPEG_DENSE_TIMEOUT" envDefault:"100s"`

	// MaxProcesses caps concurrent ffmpeg/ffprobe processes (semaphore).
	// Conservative default for the shared box; tune up from the benchmark.
	MaxProcesses int `env:"FFMPEG_MAX_CONCURRENT" envDefault:"4"`

	// ThreadsPerProc bounds each decode's thread fan-out (ffmpeg -threads).
	// 0 = let ffmpeg auto-pick (may grab every core). Non-zero keeps total
	// load predictable on a host we don't own outright.
	ThreadsPerProc int `env:"FFMPEG_THREADS_PER_PROC" envDefault:"2"`

	// FrameQuality is the mjpeg -q:v for single (vision) frames: 2..31,
	// lower = better. Dense hash frames use bounded lossless grayscale PNG.
	FrameQuality int `env:"FFMPEG_FRAME_QUALITY" envDefault:"3"`
}
