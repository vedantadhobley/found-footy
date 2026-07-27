// filter.go — the pre-hashing hard-filter: a pure metadata gate that
// rejects a candidate before any (expensive) frame extraction / hashing /
// vision. Rejecting here is a normal OUTCOME, not an error. Thresholds come
// from config.HardFilterConfig (mapped to FilterThresholds by the activity
// so this stays free of infra/config imports). Evaluation short-circuits on
// the first failure — duration first (most definitive), then aspect, then
// framerate, then short-edge — per video-dedup.md.
package video

import "fmt"

// FilterThresholds is the hard-filter's tunable bounds. The activity maps
// config.HardFilterConfig onto this so the domain stays import-clean.
type FilterThresholds struct {
	MinDurationSecs float64
	MaxDurationSecs float64
	MinAspectRatio  float64
	MaxAspectRatio  float64
	MinShortEdge    int
	MinFrameRate    float64
}

// HardFilter checks a candidate's probed metadata against the thresholds.
// Returns ("", true) if it passes, or (reason, false) on the first failure.
// The reason is a stable, greppable slug (e.g. "aspect_too_wide_1.817") for
// the candidate outcome record.
func HardFilter(width, height int, durationSecs, frameRate float64, t FilterThresholds) (reason string, ok bool) {
	if width <= 0 || height <= 0 {
		return "invalid_dimensions", false
	}
	// 1. Duration — most definitive. Sub-min = thumbnail/loop, over-max =
	//    compilation reel or half-footage.
	if durationSecs < t.MinDurationSecs {
		return fmt.Sprintf("duration_too_short_%.1fs", durationSecs), false
	}
	if durationSecs > t.MaxDurationSecs {
		return fmt.Sprintf("duration_too_long_%.1fs", durationSecs), false
	}
	// 2. Aspect — rejects portrait/mobile + the social remix layer, and
	//    keeps framing consistent so dHash dedup stays reliable.
	aspect := float64(width) / float64(height)
	if aspect < t.MinAspectRatio {
		return fmt.Sprintf("aspect_too_narrow_%.3f", aspect), false
	}
	if aspect > t.MaxAspectRatio {
		return fmt.Sprintf("aspect_too_wide_%.3f", aspect), false
	}
	// 3. Framerate — filters unusual encodes (GIF re-encodes, low-fps
	//    upscales); broadcast is 24/25/30/50/60.
	if frameRate < t.MinFrameRate {
		return fmt.Sprintf("framerate_too_low_%.1f", frameRate), false
	}
	// 4. Short edge — last; letterbox semantics are fuzzier, earlier
	//    filters catch more definitive garbage first.
	shortEdge := height
	if width < height {
		shortEdge = width
	}
	if shortEdge < t.MinShortEdge {
		return fmt.Sprintf("short_edge_too_small_%d", shortEdge), false
	}
	return "", true
}
