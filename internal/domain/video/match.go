// match.go — offset-tolerant sliding-window perceptual match. Two clips of
// the same goal often start at different times (one 5 s pre-goal, another
// 2 s pre-goal), so a naive frame-by-frame compare misses them. We slide
// one dHash sequence against the other and look for a run of at least
// minConsecutive frames that agree within maxHamming bits.
//
// Ported from archive/src/utils/dedup_match.py (_dense_hashes_match), but
// simplified: because our ffmpeg fps sampling yields UNIFORM frame spacing
// (index i == time i*interval), Python's O(N⁴) timestamp-tolerance search
// collapses to a clean integer-offset slide — behaviour-preserving and far
// cheaper.
package video

import "math/bits"

// Hamming returns the bit-difference (0..64) between two dHashes.
func Hamming(a, b uint64) int { return bits.OnesCount64(a ^ b) }

// Match reports whether frame-hash sequences a and b are the same clip: at
// some integer frame offset, at least minConsecutive consecutive frames
// agree within maxHamming bits. Both sequences must be uniformly sampled at
// the same interval. Empty / too-short sequences never match.
func Match(a, b []uint64, maxHamming, minConsecutive int) bool {
	if minConsecutive <= 0 {
		minConsecutive = 1
	}
	if len(a) < minConsecutive || len(b) < minConsecutive {
		return false
	}
	// Offset d aligns a[i] with b[i+d]; slide across every overlap. A frame
	// with no counterpart (out of overlap) or a bit-difference over the
	// threshold breaks the consecutive run.
	for d := -(len(a) - 1); d <= len(b)-1; d++ {
		run := 0
		for i := 0; i < len(a); i++ {
			j := i + d
			if j < 0 || j >= len(b) {
				run = 0
				continue
			}
			if Hamming(a[i], b[j]) <= maxHamming {
				run++
				if run >= minConsecutive {
					return true
				}
			} else {
				run = 0
			}
		}
	}
	return false
}
