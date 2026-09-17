// Known incident and boundary controls for the offline tolerance experiment.
package main

import (
	"testing"

	dvision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// clockFrame constructs a control observation, not an additional historical sample.
func clockFrame(clock string, period dvision.Period, added string) dvision.FrameObservation {
	f := dvision.FrameObservation{Soccer: true}
	if clock != "" {
		f.Clock = &clock
	}
	if period != "" {
		f.Period = &period
	}
	if added != "" {
		f.Added = &added
	}
	return f
}

// controlInput uses production evaluation only to create a baseline for parser
// tests. The known-example test separately asserts both expected verdicts.
func controlInput(frames []dvision.FrameObservation, expected dvision.Expected) historyInput {
	ev := dvision.Evaluate(frames, expected, 1)
	return historyInput{
		Source: "synthetic", RunID: "run", Context: eventContext{EventID: "event", FixtureID: 1}, Scheduled: 1,
		Observations: []savedObservation{{
			ScheduleID: "2",
			Input:      visionInput{EventID: "event", FixtureID: 1, StagingKey: "staging/control.mp4", APIElapsed: &expected.Elapsed, APIExtra: &expected.Extra},
			Output:     visionOutput{Outcome: ev.Outcome, Reason: ev.Reason, MatchedMinute: ev.MatchedMinute, Frames: frames},
		}},
	}
}

// TestKnownClockCases keeps user-reviewed counterexamples distinct from synthetic
// boundary controls; none of these constructed observations inflate the corpus.
func TestKnownClockCases(t *testing.T) {
	frames := func(clocks ...string) []dvision.FrameObservation {
		var out []dvision.FrameObservation
		for _, clock := range clocks {
			out = append(out, clockFrame(clock, "", ""))
		}
		return out
	}
	frozen := clockFrame("90:00", dvision.PeriodSecondHalf, "+5")
	reset := clockFrame("05:25", dvision.PeriodSecondHalf, "")
	boundary := clockFrame("45:25", dvision.PeriodSecondHalf, "")
	lens := frozen
	stoppage := "02:44"
	lens.StoppageClock = &stoppage
	for _, tc := range []struct {
		name     string
		frames   []dvision.FrameObservation
		expected dvision.Expected
		one      dvision.Outcome
		zero     dvision.Outcome
	}{
		{"mastantuono_first_goal_in_second_event", frames("27:59", "28:03", "28:08"), dvision.Expected{Elapsed: 30}, dvision.OutcomeVerified, dvision.OutcomeRejected},
		{"mastantuono_second_goal", frames("29:36", "29:39", "29:42"), dvision.Expected{Elapsed: 30}, dvision.OutcomeVerified, dvision.OutcomeVerified},
		{"mastantuono_two_goal_bridge", frames("28:04", "29:39", "29:42"), dvision.Expected{Elapsed: 30}, dvision.OutcomeVerified, dvision.OutcomeVerified},
		// One user-reported clock replicated as a regression, not three saved OCR readings.
		{"abdelkarim_known_lower_edge", frames("28:56", "28:56", "28:56"), dvision.Expected{Elapsed: 30}, dvision.OutcomeVerified, dvision.OutcomeRejected},
		{"lens_compilation_single_exact_clock", []dvision.FrameObservation{clockFrame("", "", ""), lens, clockFrame("", "", "")}, dvision.Expected{Elapsed: 90, Extra: 3}, dvision.OutcomeVerified, dvision.OutcomeVerified},
		{"frozen_stoppage_has_no_exact_minute", []dvision.FrameObservation{frozen, frozen, frozen}, dvision.Expected{Elapsed: 90, Extra: 3}, dvision.OutcomeUnverified, dvision.OutcomeUnverified},
		{"no_clock", frames("", "", ""), dvision.Expected{Elapsed: 30}, dvision.OutcomeUnverified, dvision.OutcomeUnverified},
		{"wrong_half", []dvision.FrameObservation{clockFrame("28:01", dvision.PeriodFirstHalf, ""), clockFrame("28:02", dvision.PeriodFirstHalf, ""), clockFrame("28:03", dvision.PeriodFirstHalf, "")}, dvision.Expected{Elapsed: 74}, dvision.OutcomeRejected, dvision.OutcomeRejected},
		{"reset_second_half", []dvision.FrameObservation{reset, reset, reset}, dvision.Expected{Elapsed: 51}, dvision.OutcomeVerified, dvision.OutcomeVerified},
		{"unsupported_reset_without_period", frames("05:25", "05:26", "05:27"), dvision.Expected{Elapsed: 51}, dvision.OutcomeRejected, dvision.OutcomeRejected},
		{"exact_second_half_boundary", []dvision.FrameObservation{boundary, boundary, boundary}, dvision.Expected{Elapsed: 90, Extra: 1}, dvision.OutcomeVerified, dvision.OutcomeVerified},
		{"ocr_rebase_still_passes", frames("02:44", "02:45", "02:46"), dvision.Expected{Elapsed: 90, Extra: 3}, dvision.OutcomeVerified, dvision.OutcomeVerified},
		{"first_minute", frames("00:20", "00:25", "00:30"), dvision.Expected{Elapsed: 1}, dvision.OutcomeVerified, dvision.OutcomeVerified},
		{"unknown_api_time", frames("28:01", "28:02", "28:03"), dvision.Expected{}, dvision.OutcomeUnverified, dvision.OutcomeUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := compare([]historyInput{controlInput(tc.frames, tc.expected)})
			if err != nil {
				t.Fatal(err)
			}
			got := result.Events[0].Observations[0]
			if got.Baseline.Outcome != tc.one || got.Strict.Outcome != tc.zero {
				t.Fatalf("got %s -> %s, want %s -> %s", got.Baseline.Outcome, got.Strict.Outcome, tc.one, tc.zero)
			}
		})
	}
}

// TestComparisonEvidenceGuards rejects drift and avoids counting absent OCR as rejection.
func TestComparisonEvidenceGuards(t *testing.T) {
	newInput := func() historyInput {
		f := clockFrame("29:30", "", "")
		return controlInput([]dvision.FrameObservation{f, f, f}, dvision.Expected{Elapsed: 30})
	}
	t.Run("identical_exports_count_once", func(t *testing.T) {
		a, b := newInput(), newInput()
		b.Source = "second-export"
		out, err := compare([]historyInput{a, b})
		if err != nil || out.Evaluated != 1 || out.DuplicateFiles != 1 || len(out.Sources) != 2 {
			t.Fatalf("result=%+v err=%v", out, err)
		}
	})
	t.Run("conflicting_exports_fail", func(t *testing.T) {
		a, b := newInput(), newInput()
		b.Scheduled++
		if _, err := compare([]historyInput{a, b}); err == nil {
			t.Fatal("accepted conflicting run snapshots")
		}
	})
	t.Run("baseline_drift_fails", func(t *testing.T) {
		a := newInput()
		a.Observations[0].Output.Reason = "different recorded policy"
		if _, err := compare([]historyInput{a}); err == nil {
			t.Fatal("accepted baseline drift")
		}
	})
	t.Run("missing_frames_are_not_invented", func(t *testing.T) {
		a := newInput()
		a.Observations[0].Output.Frames = nil
		out, err := compare([]historyInput{a})
		if err != nil || out.MissingFrames != 1 || out.Evaluated != 0 {
			t.Fatalf("result=%+v err=%v", out, err)
		}
	})
	t.Run("partial_frame_sets_fail", func(t *testing.T) {
		a := newInput()
		a.Observations[0].Output.Frames = a.Observations[0].Output.Frames[:1]
		if _, err := compare([]historyInput{a}); err == nil {
			t.Fatal("accepted partial frame set")
		}
	})
}
