// clock.go — broadcast-clock field parsers + the match-period map. Ported
// from archive/src/activities/vision.py (parse_clock_field / parse_added_field
// / parse_stoppage_clock_field / compute_absolute_minute) with the
// period-awareness the Python version lacked. Pure + table-tested: no I/O,
// no model, no infra. The vision model reads raw digits off the scorebug;
// this file turns those digits into an absolute match minute + a period so
// evaluate.go can validate them against the API's reported time.
package vision

import (
	"regexp"
	"strings"
)

// Period names which segment of the match a minute belongs to. The
// boundary minute (45/90/105/120) belongs to the LOWER period, because a
// stoppage-of-period-N clock freezes at that boundary (45+3 is still H1).
type Period int

const (
	PeriodUnknown     Period = iota
	PeriodFirstHalf          // 1–45 (+ H1 stoppage frozen at 45)
	PeriodSecondHalf         // 46–90 (+ H2 stoppage frozen at 90)
	PeriodExtraFirst         // 91–105 (+ ET1 stoppage frozen at 105)
	PeriodExtraSecond        // 106–120 (+ ET2 stoppage frozen at 120)
)

// periodOf maps a BASE minute (the frozen boundary or the running value,
// NOT base+stoppage) to its period. Verified against real API-Football data
// (WC-2022 final, 2026-07-28): a 45+3 goal reports elapsed=45 → H1; a 48'
// goal reports elapsed=48 → H2; a 108' goal reports elapsed=108 → ET2; a
// 105+3 goal reports elapsed=105 → ET1. So elapsed alone names the period.
func periodOf(baseMinute int) Period {
	switch {
	case baseMinute <= 45:
		return PeriodFirstHalf
	case baseMinute <= 90:
		return PeriodSecondHalf
	case baseMinute <= 105:
		return PeriodExtraFirst
	default:
		return PeriodExtraSecond
	}
}

// isExtraTime reports whether a period is one of the extra-time halves —
// where broadcast clock rendering is unreliable, so evaluate.go softens the
// period guard rather than dropping a clip (see the strictness rules).
func isExtraTime(p Period) bool {
	return p == PeriodExtraFirst || p == PeriodExtraSecond
}

// String renders a Period as a short label for logs + candidate outcome
// detail, so a clock-reject can record "detected 2H vs expected 1H" (#181).
func (p Period) String() string {
	switch p {
	case PeriodFirstHalf:
		return "1H"
	case PeriodSecondHalf:
		return "2H"
	case PeriodExtraFirst:
		return "ET1"
	case PeriodExtraSecond:
		return "ET2"
	default:
		return "unknown"
	}
}

var (
	reCompactStoppage = regexp.MustCompile(`^(\d+)\s*\+\s*(\d+)`)  // "45+2"
	reMMSS            = regexp.MustCompile(`(\d{1,3}):(\d{2})`)    // "90:00"
	reBareMinutes     = regexp.MustCompile(`^(\d{1,3})$`)          // "90"
	reAdded           = regexp.MustCompile(`\+\s*(\d+)`)           // "+4"
	rePeriodHints     = regexp.MustCompile(`\b(ET|AET|EXTRA\s*TIME|2H|2ND\s*HALF|1H|1ST\s*HALF)\b`)
)

// parseClockField parses the MAIN timer field into an absolute minute.
// Handles "MM:SS", bare "MM", compact "45+2", and 1H/2H/ET textual hints.
// Returns ok=false for empty / non-clock text (NONE/HT/FT). Ported from
// Python parse_clock_field.
func parseClockField(raw string) (int, bool) {
	text := strings.ToUpper(strings.TrimSpace(raw))
	switch text {
	case "", "NONE", "HT", "FT", "HALF TIME", "FULL TIME":
		return 0, false
	}

	// 1H (or no hint) means the value is already absolute — that's the
	// default branch, so we only need to detect the ET / 2H offset cases.
	hasET := regexp.MustCompile(`\b(ET|AET|EXTRA\s*TIME)\b`).MatchString(text)
	has2H := regexp.MustCompile(`\b(2H|2ND\s*HALF)\b`).MatchString(text)
	clean := strings.TrimSpace(rePeriodHints.ReplaceAllString(text, ""))

	// Compact stoppage "45+2" → 47.
	if m := reCompactStoppage.FindStringSubmatch(clean); m != nil {
		return atoi(m[1]) + atoi(m[2]), true
	}

	var minutes int
	if m := reMMSS.FindStringSubmatch(clean); m != nil {
		minutes = atoi(m[1])
	} else if m := reBareMinutes.FindStringSubmatch(strings.TrimSpace(clean)); m != nil {
		minutes = atoi(m[1])
	} else {
		return 0, false
	}

	// Relative-clock offset hints (a "2H 15:00" broadcast shows 15, meaning 60).
	switch {
	case hasET:
		if minutes <= 30 {
			return 90 + minutes, true
		}
		return minutes, true
	case has2H:
		if minutes < 45 {
			return 45 + minutes, true
		}
		return minutes, true
	default: // 1H hint or no hint: value is already absolute
		return minutes, true
	}
}

// parseAddedField parses the announced added-time indicator: "+4" → 4.
// This is the ANNOUNCED total (a "+5" board), not the current stoppage
// minute — it pins the period as "in stoppage" but not the exact minute.
func parseAddedField(raw string) (int, bool) {
	t := strings.ToUpper(strings.TrimSpace(raw))
	if t == "" || t == "NONE" || t == "N/A" {
		return 0, false
	}
	if m := reAdded.FindStringSubmatch(t); m != nil {
		return atoi(m[1]), true
	}
	return 0, false
}

// parseStoppageClockField parses the secondary stoppage sub-timer's MINUTES
// component: "03:57" → 3, "+1:48" → 1. Strips a leading '+' first — gemma
// returns "01:48", Qwen returns "+1:48" (bake-off 2026-07-28). This is the
// signal that pins the exact stoppage minute (base + this).
func parseStoppageClockField(raw string) (int, bool) {
	t := strings.TrimSpace(raw)
	t = strings.TrimPrefix(t, "+")
	t = strings.TrimSpace(t)
	up := strings.ToUpper(t)
	if up == "" || up == "NONE" || up == "N/A" {
		return 0, false
	}
	if m := regexp.MustCompile(`^(\d{1,2}):(\d{2})`).FindStringSubmatch(t); m != nil {
		return atoi(m[1]), true
	}
	return 0, false
}

// atoi is a panic-free int parse for regexp-captured digit groups (always
// numeric, so an error is impossible — but we avoid strconv's error return
// cluttering the parsers).
func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}
