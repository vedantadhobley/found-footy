// evaluate_test.go — the verdict logic, exercised with the real prod clips
// from the 2026-07-28 end-to-end test (Yeboah 71', Lauberbach 90+2, both
// with a clean-broadcast and a phone-of-TV variant) plus the period-guard
// boundary cases the API distinguishes (45+3 vs 48, 105+3 vs 108).
package vision

import "testing"

func sp(s string) *string { return &s }

// frame builds a FrameObservation; pass "" for a nil time field.
func frame(soccer, screen bool, clock, added, stoppage string) FrameObservation {
	f := FrameObservation{Soccer: soccer, Screen: screen}
	if clock != "" {
		f.Clock = sp(clock)
	}
	if added != "" {
		f.Added = sp(added)
	}
	if stoppage != "" {
		f.StoppageClock = sp(stoppage)
	}
	return f
}

func rep(f FrameObservation, n int) []FrameObservation {
	out := make([]FrameObservation, n)
	for i := range out {
		out[i] = f
	}
	return out
}

func withPeriod(f FrameObservation, period Period) FrameObservation {
	f.Period = &period
	return f
}

const tol = 1

func TestEvaluate_RealProdClips(t *testing.T) {
	cases := []struct {
		name    string
		frames  []FrameObservation
		exp     Expected
		want    Outcome
		wantMin int // MatchedMinute when verified; -1 if not verified
	}{
		{
			// Yeboah 71' clean broadcast: clock ticks 70:13/17/21.
			name: "yeboah71-broadcast-verified",
			frames: []FrameObservation{
				frame(true, false, "70:13", "", ""),
				frame(true, false, "70:17", "", ""),
				frame(true, false, "70:21", "", ""),
			},
			exp: Expected{Elapsed: 71, Extra: 0}, want: OutcomeVerified, wantMin: 70,
		},
		{
			// Yeboah 71' phone-of-TV variant: screen=true, no clock → reject.
			name:   "yeboah71-phone-of-tv-rejected",
			frames: rep(frame(true, true, "", "", ""), 3),
			exp:    Expected{Elapsed: 71, Extra: 0}, want: OutcomeRejected, wantMin: -1,
		},
		{
			// Lauberbach 90+2: frozen 90:00 + sub-timer +1:48 + added +2.
			// absolute = 90 + 1 = 91; expected = 90+2-1 = 91.
			name: "lauberbach90+2-stoppage-verified",
			frames: []FrameObservation{
				frame(true, false, "90:00", "+2", "+1:48"),
				frame(true, false, "90:00", "+2", "+1:53"),
				frame(true, false, "90:00", "+2", "+1:58"),
			},
			exp: Expected{Elapsed: 90, Extra: 2}, want: OutcomeVerified, wantMin: 91,
		},
		{
			// Lauberbach 90+2 phone-of-TV variant → reject.
			name:   "lauberbach90+2-phone-of-tv-rejected",
			frames: rep(frame(true, true, "", "", ""), 3),
			exp:    Expected{Elapsed: 90, Extra: 2}, want: OutcomeRejected, wantMin: -1,
		},
		{
			// Fan phone-vid of the real pitch: soccer, not screen, no clock.
			name:   "fan-video-no-clock-unverified",
			frames: rep(frame(true, false, "", "", ""), 3),
			exp:    Expected{Elapsed: 71, Extra: 0}, want: OutcomeUnverified, wantMin: -1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := Evaluate(c.frames, c.exp, tol)
			if ev.Outcome != c.want {
				t.Fatalf("Outcome = %q (%s), want %q", ev.Outcome, ev.Reason, c.want)
			}
			if c.wantMin >= 0 {
				if ev.MatchedMinute == nil || *ev.MatchedMinute != c.wantMin {
					t.Errorf("MatchedMinute = %v, want %d", ev.MatchedMinute, c.wantMin)
				}
			}
		})
	}
}

// TestEvaluate_CapturesDetectedClock — the read clock is retained on the
// Evaluation (even on a clock-reject) so a candidate's outcome_detail can show
// detected-vs-expected for post-hoc triage (#181), not just "didn't match".
func TestEvaluate_CapturesDetectedClock(t *testing.T) {
	// Clock present but wrong: read 10:00 (1H) for a 71' (2H) goal → rejected,
	// and the read clock is retained.
	rej := Evaluate(rep(frame(true, false, "10:00", "", ""), 3), Expected{Elapsed: 71, Extra: 0}, tol)
	if rej.Outcome != OutcomeRejected {
		t.Fatalf("Outcome = %q (%s), want rejected", rej.Outcome, rej.Reason)
	}
	if rej.DetectedMinute == nil || *rej.DetectedMinute != 10 || rej.DetectedPeriod != "1H" {
		t.Errorf("detected = %v/%q, want 10/1H", rej.DetectedMinute, rej.DetectedPeriod)
	}
	if rej.ExpectedMinute != 70 || rej.ExpectedPeriod != "2H" {
		t.Errorf("expected = %d/%q, want 70/2H", rej.ExpectedMinute, rej.ExpectedPeriod)
	}

	// No clock visible → DetectedMinute stays nil (distinguishes "no clock"
	// from "clock read as some minute").
	noClk := Evaluate(rep(frame(true, false, "", "", ""), 3), Expected{Elapsed: 71, Extra: 0}, tol)
	if noClk.DetectedMinute != nil {
		t.Errorf("no-clock DetectedMinute = %d, want nil", *noClk.DetectedMinute)
	}

	// Verified clip also carries the detected clock, not just MatchedMinute.
	ver := Evaluate(rep(frame(true, false, "70:13", "", ""), 3), Expected{Elapsed: 71, Extra: 0}, tol)
	if ver.Outcome != OutcomeVerified {
		t.Fatalf("Outcome = %q (%s), want verified", ver.Outcome, ver.Reason)
	}
	if ver.DetectedMinute == nil || *ver.DetectedMinute != 70 {
		t.Errorf("verified detected = %v, want 70", ver.DetectedMinute)
	}
}

// TestEvaluate_MinuteNormalization locks the provider-to-broadcast clock
// contract independently of the verdict. A ±1 verdict alone did not catch the
// Go-port regression that calculated elapsed-1 and then clamped it to elapsed.
func TestEvaluate_MinuteNormalization(t *testing.T) {
	cases := []struct {
		name       string
		frames     []FrameObservation
		exp        Expected
		want       Outcome
		wantExpect int
		wantMatch  int
	}{
		{
			name:       "first-minute-clock-zero",
			frames:     rep(frame(true, false, "00:30", "", ""), 3),
			exp:        Expected{Elapsed: 1},
			want:       OutcomeVerified,
			wantExpect: 0,
			wantMatch:  0,
		},
		{
			// Abdelkarim live regression: API 30', sampled clock 28'. The
			// normalized center is 29, so 28 remains inside the intended ±1.
			name:       "normal-time-lower-tolerance-edge",
			frames:     rep(frame(true, false, "28:30", "", ""), 3),
			exp:        Expected{Elapsed: 30},
			want:       OutcomeVerified,
			wantExpect: 29,
			wantMatch:  28,
		},
		{
			name:       "normal-time-outside-tolerance",
			frames:     rep(frame(true, false, "27:59", "", ""), 3),
			exp:        Expected{Elapsed: 30},
			want:       OutcomeRejected,
			wantExpect: 29,
			wantMatch:  -1,
		},
		{
			name:       "first-half-stoppage-second-minute",
			frames:     rep(frame(true, false, "45:00", "+2", "+1:30"), 3),
			exp:        Expected{Elapsed: 45, Extra: 2},
			want:       OutcomeVerified,
			wantExpect: 46,
			wantMatch:  46,
		},
		{
			name:       "second-half-forty-seventh-minute",
			frames:     rep(frame(true, false, "46:30", "", ""), 3),
			exp:        Expected{Elapsed: 47},
			want:       OutcomeVerified,
			wantExpect: 46,
			wantMatch:  46,
		},
		{
			name:       "second-half-stoppage-fourth-minute",
			frames:     rep(frame(true, false, "90:00", "+4", "+3:30"), 3),
			exp:        Expected{Elapsed: 90, Extra: 4},
			want:       OutcomeVerified,
			wantExpect: 93,
			wantMatch:  93,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := Evaluate(c.frames, c.exp, tol)
			if ev.ExpectedMinute != c.wantExpect {
				t.Errorf("ExpectedMinute = %d, want %d", ev.ExpectedMinute, c.wantExpect)
			}
			if ev.Outcome != c.want {
				t.Fatalf("Outcome = %q (%s), want %q", ev.Outcome, ev.Reason, c.want)
			}
			if c.wantMatch < 0 {
				if ev.MatchedMinute != nil {
					t.Errorf("MatchedMinute = %d, want nil", *ev.MatchedMinute)
				}
				return
			}
			if ev.MatchedMinute == nil || *ev.MatchedMinute != c.wantMatch {
				t.Errorf("MatchedMinute = %v, want %d", ev.MatchedMinute, c.wantMatch)
			}
		})
	}
}

func TestEvaluate_ResetPerHalfScorebug(t *testing.T) {
	cases := []struct {
		name      string
		frame     FrameObservation
		exp       Expected
		want      Outcome
		wantMatch int
	}{
		{
			name:  "abdelkarim first-half regression",
			frame: withPeriod(frame(true, false, "28:56", "", ""), PeriodFirstHalf),
			exp:   Expected{Elapsed: 30}, want: OutcomeVerified, wantMatch: 28,
		},
		{
			name:  "zizo reset second-half clock",
			frame: withPeriod(frame(true, false, "05:25", "", ""), PeriodSecondHalf),
			exp:   Expected{Elapsed: 51}, want: OutcomeVerified, wantMatch: 50,
		},
		{
			name:  "continuous second-half clock remains absolute",
			frame: withPeriod(frame(true, false, "50:25", "", ""), PeriodSecondHalf),
			exp:   Expected{Elapsed: 51}, want: OutcomeVerified, wantMatch: 50,
		},
		{
			name:  "explicit first-half conflict rejects",
			frame: withPeriod(frame(true, false, "05:25", "", ""), PeriodFirstHalf),
			exp:   Expected{Elapsed: 51}, want: OutcomeRejected, wantMatch: -1,
		},
		{
			name:  "missing period cannot manufacture verification",
			frame: frame(true, false, "05:25", "", ""),
			exp:   Expected{Elapsed: 51}, want: OutcomeUnverified, wantMatch: -1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := Evaluate(rep(c.frame, 3), c.exp, tol)
			if ev.Outcome != c.want {
				t.Fatalf("Outcome = %q (%s), want %q", ev.Outcome, ev.Reason, c.want)
			}
			if c.wantMatch < 0 {
				if ev.MatchedMinute != nil {
					t.Fatalf("MatchedMinute = %d, want nil", *ev.MatchedMinute)
				}
				return
			}
			if ev.MatchedMinute == nil || *ev.MatchedMinute != c.wantMatch {
				t.Fatalf("MatchedMinute = %v, want %d", ev.MatchedMinute, c.wantMatch)
			}
			if len(ev.ClockReadings) != 3 {
				t.Fatalf("ClockReadings len = %d, want 3", len(ev.ClockReadings))
			}
		})
	}
}

func TestEvaluate_PeriodGuard(t *testing.T) {
	// The halftime boundary is clean on both sides → strict reject of the
	// wrong half; the ET boundary is ambiguous → soft-keep (unverified).
	cases := []struct {
		name   string
		frames []FrameObservation
		exp    Expected
		want   Outcome
	}{
		{
			// H1 stoppage clip (45:00 +3, sub 02:xx → 47) vs a 48' H2 goal.
			// Clean wrong-half conflict → reject.
			name:   "h1-stoppage-clip-vs-h2-goal-rejected",
			frames: rep(frame(true, false, "45:00", "+3", "02:10"), 3),
			exp:    Expected{Elapsed: 48, Extra: 0}, want: OutcomeRejected,
		},
		{
			// 48' running clip vs a 45+3 H1 goal → clean wrong-half → reject.
			name:   "h2-running-clip-vs-h1-stoppage-goal-rejected",
			frames: rep(frame(true, false, "48:00", "", ""), 3),
			exp:    Expected{Elapsed: 45, Extra: 3}, want: OutcomeRejected,
		},
		{
			// Genuine H1 stoppage clip vs the 45+3 goal → verified (47 vs exp 47).
			name:   "h1-stoppage-clip-vs-h1-goal-verified",
			frames: rep(frame(true, false, "45:00", "+3", "02:10"), 3),
			exp:    Expected{Elapsed: 45, Extra: 3}, want: OutcomeVerified,
		},
		{
			// ET1-stoppage clip (105:00 +3 → 108) vs an ET2 108' goal.
			// Numeric matches (108==expected 108) but period differs (ET1 vs
			// ET2) → ET ambiguity → soft-keep as unverified, not dropped.
			name:   "et1-stoppage-clip-vs-et2-goal-softkeep",
			frames: rep(frame(true, false, "105:00", "+3", "03:05"), 3),
			exp:    Expected{Elapsed: 108, Extra: 0}, want: OutcomeUnverified,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if ev := Evaluate(c.frames, c.exp, tol); ev.Outcome != c.want {
				t.Errorf("Outcome = %q (%s), want %q", ev.Outcome, ev.Reason, c.want)
			}
		})
	}
}

func TestEvaluate_ClockCorrections(t *testing.T) {
	cases := []struct {
		name    string
		frames  []FrameObservation
		exp     Expected
		want    Outcome
		wantMin int
	}{
		{
			// OCR dropped the leading 9: "02:36" for a 90+2 stoppage goal.
			// rebase = elapsed(90) + 2 = 92; expected = 91 → within ±1.
			name:   "ocr-leading-digit-rebase",
			frames: rep(frame(true, false, "02:36", "+2", ""), 3),
			exp:    Expected{Elapsed: 90, Extra: 2}, want: OutcomeVerified, wantMin: 92,
		},
		{
			// Frozen 90:00 + added +5 but NO sub-timer: exact minute unpinned,
			// right period's stoppage → verify on period (candidate 2).
			name:   "frozen-boundary-no-subtimer-verified",
			frames: rep(frame(true, false, "90:00", "+5", ""), 3),
			exp:    Expected{Elapsed: 90, Extra: 5}, want: OutcomeVerified, wantMin: 90,
		},
		{
			// Clock clearly wrong minute (30:00 for a 90' goal) → reject.
			name:   "clock-wrong-minute-rejected",
			frames: rep(frame(true, false, "30:00", "", ""), 3),
			exp:    Expected{Elapsed: 90, Extra: 0}, want: OutcomeRejected, wantMin: -1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev := Evaluate(c.frames, c.exp, tol)
			if ev.Outcome != c.want {
				t.Fatalf("Outcome = %q (%s), want %q", ev.Outcome, ev.Reason, c.want)
			}
			if c.wantMin >= 0 && (ev.MatchedMinute == nil || *ev.MatchedMinute != c.wantMin) {
				t.Errorf("MatchedMinute = %v, want %d", ev.MatchedMinute, c.wantMin)
			}
		})
	}
}

func TestEvaluate_Gates(t *testing.T) {
	// not-soccer → reject; majority screen → reject; empty → reject.
	if ev := Evaluate(rep(frame(false, false, "45:00", "", ""), 3), Expected{Elapsed: 45}, tol); ev.Outcome != OutcomeRejected {
		t.Errorf("not-soccer: got %q, want rejected", ev.Outcome)
	}
	// 2/3 screen votes = majority → reject.
	mixed := []FrameObservation{
		frame(true, true, "", "", ""),
		frame(true, true, "", "", ""),
		frame(true, false, "", "", ""),
	}
	if ev := Evaluate(mixed, Expected{Elapsed: 45}, tol); ev.Outcome != OutcomeRejected {
		t.Errorf("majority-screen: got %q, want rejected", ev.Outcome)
	}
	if ev := Evaluate(nil, Expected{Elapsed: 45}, tol); ev.Outcome != OutcomeRejected {
		t.Errorf("empty: got %q, want rejected", ev.Outcome)
	}
}

func TestEvaluate_UnknownAPIMinuteKeepsSoccerUnverified(t *testing.T) {
	for _, frames := range [][]FrameObservation{
		rep(frame(true, false, "71:30", "", ""), 3),
		rep(frame(true, false, "", "", ""), 3),
	} {
		ev := Evaluate(frames, Expected{Elapsed: 0}, tol)
		if ev.Outcome != OutcomeUnverified || ev.Reason != "API minute unavailable" {
			t.Fatalf("unknown API minute outcome/reason = %q/%q, want unverified/API minute unavailable", ev.Outcome, ev.Reason)
		}
		if ev.MatchedMinute != nil {
			t.Fatalf("unknown API minute must not claim a match: %v", ev.MatchedMinute)
		}
	}

	// Missing timestamp evidence does not bypass the content gates.
	if ev := Evaluate(rep(frame(false, false, "71:30", "", ""), 3), Expected{}, tol); ev.Outcome != OutcomeRejected {
		t.Fatalf("non-soccer with unknown minute = %q, want rejected", ev.Outcome)
	}
	if ev := Evaluate(rep(frame(true, true, "71:30", "", ""), 3), Expected{}, tol); ev.Outcome != OutcomeRejected {
		t.Fatalf("screen recording with unknown minute = %q, want rejected", ev.Outcome)
	}
}
