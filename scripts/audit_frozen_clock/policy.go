// Offline frozen-clock controls exclude unmeasured minutes without changing production evaluation.
package main

import (
	"regexp"
	"strconv"
	"strings"

	vision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

var boundaryClock = regexp.MustCompile(`^(\d{1,3}):00$`)

// frozenBoundary recognizes supported, seconds-visible boundary shapes, not actual image motion.
// Existing incorrectly unpinned running clocks are deliberately outside this experiment.
func frozenBoundary(f vision.FrameObservation, r vision.ClockReading) (int, bool) {
	if f.Clock == nil || !r.Stoppage || r.ExactMinute || r.Ambiguous {
		return 0, false
	}
	m := boundaryClock.FindStringSubmatch(strings.TrimSpace(*f.Clock))
	if m == nil {
		return 0, false
	}
	displayed, _ := strconv.Atoi(m[1])
	switch r.Period {
	case vision.PeriodFirstHalf:
		return 45, displayed == 45
	case vision.PeriodSecondHalf:
		return 90, displayed == 90 || (displayed == 45 && f.Period != nil && *f.Period == r.Period)
	case vision.PeriodExtraFirst:
		return 105, displayed == 105 || (displayed == 15 && f.Period != nil && *f.Period == r.Period)
	case vision.PeriodExtraSecond:
		return 120, displayed == 120 || ((displayed == 15 || displayed == 30) && f.Period != nil && *f.Period == r.Period)
	default:
		return 0, false
	}
}

// evaluateControl removes only same-period frozen candidates from minute evidence.
// Content votes and all other parsing/fallbacks remain production code. The naive
// control omits the boundary lower bound to expose why blanket soft acceptance is unsafe.
func evaluateControl(frames []vision.FrameObservation, exp vision.Expected, tolerance int, bounded bool) (vision.Evaluation, []int) {
	before := vision.Evaluate(frames, exp, tolerance)
	filtered := append([]vision.FrameObservation(nil), frames...)
	var excluded []int
	contradiction := false
	for _, reading := range before.ClockReadings {
		boundary, ok := frozenBoundary(frames[reading.FrameIndex], reading)
		if !ok || reading.Period.String() != before.ExpectedPeriod {
			continue // Preserve current wrong-period and ET ambiguity behavior.
		}
		excluded = append(excluded, reading.FrameIndex)
		filtered[reading.FrameIndex].Clock = nil
		contradiction = contradiction || before.ExpectedMinute+tolerance < boundary
	}
	if len(excluded) == 0 {
		return before, nil
	}
	after := vision.Evaluate(filtered, exp, tolerance)
	// An independently usable matching frame still suffices. This experiment
	// adds neither unanimity nor a clock-progression requirement.
	if after.Outcome == vision.OutcomeVerified {
		return after, excluded
	}
	if bounded && contradiction {
		after.Outcome = vision.OutcomeRejected
		after.MatchedMinute = nil
		after.Reason = "frozen period boundary is later than expected window"
	} else if after.Outcome == vision.OutcomeUnverified && len(after.ClockReadings) == 0 {
		after.Reason = "frozen boundary with no elapsed timer — minute unverified"
	}
	return after, excluded
}
