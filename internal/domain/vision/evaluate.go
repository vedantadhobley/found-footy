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

	// Clock check. The API reports the minute AFTER the goal, so expected =
	// elapsed + extra - 1 (never below elapsed).
	expectedMinute := exp.Elapsed + exp.Extra - 1
	if expectedMinute < exp.Elapsed {
		expectedMinute = exp.Elapsed
	}
	expectedPeriod := periodOf(exp.Elapsed)
	expectedStoppage := exp.Extra > 0
	ev.ExpectedMinute = expectedMinute
	ev.ExpectedPeriod = expectedPeriod.String()

	sawClock := false
	softKeep := false // numeric-matched but period conflict at an ET boundary

	for _, f := range frames {
		if f.Clock == nil {
			continue
		}
		base, ok := parseClockField(*f.Clock)
		if !ok {
			continue
		}
		sawClock = true

		stopMin, hasStop := 0, false
		if f.StoppageClock != nil {
			if s, sok := parseStoppageClockField(*f.StoppageClock); sok {
				stopMin, hasStop = s, true
			}
		}
		hasAdded := false
		if f.Added != nil {
			if _, aok := parseAddedField(*f.Added); aok {
				hasAdded = true
			}
		}
		frozen := hasStop || hasAdded
		absolute := base
		if hasStop {
			absolute = base + stopMin
		}
		clipPeriod := periodOf(base)
		// Retain the read clock (last legible one) so a clock-reject records
		// what the OCR saw, not just that it mismatched (#181).
		detectedMinute := absolute
		ev.DetectedMinute = &detectedMinute
		ev.DetectedPeriod = clipPeriod.String()

		// Candidate 1 — direct: right period AND within ±tol.
		if clipPeriod == expectedPeriod && abs(absolute-expectedMinute) <= tol {
			return ev.verify(absolute, "clock matches minute+period")
		}
		// Candidate 2 — frozen boundary, no sub-timer to pin the exact
		// stoppage minute, but the right period's stoppage: accept on
		// period alone (two goals in one stoppage window are ~never confused).
		if frozen && !hasStop && expectedStoppage && clipPeriod == expectedPeriod {
			return ev.verify(base, "frozen-boundary stoppage, period match (minute unpinned)")
		}
		// Candidate 3 — OCR leading-digit rebase: the model dropped the
		// leading digit of a stoppage clock ("92:36"→"02:36"); api elapsed
		// IS the dropped base. Only when the API expects stoppage.
		if expectedStoppage {
			if corrected := exp.Elapsed + absolute; abs(corrected-expectedMinute) <= tol {
				return ev.verify(corrected, "OCR leading-digit rebase")
			}
		}
		// Numeric matches but the period conflicts. If an extra-time period
		// is involved, the conflict may be a broadcast-rendering artifact →
		// don't drop; mark for soft-keep. A clean H1/H2 conflict falls
		// through to reject.
		if abs(absolute-expectedMinute) <= tol && clipPeriod != expectedPeriod {
			if isExtraTime(expectedPeriod) || isExtraTime(clipPeriod) {
				softKeep = true
			}
		}
	}

	switch {
	case !sawClock:
		ev.Outcome = OutcomeUnverified
		ev.Reason = "no clock visible"
	case softKeep:
		ev.Outcome = OutcomeUnverified
		ev.Reason = "clock numeric-matches but extra-time period ambiguous — kept, not clock-verified"
	default:
		ev.Outcome = OutcomeRejected
		ev.Reason = "clock present but does not match expected (wrong minute or wrong half)"
	}
	return ev
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
