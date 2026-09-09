// measure.go — Experimental periodic-repeat evidence over native-cadence frame differences.
package main

import (
	"math"
	"slices"
)

const (
	frameWidth  = 320
	frameHeight = 180
	frameBytes  = frameWidth * frameHeight
	// These are fixed experiment parameters, not accepted quality thresholds.
	motionFloor     = 0.6
	weakStrongRatio = 0.20
	minimumCycles   = 12
	cycleAgreement  = 0.80
)

type classification string

const (
	periodicRepeats classification = "periodic_repeat_evidence"
	noPattern       classification = "no_repeat_pattern_detected"
	lowMotion       classification = "inconclusive_low_motion"
	irregularTiming classification = "inconclusive_timing"
	shortWindow     classification = "inconclusive_short_window"
)

// transition retains only bounded scalar evidence, never source pixels.
type transition struct {
	seconds, delta float64
	full, center   float64
}

// window reports a repeat pattern, not an inferred original source frame rate.
type window struct {
	StartSeconds   float64        `json:"start_seconds"`
	EndSeconds     float64        `json:"end_seconds"`
	Transitions    int            `json:"transitions"`
	Classification classification `json:"classification"`
	CenterMedian   float64        `json:"center_mad_median"`
	CenterP90      float64        `json:"center_mad_p90"`
	FullMedian     float64        `json:"full_mad_median"`
	RepeatFactor   int            `json:"repeat_factor,omitempty"`
	Agreement      float64        `json:"cycle_agreement,omitempty"`
}

// frameDifference excludes common edge overlays from the center measurement.
// Both measurements remain visible because that crop can also omit real action.
func frameDifference(previous, current []byte) (full, center float64) {
	var wholeSum, centerSum, centerPixels int
	for y := 0; y < frameHeight; y++ {
		for x := 0; x < frameWidth; x++ {
			index := y*frameWidth + x
			difference := int(current[index]) - int(previous[index])
			if difference < 0 {
				difference = -difference
			}
			wholeSum += difference
			if x >= 32 && x < 288 && y >= 36 && y < 153 {
				centerSum += difference
				centerPixels++
			}
		}
	}
	return float64(wholeSum) / frameBytes, float64(centerSum) / float64(centerPixels)
}

// analyze uses roughly two-second, non-overlapping windows. Short tails are
// explicit rather than silently counted as evidence of good or bad cadence.
func analyze(samples []transition, reportedFPS float64) []window {
	windowFrames := max(1, int(math.Round(2*reportedFPS)))
	result := make([]window, 0, len(samples)/windowFrames+1)
	for start := 0; start < len(samples); start += windowFrames {
		result = append(result, measureWindow(samples[start:min(start+windowFrames, len(samples))], reportedFPS))
	}
	return result
}

// measureWindow rejects timing and motion ambiguity before looking for repeat factors.
func measureWindow(samples []transition, reportedFPS float64) window {
	result := window{Transitions: len(samples), Classification: shortWindow}
	if len(samples) == 0 {
		return result
	}
	result.StartSeconds = samples[0].seconds - samples[0].delta
	result.EndSeconds = samples[len(samples)-1].seconds
	center, full := make([]float64, len(samples)), make([]float64, len(samples))
	for i, sample := range samples {
		center[i], full[i] = sample.center, sample.full
		if sample.delta < 0.5/reportedFPS || sample.delta > 1.5/reportedFPS {
			result.Classification = irregularTiming
		}
	}
	result.CenterMedian, result.CenterP90 = quantile(center, .5), quantile(center, .9)
	result.FullMedian = quantile(full, .5)
	if result.Classification == irregularTiming || len(samples) < 48 {
		return result
	}
	if result.CenterP90 < motionFloor {
		result.Classification = lowMotion
		return result
	}
	result.Classification = noPattern
	// Smallest supported period wins; no rule treats its absence as native FPS.
	for factor := 2; factor <= 6; factor++ {
		if agreement := repeatAgreement(center, factor); agreement >= cycleAgreement {
			result.Classification, result.RepeatFactor, result.Agreement = periodicRepeats, factor, agreement
			break
		}
	}
	return result
}

// repeatAgreement requires one strong change and weak intervening changes in
// the same phase across at least twelve cycles. Lossy noise need not be zero.
func repeatAgreement(changes []float64, factor int) float64 {
	cycles := len(changes) / factor
	if cycles < minimumCycles {
		return 0
	}
	best := 0.0
	for phase := 0; phase < factor; phase++ {
		accepted := 0
		for cycle := 0; cycle < cycles; cycle++ {
			strong := changes[cycle*factor+phase]
			weak := 0.0
			for offset := 0; offset < factor; offset++ {
				if offset != phase {
					weak = max(weak, changes[cycle*factor+offset])
				}
			}
			if strong >= motionFloor && weak <= strong*weakStrongRatio {
				accepted++
			}
		}
		best = max(best, float64(accepted)/float64(cycles))
	}
	return best
}

// quantile copies before sorting so chronology survives the summary calculation.
func quantile(values []float64, fraction float64) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := slices.Clone(values)
	slices.Sort(ordered)
	return ordered[min(len(ordered)-1, int(float64(len(ordered))*fraction))]
}
