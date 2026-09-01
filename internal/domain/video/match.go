// match.go — offset-tolerant sliding-window perceptual match. Two clips of
// the same goal often start at slightly different times (and sample at
// non-aligned sub-interval offsets), so we slide one dHash sequence against
// the other and look for a long aligned window of matching frames.
//
// The window is GAP-TOLERANT: it allows up to maxGaps mismatched frames, so a
// single bad frame (a compression artifact, or a fast-motion frame where the
// sub-interval temporal jitter spikes) doesn't shatter a real multi-second
// match into two short runs. Match supplies the mechanism; the caller owns the
// calibrated evidence policy because safe thresholds depend on window length
// and production false-match boundaries.
package video

import "math/bits"

// AlignmentEvidence is the strongest aligned dHash window found for one
// matcher route. Frame indexes address the original left and right sequences;
// Frames includes tolerated gaps because those frames remain inside the
// aligned span.
type AlignmentEvidence struct {
	LeftStart  int
	RightStart int
	Frames     int
	Gaps       int
}

// Hamming returns the bit-difference (0..64) between two dHashes.
func Hamming(a, b uint64) int { return bits.OnesCount64(a ^ b) }

// Match reports whether frame-hash sequences a and b are the same clip: at
// some integer frame offset there is an aligned window of at least minRun
// frames containing at most maxGaps frames that differ by more than maxHamming
// bits. Both sequences are assumed uniformly sampled at the same interval.
func Match(a, b []uint64, maxHamming, minRun, maxGaps int) bool {
	if minRun <= 0 {
		minRun = 1
	}
	if len(a) < minRun || len(b) < minRun {
		return false
	}
	return BestAlignment(a, b, maxHamming, maxGaps).Frames >= minRun
}

// BestAlignment returns the longest aligned window, across all integer
// offsets, that contains at most maxGaps over-threshold frames. Ties prefer
// fewer gaps and then the earliest stable positions. A two-pointer scan per
// offset keeps it O((len(a)+len(b))·len(a)). Match remains the policy gate;
// this function exposes evidence without deciding whether the run is long
// enough to identify shared footage.
func BestAlignment(a, b []uint64, maxHamming, maxGaps int) AlignmentEvidence {
	if len(a) == 0 || len(b) == 0 {
		return AlignmentEvidence{}
	}
	if maxGaps < 0 {
		maxGaps = 0
	}
	best := AlignmentEvidence{}
	for d := -(len(a) - 1); d <= len(b)-1; d++ {
		leftStart := max(0, -d)
		leftEnd := min(len(a), len(b)-d)
		// longest window with <= maxGaps misses
		lo, misses := 0, 0
		for hi := 0; hi < leftEnd-leftStart; hi++ {
			leftIndex := leftStart + hi
			if Hamming(a[leftIndex], b[leftIndex+d]) > maxHamming {
				misses++
			}
			for misses > maxGaps {
				oldLeftIndex := leftStart + lo
				if Hamming(a[oldLeftIndex], b[oldLeftIndex+d]) > maxHamming {
					misses--
				}
				lo++
			}
			candidate := AlignmentEvidence{
				LeftStart:  leftStart + lo,
				RightStart: leftStart + lo + d,
				Frames:     hi - lo + 1,
				Gaps:       misses,
			}
			if strongerAlignment(candidate, best) {
				best = candidate
			}
		}
	}
	return best
}

// strongerAlignment gives diagnostic output a deterministic representative
// when repeated footage produces several equal-length tolerated windows.
func strongerAlignment(candidate, incumbent AlignmentEvidence) bool {
	if candidate.Frames != incumbent.Frames {
		return candidate.Frames > incumbent.Frames
	}
	if candidate.Gaps != incumbent.Gaps {
		return candidate.Gaps < incumbent.Gaps
	}
	if candidate.LeftStart+candidate.RightStart != incumbent.LeftStart+incumbent.RightStart {
		return candidate.LeftStart+candidate.RightStart < incumbent.LeftStart+incumbent.RightStart
	}
	if candidate.LeftStart != incumbent.LeftStart {
		return candidate.LeftStart < incumbent.LeftStart
	}
	return candidate.RightStart < incumbent.RightStart
}
