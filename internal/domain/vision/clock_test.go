// clock_test.go — table tests for the scorebug parsers + the period map.
// Covers the real broadcast renderings seen in the 2026-07-28 bake-off
// (frozen "90:00" + "+1:48" sub-timer + "+2" added; running "70:17") and
// the API-verified period boundaries (45/90/105/120).
package vision

import "testing"

func TestParseClockField(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"90:00", 90, true},
		{"70:17", 70, true},
		{"34:39", 34, true},
		{"45+2", 47, true},
		{"90", 90, true},
		{"2H 15:00", 60, true}, // relative 2nd-half clock → 45+15
		{"ET 5:00", 95, true},  // extra time 5' → 90+5
		{"1H 30:00", 30, true}, // 1st-half absolute
		{"", 0, false},
		{"NONE", 0, false},
		{"HT", 0, false},
		{"FT", 0, false},
		{"not-a-clock", 0, false},
	}
	for _, c := range cases {
		got, ok := parseClockField(c.in)
		if ok != c.ok || (ok && got.Minute != c.want) {
			t.Errorf("parseClockField(%q) = (%+v,%v), want minute (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseClockFieldRetainsPeriodProvenance(t *testing.T) {
	cases := []struct {
		name          string
		in            string
		wantMinute    int
		wantPeriod    Period
		wantExplicit  bool
		wantStoppage  bool
		wantAmbiguous bool
	}{
		{name: "continuous", in: "70:17", wantMinute: 70, wantPeriod: PeriodSecondHalf},
		{name: "embedded relative second half", in: "2H 15:00", wantMinute: 60, wantPeriod: PeriodSecondHalf, wantExplicit: true},
		{name: "compact first half stoppage", in: "45+2", wantMinute: 47, wantPeriod: PeriodFirstHalf, wantExplicit: true, wantStoppage: true},
		{name: "compact second half stoppage", in: "90+4", wantMinute: 94, wantPeriod: PeriodSecondHalf, wantExplicit: true, wantStoppage: true},
		{name: "compact first extra-time stoppage", in: "105+3", wantMinute: 108, wantPeriod: PeriodExtraFirst, wantExplicit: true, wantStoppage: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseClockField(c.in)
			if !ok {
				t.Fatal("parseClockField returned !ok")
			}
			if got.Minute != c.wantMinute || got.Period != c.wantPeriod ||
				got.PeriodPinned != c.wantExplicit || got.Stoppage != c.wantStoppage ||
				got.Ambiguous != c.wantAmbiguous {
				t.Errorf("reading = %+v, want minute=%d period=%q explicit=%v stoppage=%v ambiguous=%v",
					got, c.wantMinute, c.wantPeriod, c.wantExplicit, c.wantStoppage, c.wantAmbiguous)
			}
		})
	}
}

func TestNormalizeDisplayedMinuteByVisiblePeriod(t *testing.T) {
	cases := []struct {
		name   string
		minute int
		period Period
		want   int
		ok     bool
	}{
		{name: "first half", minute: 28, period: PeriodFirstHalf, want: 28, ok: true},
		{name: "reset second half", minute: 5, period: PeriodSecondHalf, want: 50, ok: true},
		{name: "continuous second half", minute: 50, period: PeriodSecondHalf, want: 50, ok: true},
		{name: "reset first extra time", minute: 5, period: PeriodExtraFirst, want: 95, ok: true},
		{name: "reset second extra time", minute: 5, period: PeriodExtraSecond, want: 110, ok: true},
		{name: "cumulative second extra time", minute: 20, period: PeriodExtraSecond, want: 110, ok: true},
		{name: "continuous second extra time", minute: 110, period: PeriodExtraSecond, want: 110, ok: true},
		{name: "unsupported extra-time shape", minute: 50, period: PeriodExtraFirst, want: 50, ok: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := normalizeDisplayedMinute(c.minute, c.period)
			if got != c.want || ok != c.ok {
				t.Errorf("normalizeDisplayedMinute(%d, %q) = (%d,%v), want (%d,%v)",
					c.minute, c.period, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestParseFrameClockUsesVisiblePeriod(t *testing.T) {
	second := PeriodSecondHalf
	r, ok := parseFrameClock(1, FrameObservation{Clock: sp("05:25"), Period: &second})
	if !ok {
		t.Fatal("parseFrameClock returned !ok")
	}
	if r.FrameIndex != 1 || r.Minute != 50 || r.Period != PeriodSecondHalf || !r.PeriodPinned {
		t.Errorf("reading = %+v, want frame=1 minute=50 period=2H explicit", r)
	}
}

// TestParseFrameClockRetainsBoundaryConventions covers the exact values where
// reset-per-period and cumulative scorebugs render the same display.
func TestParseFrameClockRetainsBoundaryConventions(t *testing.T) {
	cases := []struct {
		name        string
		clock       string
		period      Period
		wantPrimary int
		wantAlt     int
	}{
		{name: "second-half boundary", clock: "45:25", period: PeriodSecondHalf, wantPrimary: 45, wantAlt: 90},
		{name: "second-extra-time boundary", clock: "15:10", period: PeriodExtraSecond, wantPrimary: 120, wantAlt: 105},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, ok := parseFrameClock(0, FrameObservation{Clock: sp(c.clock), Period: &c.period})
			if !ok {
				t.Fatal("parseFrameClock returned !ok")
			}
			if r.Minute != c.wantPrimary || len(r.AlternativeMinutes) != 1 || r.AlternativeMinutes[0] != c.wantAlt {
				t.Errorf("reading = %+v, want primary=%d alternative=%d", r, c.wantPrimary, c.wantAlt)
			}
		})
	}
}

func TestParseAddedField(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"+2", 2, true},
		{"+5", 5, true},
		{"+ 4", 4, true},
		{"", 0, false},
		{"NONE", 0, false},
		{"N/A", 0, false},
	}
	for _, c := range cases {
		got, ok := parseAddedField(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseAddedField(%q) = (%d,%v), want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseStoppageClockField(t *testing.T) {
	// gemma returns "01:48", Qwen returns "+1:48" — both must yield 1.
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"01:48", 1, true},
		{"+1:48", 1, true},
		{"+1:53", 1, true},
		{"03:57", 3, true},
		{"02:17", 2, true},
		{"", 0, false},
		{"NONE", 0, false},
	}
	for _, c := range cases {
		got, ok := parseStoppageClockField(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseStoppageClockField(%q) = (%d,%v), want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestPeriodOf(t *testing.T) {
	// Boundaries verified against real API-Football data (WC-2022 final).
	cases := []struct {
		minute int
		want   Period
	}{
		{30, PeriodFirstHalf},
		{45, PeriodFirstHalf}, // 45+N stoppage frozen at 45 → H1
		{46, PeriodSecondHalf},
		{48, PeriodSecondHalf}, // 3' into H2
		{90, PeriodSecondHalf}, // 90+N stoppage frozen at 90 → H2
		{94, PeriodExtraFirst}, // 4' into ET1
		{105, PeriodExtraFirst},
		{108, PeriodExtraSecond}, // 3' into ET2 (Messi WC final)
		{120, PeriodExtraSecond},
	}
	for _, c := range cases {
		if got := periodOf(c.minute); got != c.want {
			t.Errorf("periodOf(%d) = %q, want %q", c.minute, got, c.want)
		}
	}
}
