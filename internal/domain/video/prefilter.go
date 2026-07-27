// prefilter.go — the pre-download subset of the hard-filter: aspect +
// duration, the two dimensions the syndication tweet-result exposes BEFORE
// downloading. Lets DownloadAndStage reject portrait / compilation
// candidates with zero bytes fetched. Framerate + short-edge still need the
// real file, so a PreFilter pass is provisional — HardFilter re-checks all
// four post-download.
package video

import "fmt"

// PreFilter checks the two variant-invariant dimensions (aspect + duration)
// available pre-download. Returns ("", true) to proceed to download, or
// (reason, false) to reject without fetching. Unknown dims (0) fall through
// to download — we can't pre-judge what we can't measure.
func PreFilter(width, height int, durationSecs float64, t FilterThresholds) (reason string, ok bool) {
	if width <= 0 || height <= 0 {
		return "", true // unknown — download + let HardFilter decide
	}
	if durationSecs > 0 {
		if durationSecs < t.MinDurationSecs {
			return fmt.Sprintf("duration_too_short_%.1fs", durationSecs), false
		}
		if durationSecs > t.MaxDurationSecs {
			return fmt.Sprintf("duration_too_long_%.1fs", durationSecs), false
		}
	}
	aspect := float64(width) / float64(height)
	if aspect < t.MinAspectRatio {
		return fmt.Sprintf("aspect_too_narrow_%.3f", aspect), false
	}
	if aspect > t.MaxAspectRatio {
		return fmt.Sprintf("aspect_too_wide_%.3f", aspect), false
	}
	return "", true
}
