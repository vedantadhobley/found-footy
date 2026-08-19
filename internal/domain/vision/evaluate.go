// evaluate.go — turns the model's per-frame observations into a single
// clip verdict (verified / unverified / rejected), applying the soccer +
// screen gates and the period-aware clock check. Pure domain logic: the
// activity does the model call + frame extraction, then hands the parsed
// frames here. Strictness rules settled 2026-07-28 (see vision.md):
//   - ±1 minute tolerance (configurable);
//   - period match required, but hard-reject a wrong-half clip only when
//     the conflict is clean (halftime); soften to "unverified" when an
//     extra-time boundary is involved (broadcast ET rendering is unreliable).
package vision

// Outcome is the clip's verdict, which routes it into a dedup pool.
type Outcome string

const (
	// OutcomeVerified — soccer, not a screen recording, clock present and
	// consistent with the API minute+period. Routes to the clock pool (top).
	OutcomeVerified Outcome = "verified"
	// OutcomeUnverified — soccer, not a screen recording, but no usable
	// clock (none visible, or an ET-ambiguous reading we won't trust as
	// verified but won't drop). Routes to the no-clock pool (below clock).
	OutcomeUnverified Outcome = "unverified"
	// OutcomeRejected — not soccer, OR a phone-of-TV screen recording, OR a
	// clock that is present but clearly wrong (wrong minute / clean wrong
	// half). Dropped.
	OutcomeRejected Outcome = "rejected"
)

// Expected carries the API-reported time for the goal being validated.
// Extra is 0 when the goal isn't in stoppage. Both come straight from the
// fixture event's time.{elapsed,extra}.
type Expected struct {
	Elapsed int
	Extra   int
}

// Evaluation is the verdict plus the evidence behind it (for logging +
// candidate persistence). MatchedMinute is set only when verified.
type Evaluation struct {
	Outcome       Outcome
	MatchedMinute *int
	Reason        string
	SoccerVotes   int // frames voting soccer=true
	ScreenVotes   int // frames voting screen=true
	FrameCount    int
	// DetectedMinute/Period is the clock the OCR actually read (the last
	// legible reading), retained even on a clock-reject so the candidate record
	// can show "detected X vs expected Y" (#181) — otherwise a genuine
	// wrong-minute reject and an OCR-misread reject are indistinguishable
	// post-hoc. nil when no clock was visible. ExpectedMinute/Period is what
	// the clock was checked against.
	DetectedMinute *int
	DetectedPeriod string
	ExpectedMinute int
	ExpectedPeriod string
	ClockReadings  []ClockReading
}

// Evaluate applies the gates + clock check to a clip's frames. tol is the
// ±minute tolerance (1 in prod). Frames is the ordered per-frame model
// output; an empty slice yields rejected (nothing to judge).
func Evaluate(frames []FrameObservation, exp Expected, tol int) Evaluation {
	ev := Evaluation{FrameCount: len(frames)}
	if len(frames) == 0 {
		ev.Outcome = OutcomeRejected
		ev.Reason = "no frames"
		return ev
	}

	for _, f := range frames {
		if f.Soccer {
			ev.SoccerVotes++
		}
		if f.Screen {
			ev.ScreenVotes++
		}
	}
	majority := len(frames)/2 + 1

	// Gate 1: must be live match footage (majority vote).
	if ev.SoccerVotes < majority {
		ev.Outcome = OutcomeRejected
		ev.Reason = "not soccer (minority soccer vote)"
		return ev
	}
	// Gate 2: must not be a phone-of-TV recording (majority vote).
	if ev.ScreenVotes >= majority {
		ev.Outcome = OutcomeRejected
		ev.Reason = "screen recording (majority screen vote)"
		return ev
	}
	// A zero API minute means the provider did not supply time evidence. Keep
	// valid soccer footage in the unverified pool instead of comparing a visible
	// broadcast clock against a fabricated minute zero.
	if exp.Elapsed <= 0 {
		ev.Outcome = OutcomeUnverified
		ev.Reason = "API minute unavailable"
		return ev
	}

	// Clock check. API-Football reports the ordinal minute in play while the
	// broadcast clock shows completed minutes: a goal at 29:xx is reported as
	// 30'. Stoppage time follows the same rule (45+2' is 46:xx), so normalize
	// both forms before applying the configured tolerance.
	expectedMinute := exp.Elapsed + exp.Extra - 1
	expectedPeriod := periodOf(exp.Elapsed)
	expectedStoppage := exp.Extra > 0
	ev.ExpectedMinute = expectedMinute
	ev.ExpectedPeriod = expectedPeriod.String()

	readings := make([]ClockReading, 0, len(frames))
	for i, f := range frames {
		reading, ok := parseFrameClock(i, f)
		if !ok {
			continue
		}
		readings = append(readings, reading)
		// Retain the last legible reading for the compact summary. The complete
		// ordered set remains on ClockReadings for post-hoc diagnosis.
		detectedMinute := reading.Minute
		ev.DetectedMinute = &detectedMinute
		ev.DetectedPeriod = reading.Period.String()
	}
	ev.ClockReadings = readings

	softKeep := false
	for _, reading := range readings {
		if reading.Ambiguous {
			softKeep = true
			continue
		}
		periodPinned := reading.PeriodPinned || reading.Stoppage

		// Candidate 1 — direct: right period AND within ±tol.
		if reading.ExactMinute && reading.Period == expectedPeriod && abs(reading.Minute-expectedMinute) <= tol {
			return ev.verify(reading.Minute, "clock matches minute+period")
		}
		// Candidate 2 — frozen boundary, no sub-timer to pin the exact
		// stoppage minute, but the right period's stoppage: accept on
		// period alone (two goals in one stoppage window are ~never confused).
		if reading.Stoppage && !reading.ExactMinute && expectedStoppage && reading.Period == expectedPeriod {
			return ev.verify(reading.Minute, "frozen-boundary stoppage, period match (minute unpinned)")
		}
		// Candidate 3 — OCR leading-digit rebase: the model dropped the
		// leading digit of a stoppage clock ("92:36"→"02:36"); api elapsed
		// IS the dropped base. Only when the API expects stoppage and the
		// scorebug did not explicitly identify a conflicting period.
		if expectedStoppage && !reading.PeriodPinned {
			if corrected := exp.Elapsed + reading.Minute; abs(corrected-expectedMinute) <= tol {
				return ev.verify(corrected, "OCR leading-digit rebase")
			}
		}
		// Numeric matches but the period conflicts. If an extra-time period
		// is involved, the conflict may be a broadcast-rendering artifact →
		// don't drop; mark for soft-keep. A clean H1/H2 conflict falls
		// through to reject.
		if abs(reading.Minute-expectedMinute) <= tol && reading.Period != expectedPeriod {
			if (!periodPinned && isPeriodBoundary(reading.Minute)) ||
				isExtraTime(expectedPeriod) || isExtraTime(reading.Period) {
				softKeep = true
			}
		}
		// A bare low clock can be a reset-per-period display. The API expectation
		// may reveal that interpretation, but without a visible period it is not
		// strong enough to VERIFY the clip. Preserve it in the unverified pool.
		if !periodPinned && relativeClockCouldMatch(reading.Minute, expectedPeriod, expectedMinute, tol) {
			softKeep = true
		}
	}

	switch {
	case len(readings) == 0:
		ev.Outcome = OutcomeUnverified
		ev.Reason = "no clock visible"
	case softKeep:
		ev.Outcome = OutcomeUnverified
		ev.Reason = "clock interpretation ambiguous — kept, not clock-verified"
	default:
		ev.Outcome = OutcomeRejected
		ev.Reason = "clock present but does not match expected (wrong minute or wrong half)"
	}
	return ev
}

func isPeriodBoundary(minute int) bool {
	return minute == 45 || minute == 90 || minute == 105 || minute == 120
}

// relativeClockCouldMatch reports whether a clock with no period evidence has
// a plausible reset-per-period interpretation matching the expected event.
// It is deliberately a soft-keep signal only; API context cannot manufacture
// the visual evidence required for a verified verdict.
func relativeClockCouldMatch(displayed int, period Period, expected, tol int) bool {
	match := func(candidate int) bool { return abs(candidate-expected) <= tol }
	switch period {
	case PeriodSecondHalf:
		return displayed < 45 && match(45+displayed)
	case PeriodExtraFirst:
		return displayed <= 15 && match(90+displayed)
	case PeriodExtraSecond:
		return (displayed <= 15 && match(105+displayed)) ||
			(displayed <= 30 && match(90+displayed))
	default:
		return false
	}
}

// verify is a small helper to set the verified outcome + matched minute.
func (ev Evaluation) verify(minute int, reason string) Evaluation {
	ev.Outcome = OutcomeVerified
	ev.MatchedMinute = &minute
	ev.Reason = reason
	return ev
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
