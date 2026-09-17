// Relative-clock regressions require observed period evidence instead of an API-driven offset.
package vision

import (
	"reflect"
	"testing"
)

// TestRelativeClocksRequirePeriodEvidence keeps each supported reset convention
// while rejecting the same readable clock when its offset is only hypothetical.
func TestRelativeClocksRequirePeriodEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		clock  string
		period Period
		minute int
	}{
		{"second half", "05:25", PeriodSecondHalf, 50},
		{"extra first", "05:25", PeriodExtraFirst, 95},
		{"extra second reset", "05:25", PeriodExtraSecond, 110},
		{"extra second cumulative", "20:25", PeriodExtraSecond, 110},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected := Expected{Elapsed: tc.minute + 1}
			for _, allowance := range []string{"", "+1", "+2"} {
				f := frame(true, false, tc.clock, allowance, "")
				bare := Evaluate(rep(f, 3), expected, 1)
				if bare.Outcome != OutcomeRejected || bare.MatchedMinute != nil {
					t.Fatalf("offset invented without period (allowance %q): %+v", allowance, bare)
				}
				labeled := Evaluate(rep(withPeriod(f, tc.period), 3), expected, 1)
				if labeled.Outcome != OutcomeVerified || labeled.MatchedMinute == nil || *labeled.MatchedMinute != tc.minute {
					t.Fatalf("lost explicit reset clock (allowance %q): %+v", allowance, labeled)
				}
			}
		})
	}
}

// TestYamalWrongHalfRejects retains the actual September 17 observations and
// the existing any-usable-match policy, without changing raw OCR or tolerance.
func TestYamalWrongHalfRejects(t *testing.T) {
	frames := []FrameObservation{
		frame(true, false, "44:48", "", ""),
		frame(true, false, "44:51", "", ""),
		frame(true, false, "44:53", "", ""),
	}
	before := append([]FrameObservation(nil), frames...)
	ev := Evaluate(frames, Expected{Elapsed: 89}, 1)
	if ev.Outcome != OutcomeRejected || ev.MatchedMinute != nil ||
		ev.DetectedMinute == nil || *ev.DetectedMinute != 44 || ev.ExpectedMinute != 88 {
		t.Fatalf("wrong-half admission: %+v", ev)
	}
	if !reflect.DeepEqual(before, frames) || len(ev.ClockReadings) != 3 {
		t.Fatal("raw observations or reading count changed")
	}
	for _, reading := range ev.ClockReadings {
		if reading.Minute != 44 || reading.Period != PeriodFirstHalf || reading.PeriodPinned || len(reading.AlternativeMinutes) != 0 {
			t.Fatalf("invented period evidence: %+v", reading)
		}
	}
	// An independent matching continuous clock still verifies without a label.
	frames[1] = frame(true, false, "88:50", "", "")
	ev = Evaluate(frames, Expected{Elapsed: 89}, 1)
	if ev.Outcome != OutcomeVerified || ev.MatchedMinute == nil || *ev.MatchedMinute != 88 {
		t.Fatalf("lost independent match: %+v", ev)
	}
}
