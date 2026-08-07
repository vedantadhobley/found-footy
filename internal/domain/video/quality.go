// quality.go — dedup winner-selection scoring. When a new clip perceptually
// matches one or more already-kept assets (a cluster — dHash Match isn't
// transitive, so a new clip can bridge two assets that don't match each
// other), this scores the cluster to pick the single highest-quality member.
//
// NOTE: BUILT BUT NOT WIRED (#171). The persist path's collapse() is still
// keep-first, so IsUpgrade below decides nothing in production yet.
//
// "Quality" is judged from metadata we already capture at download time, in
// this order:
//
//  1. Completeness — duration, compared RELATIVELY (a 15% band, per the Python
//     system) so "same clip, slightly trimmed" is distinguished from "one is
//     genuinely more complete." Duration is CAPPED at durationCapMS: past the
//     cap, extra length is padding, not completeness, so it earns no edge (a
//     long clip is never DROPPED here — that's the hard filter's 90s max; it
//     just stops benefiting from length).
//  2. Bits-per-pixel — encoding density. This is the deliberate improvement
//     over the Python system, which ranked on raw file_size: a blurry
//     source upscaled to 1080p has a high pixel count and a big file but few
//     bits describing each pixel. Density catches that; raw size/resolution
//     does not. See decisions.md 2026-08-05 (quality-aware dedup).
//  3. Resolution — raw pixel count, final tiebreak.
//
// No absolute "too short" gate: the pre-download hard filter already floors
// duration at 5s, so a survivor that's genuinely short simply loses the
// relative-duration comparison to any longer clip.
package video

import "math"

const (
	// durationCapMS caps the completeness benefit. A clip longer than this
	// competes as if it were exactly this long — length goes flat past the cap
	// (a 90s clip earns no edge over a 60s one), but the clip is never dropped
	// for length (the hard filter's MaxDurationSecs handles rejection).
	durationCapMS = 60_000

	// durationBand: two clips whose CAPPED durations are within this ratio are
	// treated as the same clip (just trimmed) → decide on density, not length.
	// Beyond it, the longer clip is meaningfully more complete. Python's 0.15.
	durationBand = 0.15

	// bppMargin: a challenger must beat the incumbent's bits-per-pixel by this
	// ratio to win the density tier — anti-churn on near-equal re-encodes of
	// the same shot (each supersede is real S3 + pg work).
	bppMargin = 1.10
)

// ClipQuality is the metadata dedup ranks a cluster member on. Bitrate is a
// pointer because the ffprobe pass can legitimately fail to read it.
type ClipQuality struct {
	DurationMS    int
	Bitrate       *int
	Width, Height int
}

// bitsPerPixel is encoding DENSITY: bitrate / (width·height). It separates a
// genuine HD encode from an upscaled/over-compressed one — same resolution, far
// fewer bits describing each pixel. Returns 0 when bitrate or dimensions are
// unknown, so an unknown never falsely wins the tier.
func (q ClipQuality) bitsPerPixel() float64 {
	if q.Bitrate == nil || *q.Bitrate <= 0 || q.Width <= 0 || q.Height <= 0 {
		return 0
	}
	return float64(*q.Bitrate) / float64(q.Width*q.Height)
}

func (q ClipQuality) pixels() int { return q.Width * q.Height }

// cappedDurationMS flattens length past durationCapMS.
func (q ClipQuality) cappedDurationMS() int {
	if q.DurationMS > durationCapMS {
		return durationCapMS
	}
	return q.DurationMS
}

// IsUpgrade reports whether challenger is a MEANINGFULLY better keep than
// incumbent — the guard the supersede path uses so we never churn an asset for
// a trivial gain, and the reducer cluster-winner selection uses (incumbents win
// ties → stability). Ties and marginal wins return false: keep the incumbent.
func IsUpgrade(challenger, incumbent ClipQuality) bool {
	// 1. Completeness — capped duration, compared relatively. Only a
	//    difference beyond the band counts; within it, the two are the same
	//    length and we fall through to quality.
	cd, id := float64(challenger.cappedDurationMS()), float64(incumbent.cappedDurationMS())
	if hi := math.Max(cd, id); hi > 0 && math.Abs(cd-id)/hi > durationBand {
		return cd > id
	}
	// 2. Same length → bits-per-pixel, with the anti-churn margin.
	cBPP, iBPP := challenger.bitsPerPixel(), incumbent.bitsPerPixel()
	if cBPP > 0 && iBPP > 0 {
		if cBPP >= iBPP*bppMargin {
			return true
		}
		if iBPP >= cBPP*bppMargin {
			return false
		}
		// within margin → indistinguishable on density, fall through
	}
	// 3. Density unknown or equal → raw resolution, strictly greater.
	return challenger.pixels() > incumbent.pixels()
}
