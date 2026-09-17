// Evidence-input regressions preserve baseline checks and the retained Nico boundary counterexample.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	vision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// TestRetainedNicoBoundary uses saved model output, not a fresh visual reading or human label.
func TestRetainedNicoBoundary(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"testdata/nico-boundary.ndjson"}, &output); err == nil || output.Len() != 0 {
		t.Fatal("v1 reason must not silently reproduce under v2")
	}
	if err := replayCurrent([]string{"testdata/nico-boundary.ndjson"}, &output); err != nil {
		t.Fatal(err)
	}
	var got currentReport
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Changes) != 1 || got.Changes[0].Recorded.Outcome != vision.OutcomeRejected || got.Changes[0].Current.Outcome != vision.OutcomeRejected {
		t.Fatalf("changes=%+v", got.Changes)
	}
}

// writeRows serializes test inputs into disposable files, never touching saved production evidence.
func writeRows(t *testing.T, rows ...row) string {
	t.Helper()
	var buffer bytes.Buffer
	for _, r := range rows {
		if err := json.NewEncoder(&buffer).Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "evidence.ndjson")
	if err := os.WriteFile(path, buffer.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestInputGuards rejects drift and malformed inputs before emitting a partial report.
func TestInputGuards(t *testing.T) {
	frames := repeated(observation("70:15", "", "", vision.PeriodSecondHalf))
	base := row{Source: "history", ID: "observation", EventID: "event", Frames: frames, Expected: vision.Expected{Elapsed: 71}, Tolerance: 1,
		Evaluation: vision.Evaluate(frames, vision.Expected{Elapsed: 71}, 1)}
	for _, c := range []struct {
		name   string
		modify func(*row)
	}{
		{"baseline reason", func(r *row) { r.Evaluation.Reason = "invented" }},
		{"matched minute", func(r *row) { v := 69; r.Evaluation.MatchedMinute = &v }},
		{"partial frames", func(r *row) { r.Frames = r.Frames[:2] }},
		{"unknown source", func(r *row) { r.Source = "unknown" }},
		{"missing identity", func(r *row) { r.EventID = "" }},
		{"unsupported evaluator", func(r *row) { r.Source = "accepted"; r.Origin.WorkflowID = "event"; r.Evaluator = "future" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := base
			c.modify(&r)
			var output bytes.Buffer
			if err := run([]string{writeRows(t, r)}, &output); err == nil || output.Len() != 0 {
				t.Fatalf("err=%v output=%s", err, &output)
			}
		})
	}
	var output bytes.Buffer
	if err := run([]string{writeRows(t, base, base)}, &output); err == nil {
		t.Fatal("duplicate evidence accepted")
	}
}

// TestExclusionsStayVisible distinguishes manual proof, missing history, and changed provider expectations.
func TestExclusionsStayVisible(t *testing.T) {
	minute := 45
	rows := []row{
		{Source: "accepted", ID: "manual", EventID: "event"},
		{Source: "history", ID: "missing", EventID: "event", Tolerance: 1},
		{Source: "rejected", ID: "no-expectation", EventID: "event"},
		{Source: "rejected", ID: "drift", EventID: "event", CurrentElapsed: 70},
	}
	rows[3].Detail.Frames = repeated(observation("90:00", "+2", "", vision.PeriodSecondHalf))
	rows[3].Detail.ExpectedMinute = &minute
	rows[3].Detail.ExpectedPeriod = "2H"
	var output bytes.Buffer
	if err := run([]string{writeRows(t, rows...)}, &output); err != nil {
		t.Fatal(err)
	}
	var got report
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"manual_evaluations_excluded", "history_missing_frames_excluded", "reject_without_clock_expectation_excluded", "changed_expectation_excluded"} {
		if got.Counts[key] != 1 {
			t.Fatalf("%s=%d", key, got.Counts[key])
		}
	}
}
