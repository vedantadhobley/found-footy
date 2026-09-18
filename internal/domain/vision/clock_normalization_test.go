// FF-094 regressions separate literal timing evidence from announced allowance.
package vision

import (
	"reflect"
	"testing"
)

// TestLiteralTimers rejects partial OCR and preserves whether seconds were supplied.
func TestLiteralTimers(t *testing.T) {
	for _, raw := range []string{"90:99", "90:60", "90:000", "x90:00", "90:00x", "70+4", "90+4garbage", "90+4:99", "999999999999999999999+4"} {
		if r, _, ok := parseClockField(raw); ok {
			t.Errorf("accepted malformed main %q: %+v", raw, r)
		}
	}
	for _, raw := range []string{"5", "+5", "5′", "05:99", "05:00x", "100:00"} {
		if _, ok := parseStoppageClockField(raw); ok {
			t.Errorf("accepted non-timer secondary %q", raw)
		}
	}
	for _, raw := range []string{"05:00", "+5:00", "x+5", "+5x"} {
		if _, ok := parseAddedField(raw); ok {
			t.Errorf("accepted non-allowance %q", raw)
		}
	}
	for _, raw := range []string{"+5", "5", "5′", "+ 5'"} {
		if n, ok := parseAddedField(raw); !ok || n != 5 {
			t.Errorf("lost literal allowance %q", raw)
		}
	}
	for _, tc := range []struct {
		raw     string
		seconds int
		known   bool
	}{
		{"90", 0, false}, {"90:00", 0, true}, {"90:37", 37, true},
	} {
		_, timer, ok := parseClockField(tc.raw)
		if !ok || timer.second != tc.seconds || timer.secondsKnown != tc.known {
			t.Errorf("lost precision for %s: %+v", tc.raw, timer)
		}
	}
	if r, _, ok := parseClockField("90+1:23"); !ok || r.Minute != 91 || !r.Stoppage || r.Period != PeriodSecondHalf {
		t.Errorf("lost compact elapsed clock: %+v", r)
	}
}

// TestAuxiliaryFieldsCannotReplaceRunningEvidence guards both production failure branches.
func TestAuxiliaryFieldsCannotReplaceRunningEvidence(t *testing.T) {
	for _, tc := range []struct {
		clock, added, secondary string
		period                  Period
		minute                  int
	}{
		{"95:15", "+5", "05:00", PeriodSecondHalf, 95},
		{"95:15", "5′", "", PeriodSecondHalf, 95},
		{"96:15", "+5", "", PeriodSecondHalf, 96}, // allowance is not a ceiling
		{"90:37", "+4", "", PeriodSecondHalf, 90},
		{"90:37", "+4", "03:30", PeriodSecondHalf, 90},
		{"90", "+5", "05:15", PeriodSecondHalf, 90}, // absent seconds are not :00
		{"70:00", "+5", "05:00", PeriodSecondHalf, 70},
		{"45:25", "+5", "05:00", PeriodSecondHalf, 45},
		{"90+4", "+5", "04:30", PeriodSecondHalf, 94}, // no double addition
		{"90:00", "05:00", "05:99", PeriodSecondHalf, 90},
	} {
		t.Run(tc.clock+tc.added+tc.secondary, func(t *testing.T) {
			f := withPeriod(frame(true, false, tc.clock, tc.added, tc.secondary), tc.period)
			r, ok := parseFrameClock(0, f)
			if !ok || r.Minute != tc.minute || !r.ExactMinute || r.Ambiguous {
				t.Fatalf("reading = %+v", r)
			}
		})
	}
}

// TestEndBoundariesComposeOnlyOnce covers all supported reset and continuous forms.
func TestEndBoundariesComposeOnlyOnce(t *testing.T) {
	for _, tc := range []struct {
		clock  string
		period Period
		end    int
	}{
		{"45:00", PeriodFirstHalf, 45}, {"90:00", PeriodSecondHalf, 90},
		{"105:00", PeriodExtraFirst, 105}, {"120:00", PeriodExtraSecond, 120},
		{"45:00", PeriodSecondHalf, 90}, {"15:00", PeriodExtraFirst, 105},
		{"15:00", PeriodExtraSecond, 120}, {"30:00", PeriodExtraSecond, 120},
	} {
		for _, allowance := range []string{"+1", "+2", "+5", "+10"} {
			t.Run(string(tc.period)+tc.clock+allowance, func(t *testing.T) {
				f := withPeriod(frame(true, false, tc.clock, allowance, "+02:10"), tc.period)
				r, ok := parseFrameClock(0, f)
				if !ok || r.Minute != tc.end+2 || !r.ExactMinute || !r.Stoppage || len(r.AlternativeMinutes) != 0 {
					t.Fatalf("secondary composition = %+v", r)
				}
				f.StoppageClock = nil
				r, ok = parseFrameClock(0, f)
				if !ok || r.Minute != tc.end || r.ExactMinute || len(r.AlternativeMinutes) != 0 {
					t.Fatalf("unpinned boundary = %+v", r)
				}
				ev := Evaluate(rep(f, 3), Expected{Elapsed: tc.end, Extra: 10}, 1)
				if ev.Outcome != OutcomeUnverified || ev.MatchedMinute != nil {
					t.Fatalf("invented elapsed minute: %+v", ev)
				}
			})
		}
	}
}

// TestFrozenClockEvidenceCannotHideContradictions preserves any exact match without a progression gate.
func TestFrozenClockEvidenceCannotHideContradictions(t *testing.T) {
	frozen := withPeriod(frame(true, false, "90:00", "+5", ""), PeriodSecondHalf)
	missing := frame(true, false, "", "", "")
	exact := withPeriod(frame(true, false, "95:15", "", ""), PeriodSecondHalf)
	wrong := withPeriod(frame(true, false, "80:15", "", ""), PeriodSecondHalf)
	for _, tc := range []struct {
		name   string
		frames []FrameObservation
		exp    Expected
		want   Outcome
	}{
		{"unknown minute", rep(frozen, 3), Expected{90, 6}, OutcomeUnverified},
		{"nico 46 lower bound", rep(frozen, 3), Expected{46, 0}, OutcomeRejected},
		{"normal time near boundary", rep(frozen, 3), Expected{90, 0}, OutcomeUnverified},
		{"wrong half", rep(frozen, 3), Expected{45, 5}, OutcomeRejected},
		{"independent exact", []FrameObservation{frozen, missing, exact}, Expected{90, 6}, OutcomeVerified},
		{"contradictory exact", []FrameObservation{frozen, missing, wrong}, Expected{90, 6}, OutcomeRejected},
		{"repeated exact", rep(exact, 3), Expected{90, 6}, OutcomeVerified},
		{"one readable", []FrameObservation{missing, exact, missing}, Expected{90, 6}, OutcomeVerified},
		{"reverse replay", []FrameObservation{withPeriod(frame(true, false, "95:30", "", ""), PeriodSecondHalf), exact, withPeriod(frame(true, false, "95:05", "", ""), PeriodSecondHalf)}, Expected{90, 6}, OutcomeVerified},
		{"lower bound plus matching earlier replay", []FrameObservation{frozen, missing, withPeriod(frame(true, false, "45:15", "", ""), PeriodSecondHalf)}, Expected{46, 0}, OutcomeVerified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := append([]FrameObservation(nil), tc.frames...)
			// Frame order must not change a verdict, including frozen-first cases.
			for i := 0; i < 3; i++ {
				ev := Evaluate(tc.frames, tc.exp, 1)
				if ev.Outcome != tc.want || (ev.MatchedMinute != nil) != (tc.want == OutcomeVerified) {
					t.Fatalf("evaluation = %+v", ev)
				}
				if !reflect.DeepEqual(original, tc.frames) {
					t.Fatal("mutated raw observations")
				}
				tc.frames = append(tc.frames[1:3:3], tc.frames[0])
				original = append([]FrameObservation(nil), tc.frames...)
			}
		})
	}
}

// TestRunningAllowanceRegressions uses agent-read Fernandez clocks and saved Adzic/Maitland shapes.
// These are explicit controls; the offline replay retains the original model records separately.
func TestRunningAllowanceRegressions(t *testing.T) {
	for _, tc := range []struct {
		name             string
		clocks           []string
		added, secondary string
		exp              Expected
		want             Outcome
	}{
		{"fernandez ordinary", []string{"45:17", "45:25", "45:33"}, "+1", "", Expected{45, 0}, OutcomeVerified},
		{"fernandez wrong stoppage control", []string{"45:17", "45:25", "45:33"}, "+1", "", Expected{45, 4}, OutcomeRejected},
		{"adzic running allowance", []string{"90:28", "90:37", "90:59"}, "+4", "", Expected{90, 4}, OutcomeRejected},
		{"adzic erroneous secondary", []string{"90:28", "90:37", "90:59"}, "", "04:00", Expected{90, 4}, OutcomeRejected},
		{"maitland erroneous secondary", []string{"95:04", "95:15", "95:27"}, "+5", "05:00", Expected{90, 6}, OutcomeVerified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var frames []FrameObservation
			for _, clock := range tc.clocks {
				frames = append(frames, frame(true, false, clock, tc.added, tc.secondary))
			}
			ev := Evaluate(frames, tc.exp, 1)
			if ev.Outcome != tc.want {
				t.Fatalf("evaluation = %+v", ev)
			}
		})
	}
	for _, period := range []Period{PeriodFirstHalf, PeriodSecondHalf, PeriodExtraFirst, PeriodExtraSecond} {
		f := withPeriod(frame(true, false, "2H 05:25", "+5", "05:00"), period)
		if period != PeriodSecondHalf {
			if r, ok := parseFrameClock(0, f); !ok || !r.Ambiguous {
				t.Fatalf("lost explicit conflict: %+v", r)
			}
		}
	}
}
