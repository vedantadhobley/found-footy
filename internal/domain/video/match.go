// match.go — offset-tolerant sliding-window perceptual match. Two clips of
// the same goal often start at slightly different times (and sample at
// non-aligned sub-interval offsets), so we slide one dHash sequence against
// the other and look for a long aligned window of matching frames.
//
// The window is GAP-TOLERANT: it allows up to maxGaps mismatched frames, so a
// single bad frame (a compression artifact, or a fast-motion frame where the
// sub-interval temporal jitter spikes) doesn't shatter a real multi-second
// match into two short runs. Validated 2026-07-28 on real footage under
// scale / compression / watermark / temporal-offset transforms (see
// decisions.md): true matches produce windows of 50+ frames, different
// footage tops out at maxGaps — a huge separation.
package video

import "math/bits"

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
	return longestToleratedWindow(a, b, maxHamming, maxGaps) >= minRun
}

// longestToleratedWindow returns the longest aligned window, across all integer
// offsets, that contains at most maxGaps mismatched frames. A two-pointer scan
// per offset keeps it O((len(a)+len(b))·len(a)).
func longestToleratedWindow(a, b []uint64, maxHamming, maxGaps int) int {
	best := 0
	for d := -(len(a) - 1); d <= len(b)-1; d++ {
		// match/miss sequence for this offset's aligned overlap
		var m []bool
		for i := 0; i < len(a); i++ {
			j := i + d
			if j < 0 || j >= len(b) {
				continue
			}
			m = append(m, Hamming(a[i], b[j]) <= maxHamming)
		}
		// longest window with <= maxGaps misses
		lo, misses := 0, 0
		for hi := 0; hi < len(m); hi++ {
			if !m[hi] {
				misses++
			}
			for misses > maxGaps {
				if !m[lo] {
					misses--
				}
				lo++
			}
			if w := hi - lo + 1; w > best {
				best = w
			}
		}
	}
	return best
}
