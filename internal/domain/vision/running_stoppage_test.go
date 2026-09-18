// FF-097 regressions distinguish running stoppage clocks from the next period.
package vision

import (
	"reflect"
	"testing"
)

// TestRunningStoppageAllowance keeps the measured minute at every ending boundary.
func TestRunningStoppageAllowance(t *testing.T) {
	for _, tc := range []struct {
		clock    string
		minute   int
		period   Period
		expected Expected
	}{
		{"46:35", 46, PeriodFirstHalf, Expected{45, 2}},
		{"47:30", 47, PeriodFirstHalf, Expected{45, 2}},
		{"50:05", 50, PeriodFirstHalf, Expected{45, 5}},
		{"92:35", 92, PeriodSecondHalf, Expected{90, 3}},
		{"96:15", 96, PeriodSecondHalf, Expected{90, 7}},
		{"106:35", 106, PeriodExtraFirst, Expected{105, 2}},
		{"121:35", 121, PeriodExtraSecond, Expected{120, 2}},
		{"46", 46, PeriodFirstHalf, Expected{45, 2}},
	} {
		for _, allowance := range []string{"+1", "+4", "4", "4′", "+10"} {
			t.Run(tc.clock+"/"+allowance, func(t *testing.T) {
				f := frame(true, false, tc.clock, allowance, "")
				r, ok := parseFrameClock(0, f)
				if !ok || r.Minute != tc.minute || r.Period != tc.period || !r.PeriodPinned || !r.Stoppage || !r.ExactMinute || r.Ambiguous {
					t.Fatalf("incorrect running stoppage reading: %+v", r)
				}
				ev := Evaluate(rep(f, 3), tc.expected, 1)
				if ev.Outcome != OutcomeVerified || ev.MatchedMinute == nil || *ev.MatchedMinute != tc.minute {
					t.Fatalf("incorrect evaluation: %+v", ev)
				}
				// A spurious secondary timer must not add to an already running clock.
				f.StoppageClock = sp("04:00")
				withSecondary, _ := parseFrameClock(0, f)
				if !reflect.DeepEqual(r, withSecondary) {
					t.Fatalf("secondary changed running clock: %+v -> %+v", r, withSecondary)
				}
			})
		}
	}
}

// TestRunningStoppageGueye replays both saved in-window observation sequences.
func TestRunningStoppageGueye(t *testing.T) {
	for _, clocks := range [][]string{{"46:35", "46:37", "46:39"}, {"46:46", "47:00", "47:30"}} {
		frames := make([]FrameObservation, 0, len(clocks))
		for _, clock := range clocks {
			frames = append(frames, frame(true, false, clock, "+4", ""))
		}
		ev := Evaluate(frames, Expected{45, 2}, 1)
		if ev.Outcome != OutcomeVerified || ev.MatchedMinute == nil || *ev.MatchedMinute != 46 {
			t.Fatalf("Gueye false rejection: %+v", ev)
		}
		// Identical evidence must not verify an early-second-half event.
		if wrong := Evaluate(frames, Expected{47, 0}, 1); wrong.Outcome != OutcomeRejected {
			t.Fatalf("stoppage footage passed for the next half: %+v", wrong)
		}
	}
	for _, clock := range []string{"39:34", "48:35"} {
		if ev := Evaluate(rep(frame(true, false, clock, "+4", ""), 3), Expected{45, 2}, 1); ev.Outcome != OutcomeRejected {
			t.Fatalf("outside-window %s was rescued: %+v", clock, ev)
		}
	}
}

// TestRunningStoppageDoesNotInventEvidence preserves ordinary and pre-announcement clocks.
func TestRunningStoppageDoesNotInventEvidence(t *testing.T) {
	for _, clock := range []string{"44:59", "60:01", "70:00", "89:59"} {
		without, _ := parseFrameClock(0, frame(true, false, clock, "", ""))
		with, _ := parseFrameClock(0, frame(true, false, clock, "+4", ""))
		if !reflect.DeepEqual(without, with) {
			t.Errorf("pre-boundary or unsupported allowance changed %s: %+v -> %+v", clock, without, with)
		}
	}
	for _, allowance := range []string{"", "+0", "0", "-4", "04:00", "+4x"} {
		r, _ := parseFrameClock(0, frame(true, false, "46:35", allowance, ""))
		if r.Period != PeriodSecondHalf || r.PeriodPinned || r.Stoppage || !r.ExactMinute {
			t.Errorf("absent or invalid allowance %q manufactured evidence: %+v", allowance, r)
		}
	}
}

// TestRunningStoppagePreservesExplicitPeriods avoids overriding observed labels and compact forms.
func TestRunningStoppagePreservesExplicitPeriods(t *testing.T) {
	for _, f := range []FrameObservation{
		withPeriod(frame(true, false, "46:35", "+4", ""), PeriodSecondHalf),
		frame(true, false, "2H 46:35", "+4", ""),
	} {
		r, _ := parseFrameClock(0, f)
		if r.Period != PeriodSecondHalf || !r.PeriodPinned || r.Minute != 46 {
			t.Fatalf("allowance replaced explicit half: %+v", r)
		}
		if ev := Evaluate(rep(f, 3), Expected{45, 2}, 1); ev.Outcome != OutcomeRejected {
			t.Fatalf("explicit wrong half rescued: %+v", ev)
		}
	}
	for _, f := range []FrameObservation{
		withPeriod(frame(true, false, "60:35", "+4", ""), PeriodFirstHalf),
		withPeriod(frame(true, false, "106:35", "+4", ""), PeriodSecondHalf),
	} {
		r, _ := parseFrameClock(0, f)
		if r.Period != *f.Period || !r.ExactMinute || r.Ambiguous {
			t.Fatalf("recognition window capped explicit stoppage: %+v", r)
		}
	}
	conflict := withPeriod(frame(true, false, "45+2", "+4", ""), PeriodSecondHalf)
	if r, _ := parseFrameClock(0, conflict); !r.Ambiguous || r.Period != PeriodUnknown {
		t.Fatalf("allowance erased conflicting evidence: %+v", r)
	}
}

// TestRunningStoppageRecognitionBounds pins inference limits independently of allowance size.
func TestRunningStoppageRecognitionBounds(t *testing.T) {
	for _, tc := range []struct {
		clock    string
		inferred bool
	}{
		{"45:00", false}, {"45:35", false}, {"59:59", true}, {"60:00", false},
		{"90:00", false}, {"90:35", false}, {"104:59", true}, {"105:00", false},
		{"105:35", false}, {"119:59", true}, {"120:00", false},
		{"120:35", false}, {"134:59", true}, {"135:00", false},
	} {
		r, _, ok := parseClockField(tc.clock)
		if !ok {
			t.Fatal(tc.clock)
		}
		for _, allowance := range []string{"+1", "+20"} {
			got := withRunningStoppage(r, &allowance)
			if got.Minute != r.Minute || got.ExactMinute != r.ExactMinute || got.PeriodPinned != tc.inferred {
				t.Errorf("unexpected inference at %s %s: %+v", tc.clock, allowance, got)
			}
			if twice := withRunningStoppage(got, &allowance); !reflect.DeepEqual(got, twice) {
				t.Errorf("allowance applied twice at %s", tc.clock)
			}
		}
	}
}
