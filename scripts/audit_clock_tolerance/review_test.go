// Clock-only controls from agent-reviewed original media, not recovered model responses.
package main

import (
	"testing"

	dvision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// TestVisuallyReviewedClockCases records the eight preselected ordinary clips.
// These readings were made from regenerated 25/50/75% frames. Soccer/screen
// votes are neutral controls; neither historical OCR nor semantic correctness
// is reconstructed. The adjacent audit records identities and visual evidence.
func TestVisuallyReviewedClockCases(t *testing.T) {
	for _, tc := range []struct {
		name    string
		elapsed int
		clocks  [3]string
		one     dvision.Outcome
		zero    dvision.Outcome
	}{
		{"neto_14_non_goal_sequence", 14, [3]string{"12:01", "12:07", "12:12"}, dvision.OutcomeVerified, dvision.OutcomeRejected},
		{"farrington_27_unresolved_stored_reading", 27, [3]string{"29:53", "", ""}, dvision.OutcomeRejected, dvision.OutcomeRejected},
		{"sancet_90_exact_later_frame", 90, [3]string{"88:58", "89:06", "89:13"}, dvision.OutcomeVerified, dvision.OutcomeVerified},
		{"gibbs_white_70_penalty_award_not_kick", 70, [3]string{"67:53", "68:08", "68:22"}, dvision.OutcomeVerified, dvision.OutcomeRejected},
		// Main timer only. The separate +1 announcement has its own parser
		// characterization below; its historical model field is unavailable.
		{"fernandez_45_goal_replays_main_timer", 45, [3]string{"45:17", "45:25", "45:33"}, dvision.OutcomeVerified, dvision.OutcomeRejected},
		{"isak_9_goal_replays", 9, [3]string{"09:14", "09:22", "09:29"}, dvision.OutcomeVerified, dvision.OutcomeRejected},
		{"bischof_24_goal_action", 24, [3]string{"24:14", "24:15", "24:17"}, dvision.OutcomeVerified, dvision.OutcomeRejected},
		{"havertz_25_sideline_reaction", 25, [3]string{"25:15", "25:18", "25:21"}, dvision.OutcomeVerified, dvision.OutcomeRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frames := make([]dvision.FrameObservation, 0, len(tc.clocks))
			for _, clock := range tc.clocks {
				frames = append(frames, clockFrame(clock, "", ""))
			}
			expected := dvision.Expected{Elapsed: tc.elapsed}
			one := dvision.Evaluate(frames, expected, 1)
			zero := dvision.Evaluate(frames, expected, 0)
			if one.Outcome != tc.one || zero.Outcome != tc.zero {
				t.Fatalf("got %s -> %s, want %s -> %s", one.Outcome, zero.Outcome, tc.one, tc.zero)
			}
		})
	}
}

// TestRunningTimerWithAddedAnnouncement guards the FF-094 repair against the original control.
// The 45+4 target is a constructed negative control, not a historical event.
func TestRunningTimerWithAddedAnnouncement(t *testing.T) {
	frames := []dvision.FrameObservation{
		clockFrame("45:17", "", "+1"),
		clockFrame("45:25", "", "+1"),
		clockFrame("45:33", "", "+1"),
	}
	for _, tolerance := range []int{1, 0} {
		normal := dvision.Evaluate(frames, dvision.Expected{Elapsed: 45}, tolerance)
		want := dvision.OutcomeRejected
		if tolerance == 1 {
			want = dvision.OutcomeVerified
		}
		if normal.Outcome != want {
			t.Fatalf("tol %d: normal-time result: %+v", tolerance, normal)
		}
		wrongStoppage := dvision.Evaluate(frames, dvision.Expected{Elapsed: 45, Extra: 4}, tolerance)
		if wrongStoppage.Outcome != dvision.OutcomeRejected {
			t.Fatalf("tol %d: unpinned-minute bypass returned: %+v", tolerance, wrongStoppage)
		}
		for _, reading := range wrongStoppage.ClockReadings {
			if !reading.ExactMinute {
				t.Fatalf("tol %d: lost exact-minute evidence: %+v", tolerance, reading)
			}
		}
	}
}
