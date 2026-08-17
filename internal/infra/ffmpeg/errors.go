// errors.go — the typed error taxonomy for ffmpeg/ffprobe operations.
// Activities errors.Is against these to classify a failure (environment
// vs input vs transient) and pick the right Temporal retry behavior, and
// the video pipeline uses Retryable to decide non-retryable vs retryable.
package ffmpeg

import "errors"

var (
	// ErrBinaryNotFound — ffmpeg/ffprobe missing from PATH. Environment
	// problem; permanent (retrying won't install it).
	ErrBinaryNotFound = errors.New("ffmpeg: binary not found in PATH")

	// ErrProbeFailed — ffprobe ran but produced no usable metadata.
	ErrProbeFailed = errors.New("ffmpeg: probe failed")

	// ErrExtractionFailed — a generic non-zero ffmpeg exit not matched by a
	// more specific class. Treated as permanent for this input by default.
	ErrExtractionFailed = errors.New("ffmpeg: extraction failed")

	// ErrExtractionTimeout — the process didn't finish before the ctx/timeout
	// (SIGKILLed). Transient; retryable.
	ErrExtractionTimeout = errors.New("ffmpeg: extraction timeout")

	// ErrInputNotFound — the input path doesn't exist. Permanent.
	ErrInputNotFound = errors.New("ffmpeg: input file not found")

	// ErrInputCorrupted — ffmpeg couldn't parse the input (truncated
	// download, missing moov atom, bad NAL). Permanent for this input.
	ErrInputCorrupted = errors.New("ffmpeg: input file corrupted/unreadable")

	// ErrOutputWriteFailed — writing output failed (disk full). Transient.
	ErrOutputWriteFailed = errors.New("ffmpeg: output write failed (disk full?)")

	// ErrConcurrencyExhausted — no semaphore slot became free before the ctx
	// deadline. Transient; the caller may back off + retry.
	ErrConcurrencyExhausted = errors.New("ffmpeg: concurrency slot unavailable before ctx deadline")
)

// Retryable reports whether err is a transient ffmpeg failure worth a
// Temporal retry. Environment + input problems are permanent and must not
// be retried (they'll fail identically).
func Retryable(err error) bool {
	switch {
	case errors.Is(err, ErrExtractionTimeout),
		errors.Is(err, ErrOutputWriteFailed),
		errors.Is(err, ErrConcurrencyExhausted):
		return true
	default:
		return false
	}
}
