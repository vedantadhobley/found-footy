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

// Period names which segment of the match a minute belongs to. String values
// are also the constrained wire values returned by the vision model. The
// boundary minute (45/90/105/120) belongs to the LOWER period, because a
// stoppage-of-period-N clock freezes at that boundary (45+3 is still H1).
type Period string

const (
	PeriodUnknown     Period = ""
	PeriodFirstHalf   Period = "1H"  // 1–45 (+ H1 stoppage frozen at 45)
	PeriodSecondHalf  Period = "2H"  // 46–90 (+ H2 stoppage frozen at 90)
	PeriodExtraFirst  Period = "ET1" // 91–105 (+ ET1 stoppage frozen at 105)
	PeriodExtraSecond Period = "ET2" // 106–120 (+ ET2 stoppage frozen at 120)
)

// ClockReading is one normalized per-frame scorebug reading. Minute is an
// absolute completed match minute after any relative-period or stoppage-clock
// offset. PeriodPinned means the scorebug supplied enough evidence to identify
// the period (a visible label, compact stoppage form, or recognized running
// clock plus allowance), rather than periodOf inferring one from a conventional
// continuous clock. Stoppage records compact/frozen or recognized running
// stoppage evidence; ExactMinute is false only when a
// frozen boundary and announced added time are visible without a running
// stoppage sub-clock. Ambiguous readings are diagnostic evidence but may not
// verify a clip.
type ClockReading struct {
	FrameIndex         int    `json:"frame_index"`
	Minute             int    `json:"minute"`
	AlternativeMinutes []int  `json:"alternative_minutes,omitempty"`
	Period             Period `json:"period"`
	PeriodPinned       bool   `json:"period_pinned"`
	Stoppage           bool   `json:"stoppage"`
	ExactMinute        bool   `json:"exact_minute"`
	Ambiguous          bool   `json:"ambiguous"`
}

// periodOf maps an API BASE minute or a conventional non-stoppage clock to its
// period. Broadcast values are provisional until explicit labels and allowance
// evidence have been composed; a running 46:xx can still belong to H1.
// Verified against real API-Football data
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

// Valid reports whether p is one of the values accepted from the model.
func (p Period) Valid() bool {
	switch p {
	case PeriodFirstHalf, PeriodSecondHalf, PeriodExtraFirst, PeriodExtraSecond:
		return true
	default:
		return false
	}
}

// String renders a Period as a short label for logs + candidate outcome
// detail, so a clock-reject can record "detected 2H vs expected 1H" (#181).
func (p Period) String() string {
	if !p.Valid() {
		return "unknown"
	}
	return string(p)
}

var (
	reCompactStoppage = regexp.MustCompile(`^(45|90|105|120)\s*\+\s*(\d{1,2})(?::(\d{2}))?$`)
	reMMSS            = regexp.MustCompile(`^(\d{1,3}):(\d{2})$`) // "90:00"
	reBareMinutes     = regexp.MustCompile(`^(\d{1,3})$`)         // "90"
	reAdded           = regexp.MustCompile(`^\+?\s*(\d{1,2})\s*['′]?$`)
	rePeriodHints     = regexp.MustCompile(`\b(ET|AET|EXTRA\s*TIME|2H|2ND\s*HALF|1H|1ST\s*HALF)\b`)
	reETPeriod        = regexp.MustCompile(`\b(ET|AET|EXTRA\s*TIME)\b`)
	reSecondHalf      = regexp.MustCompile(`\b(2H|2ND\s*HALF)\b`)
	reFirstHalf       = regexp.MustCompile(`\b(1H|1ST\s*HALF)\b`)
)

// parsedTimer preserves visible precision until auxiliary fields are composed.
// A minute-only reading must never be mistaken for a frozen MM:00 display.
type parsedTimer struct {
	minute       int
	second       int
	secondsKnown bool
}

// parseTimer accepts a complete timer, not a numeric substring of malformed OCR.
func parseTimer(text string, allowBare bool) (parsedTimer, bool) {
	if m := reMMSS.FindStringSubmatch(text); m != nil {
		timer := parsedTimer{minute: atoi(m[1]), second: atoi(m[2]), secondsKnown: true}
		return timer, timer.second < 60
	}
	if allowBare && reBareMinutes.MatchString(text) {
		return parsedTimer{minute: atoi(text)}, true
	}
	return parsedTimer{}, false
}

// parseClockField parses the MAIN timer field into a structured reading.
// Handles "MM:SS", bare "MM", compact "45+2", and 1H/2H/ET textual hints.
// Returns ok=false for empty / non-clock text (NONE/HT/FT). Ported from
// Python parse_clock_field without collapsing period/stoppage provenance.
func parseClockField(raw string) (ClockReading, parsedTimer, bool) {
	r := ClockReading{ExactMinute: true}
	text := strings.ToUpper(strings.TrimSpace(raw))
	switch text {
	case "", "NONE", "HT", "FT", "HALF TIME", "FULL TIME":
		return ClockReading{}, parsedTimer{}, false
	}

	hasET := reETPeriod.MatchString(text)
	has2H := reSecondHalf.MatchString(text)
	has1H := reFirstHalf.MatchString(text)
	if boolCount(hasET, has2H, has1H) > 1 {
		r.Ambiguous = true
	}
	clean := strings.TrimSpace(rePeriodHints.ReplaceAllString(text, ""))

	// Compact stoppage "45+2" → minute 47 while retaining H1 as the
	// structurally pinned period. This is the provenance the integer parser
	// previously discarded.
	if m := reCompactStoppage.FindStringSubmatch(clean); m != nil {
		if m[3] != "" && atoi(m[3]) >= 60 {
			return ClockReading{}, parsedTimer{}, false
		}
		base := atoi(m[1])
		r.Minute = base + atoi(m[2])
		r.Period = periodOf(base)
		r.PeriodPinned = true
		r.Stoppage = true
		if hinted := embeddedPeriod(hasET, has2H, has1H, r.Minute); hinted.Valid() && hinted != r.Period {
			r.Period = PeriodUnknown
			r.Ambiguous = true
		}
		return r, parsedTimer{}, true
	}

	timer, ok := parseTimer(clean, true)
	if !ok {
		return ClockReading{}, parsedTimer{}, false
	}
	minutes := timer.minute

	r.Minute = minutes
	r.Period = periodOf(minutes)

	// Relative-clock offset hints (a "2H 15:00" broadcast shows 15, meaning
	// 60). The separate FrameObservation.period field follows this same path.
	switch {
	case hasET:
		if minutes <= 30 {
			r.Minute = 90 + minutes
		}
		r.Period = periodOf(r.Minute)
		r.PeriodPinned = true
	case has2H:
		if minutes < 45 {
			r.Minute = 45 + minutes
		} else if minutes == 45 {
			r.AlternativeMinutes = []int{90}
		}
		r.Period = PeriodSecondHalf
		r.PeriodPinned = true
	case has1H:
		r.Period = PeriodFirstHalf
		r.PeriodPinned = true
	}
	return r, timer, true
}

// parseFrameClock merges the raw main timer with the model's separately
// observed period and stoppage fields. A disagreement between two explicit
// period signals remains visible as Ambiguous and cannot verify a clip.
func parseFrameClock(frameIndex int, f FrameObservation) (ClockReading, bool) {
	if f.Clock == nil {
		return ClockReading{}, false
	}
	r, timer, ok := parseClockField(*f.Clock)
	if !ok {
		return ClockReading{}, false
	}
	r.FrameIndex = frameIndex

	hint, hasHint, conflictingHint := framePeriodEvidence(f)
	if conflictingHint {
		r.Period = PeriodUnknown
		r.Ambiguous = true
	} else if hasHint {
		switch {
		case r.PeriodPinned && r.Period != hint:
			r.Period = PeriodUnknown
			r.Ambiguous = true
		case !r.PeriodPinned:
			displayedMinute := r.Minute
			minute, normalizable := normalizeDisplayedMinute(displayedMinute, hint)
			r.Minute = minute
			r.AlternativeMinutes = boundaryAlternatives(displayedMinute, hint, minute)
			r.Period = hint
			r.PeriodPinned = true
			if !normalizable {
				r.Ambiguous = true
			}
		}
	}

	r = withRunningStoppage(r, f.Added)

	// Only seconds-visible, compatible end boundaries can freeze and accept
	// elapsed stoppage. Running and compact clocks remain usable even when an
	// auxiliary field is malformed, redundant or assigned to the wrong role.
	boundary, compatible := frozenBoundary(r, timer)
	if !compatible {
		return r, true
	}
	if f.StoppageClock != nil {
		if stop, hasStop := parseStoppageClockField(*f.StoppageClock); hasStop {
			r.Minute = boundary + stop
			r.AlternativeMinutes = nil
			r.Stoppage = true
			return r, true
		}
	}
	if f.Added != nil {
		if _, hasAdded := parseAddedField(*f.Added); hasAdded {
			r.Minute = boundary // lower bound, not a measured stoppage minute
			r.AlternativeMinutes = nil
			r.Stoppage, r.ExactMinute = true, false
		}
	}
	return r, true
}

// frozenBoundary narrows reset/continuous alternatives to the period's end.
// It recognizes compatible syntax; it does not claim the video actually froze.
func frozenBoundary(r ClockReading, timer parsedTimer) (int, bool) {
	if r.Ambiguous || r.Stoppage || !timer.secondsKnown || timer.second != 0 {
		return 0, false
	}
	var end int
	switch r.Period {
	case PeriodFirstHalf:
		end = 45
	case PeriodSecondHalf:
		end = 90
	case PeriodExtraFirst:
		end = 105
	case PeriodExtraSecond:
		end = 120
	default:
		return 0, false
	}
	for _, minute := range possibleMinutes(r) {
		if minute == end {
			return end, true
		}
	}
	return 0, false
}

// boundaryAlternatives preserves clock values that are structurally valid in
// more than one broadcast convention. A visible 45:xx 2H can be continuous
// match time at the start of H2 or a reset-per-half clock at the end of H2;
// 15:xx ET2 has the equivalent reset-versus-cumulative ambiguity. The parser
// exposes both readings so evaluation can compare evidence without replacing
// it with API context.
func boundaryAlternatives(displayed int, period Period, primary int) []int {
	var candidate int
	switch {
	case period == PeriodSecondHalf && displayed == 45:
		candidate = 90
	case period == PeriodExtraSecond && displayed == 15:
		candidate = 105
	default:
		return nil
	}
	if candidate == primary {
		return nil
	}
	return []int{candidate}
}

// normalizeDisplayedMinute rebases a period-relative scorebug into the
// conventional absolute match clock. Unknown ET shapes remain ambiguous
// rather than being forced into a broadcaster-specific interpretation.
func normalizeDisplayedMinute(minute int, period Period) (int, bool) {
	switch period {
	case PeriodFirstHalf:
		return minute, true
	case PeriodSecondHalf:
		if minute < 45 {
			return 45 + minute, true
		}
		return minute, true
	case PeriodExtraFirst:
		switch {
		case minute <= 15:
			return 90 + minute, true
		case minute >= 90:
			return minute, true
		default:
			return minute, false
		}
	case PeriodExtraSecond:
		switch {
		case minute <= 15: // reset for the second ET period
			return 105 + minute, true
		case minute <= 30: // cumulative clock across both ET periods
			return 90 + minute, true
		case minute >= 105:
			return minute, true
		default:
			return minute, false
		}
	default:
		return minute, false
	}
}

// embeddedPeriod resolves legacy textual hints without consulting API time.
func embeddedPeriod(hasET, has2H, has1H bool, minute int) Period {
	switch {
	case has2H:
		return PeriodSecondHalf
	case has1H:
		return PeriodFirstHalf
	case hasET:
		return periodOf(minute)
	default:
		return PeriodUnknown
	}
}

// boolCount detects conflicting explicit period hints.
func boolCount(values ...bool) int {
	n := 0
	for _, value := range values {
		if value {
			n++
		}
	}
	return n
}

// parseAddedField parses the announced added-time indicator: "+4" → 4.
// This is the ANNOUNCED total (a "+5" board), not the current stoppage
// minute. Only a compatible main boundary can make the minute unpinned.
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
// signal that pins the exact stoppage minute only beside a compatible boundary.
func parseStoppageClockField(raw string) (int, bool) {
	t := strings.TrimSpace(raw)
	t = strings.TrimPrefix(t, "+")
	t = strings.TrimSpace(t)
	up := strings.ToUpper(t)
	if up == "" || up == "NONE" || up == "N/A" {
		return 0, false
	}
	if timer, ok := parseTimer(t, false); ok && timer.minute < 100 {
		return timer.minute, true
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
