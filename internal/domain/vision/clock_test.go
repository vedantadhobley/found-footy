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
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("parseClockField(%q) = (%d,%v), want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
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
			t.Errorf("periodOf(%d) = %d, want %d", c.minute, got, c.want)
		}
	}
}
