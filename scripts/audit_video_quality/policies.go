// policies.go — Deterministic keeper-policy candidates evaluated by FF-081.
package main

import "math"

// strictWinner selects a deterministic exact lexicographic maximum. It removes
// hysteresis entirely and is intentionally only an audit comparison policy.
func strictWinner(assets []asset) asset {
	best := assets[0]
	for _, challenger := range assets[1:] {
		if strictBetter(challenger, best) {
			best = challenger
		}
	}
	return best
}

// strictBetter compares capped duration, known density, resolution, file size,
// then stable asset identity.
func strictBetter(left, right asset) bool {
	leftDuration, rightDuration := cappedDuration(left), cappedDuration(right)
	if leftDuration != rightDuration {
		return leftDuration > rightDuration
	}
	leftDensity, rightDensity := left.bitsPerPixel(), right.bitsPerPixel()
	if leftDensity != rightDensity {
		return leftDensity > rightDensity
	}
	leftPixels, rightPixels := left.width*left.height, right.width*right.height
	if leftPixels != rightPixels {
		return leftPixels > rightPixels
	}
	if left.fileSizeBytes != right.fileSizeBytes {
		return left.fileSizeBytes > right.fileSizeBytes
	}
	return left.id < right.id
}

// bucketWinner selects a deterministic logarithmic-tier maximum that
// approximates the current 15% duration and 10% density bands without a
// pair-relative cycle.
func bucketWinner(assets []asset) asset {
	best := assets[0]
	for _, challenger := range assets[1:] {
		if bucketBetter(challenger, best) {
			best = challenger
		}
	}
	return best
}

// bucketBetter compares fixed logarithmic duration/density tiers, resolution,
// exact metadata, and identity.
func bucketBetter(left, right asset) bool {
	leftDurationTier := logarithmicTier(float64(cappedDuration(left)), 1.15)
	rightDurationTier := logarithmicTier(float64(cappedDuration(right)), 1.15)
	if leftDurationTier != rightDurationTier {
		return leftDurationTier > rightDurationTier
	}
	leftDensity, rightDensity := left.bitsPerPixel(), right.bitsPerPixel()
	leftDensityTier := logarithmicTier(leftDensity, 1.10)
	rightDensityTier := logarithmicTier(rightDensity, 1.10)
	if leftDensityTier != rightDensityTier {
		return leftDensityTier > rightDensityTier
	}
	leftPixels, rightPixels := left.width*left.height, right.width*right.height
	if leftPixels != rightPixels {
		return leftPixels > rightPixels
	}
	if cappedDuration(left) != cappedDuration(right) {
		return cappedDuration(left) > cappedDuration(right)
	}
	if leftDensity != rightDensity {
		return leftDensity > rightDensity
	}
	if left.fileSizeBytes != right.fileSizeBytes {
		return left.fileSizeBytes > right.fileSizeBytes
	}
	return left.id < right.id
}

// anchoredBandWinner makes the current relative bands component-global: retain
// clips within 15% of maximum duration, then within 10% of maximum known
// density, then choose resolution and strict metadata. It is deterministic for
// a complete component but not an associative online comparator.
func anchoredBandWinner(assets []asset) asset {
	maxDuration := 0
	for _, item := range assets {
		if duration := cappedDuration(item); duration > maxDuration {
			maxDuration = duration
		}
	}
	durationEligible := make([]asset, 0, len(assets))
	for _, item := range assets {
		if float64(cappedDuration(item)) >= float64(maxDuration)*(1-0.15) {
			durationEligible = append(durationEligible, item)
		}
	}
	maxDensity := 0.0
	for _, item := range durationEligible {
		if density := item.bitsPerPixel(); density > maxDensity {
			maxDensity = density
		}
	}
	densityEligible := make([]asset, 0, len(durationEligible))
	for _, item := range durationEligible {
		density := item.bitsPerPixel()
		if maxDensity == 0 || (density > 0 && density*1.10 > maxDensity) {
			densityEligible = append(densityEligible, item)
		}
	}
	best := densityEligible[0]
	for _, challenger := range densityEligible[1:] {
		leftPixels := challenger.width * challenger.height
		rightPixels := best.width * best.height
		if leftPixels > rightPixels || (leftPixels == rightPixels && strictBetter(challenger, best)) {
			best = challenger
		}
	}
	return best
}

// logarithmicTier maps a positive value to one fixed multiplicative bucket.
func logarithmicTier(value, base float64) int {
	if value <= 0 {
		return -1 << 30
	}
	return int(math.Floor(math.Log(value) / math.Log(base)))
}

// cappedDuration reproduces the current 60-second completeness cap.
func cappedDuration(item asset) int {
	if item.durationMS > durationCapMS {
		return durationCapMS
	}
	return item.durationMS
}
