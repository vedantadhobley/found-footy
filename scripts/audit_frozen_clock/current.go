// Current-evaluator replay compares immutable saved verdicts without recreating a legacy runtime.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"

	vision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// currentCase keeps recorded and current interpretations separate, including reason-only changes.
type currentCase struct {
	Source   string
	ID       string
	EventID  string
	AssetID  string
	Player   string
	Expected vision.Expected
	Frames   []vision.FrameObservation
	Recorded vision.Evaluation
	Current  vision.Evaluation
}

// currentReport does not project changed acceptance through an un-replayed perceptual graph.
type currentReport struct {
	Evaluator          string
	Sources            map[string]string
	Counts             map[string]int
	Transitions        map[string]int
	Changes            []currentCase
	AffectedLiveShares []row
	Limit              string
}

// replayCurrent compares the saved verdict with the actual current implementation.
// It does not claim to reproduce the old evaluator, relabel footage, or mutate history.
func replayCurrent(paths []string, output io.Writer) error {
	rows, sources, err := readRows(paths)
	if err != nil {
		return err
	}
	out := currentReport{Evaluator: vision.EvaluatorVersion, Sources: sources,
		Counts: map[string]int{}, Transitions: map[string]int{},
		Limit: "Saved model outputs are not new visual labels. Rejected rows lack the complete original evaluation; current event time is used only when it reproduces the retained expectation. No public graph/support projection or historical repair is performed. History may overlap candidate rows; counts are not unique videos."}
	changedAssets := map[string]bool{}
	for _, r := range rows {
		out.Counts[r.Source+"_rows"]++
		if r.Source == "snapshot" || r.Source == "share" {
			continue
		}
		if r.Source == "accepted" && r.Origin.WorkflowID == "" {
			out.Counts["manual_evaluations_excluded"]++
			continue
		}
		frames, exp, tolerance, recorded := r.Frames, r.Expected, r.Tolerance, r.Evaluation
		if r.Source == "accepted" && r.Evaluator != "vision-evaluate-v1" && r.Evaluator != "vision-evaluate-v2" && r.Evaluator != vision.EvaluatorVersion {
			return fmt.Errorf("%s: unknown saved evaluator %q", r.ID, r.Evaluator)
		}
		if r.Source == "rejected" {
			frames, exp, tolerance = r.Detail.Frames, vision.Expected{Elapsed: r.CurrentElapsed, Extra: r.CurrentExtra}, 1
			if r.Detail.ExpectedMinute == nil {
				out.Counts["reject_without_clock_expectation_excluded"]++
				continue
			}
			recorded = vision.Evaluation{Outcome: vision.OutcomeRejected, Reason: r.Reason,
				ExpectedMinute: *r.Detail.ExpectedMinute, ExpectedPeriod: r.Detail.ExpectedPeriod}
		}
		if r.Source == "history" && len(frames) == 0 {
			out.Counts["history_missing_frames_excluded"]++
			continue
		}
		if len(frames) != 3 || tolerance < 0 || exp.Extra < 0 || recorded.Reason == "" {
			return fmt.Errorf("%s: incomplete evidence", r.ID)
		}
		switch recorded.Outcome {
		case vision.OutcomeVerified:
			if recorded.MatchedMinute == nil {
				return fmt.Errorf("%s: verified evidence lacks minute", r.ID)
			}
		case vision.OutcomeRejected, vision.OutcomeUnverified:
			if recorded.MatchedMinute != nil {
				return fmt.Errorf("%s: nonverified evidence claims minute", r.ID)
			}
		default:
			return fmt.Errorf("%s: unknown recorded outcome", r.ID)
		}
		after := vision.Evaluate(frames, exp, tolerance)
		if r.Source == "rejected" && (after.ExpectedMinute != recorded.ExpectedMinute || after.ExpectedPeriod != recorded.ExpectedPeriod) {
			out.Counts["changed_expectation_excluded"]++
			continue
		}
		out.Counts[r.Source+"_compared"]++
		out.Transitions[r.Source+"/"+string(recorded.Outcome)+"->"+string(after.Outcome)]++
		if recorded.Outcome != after.Outcome || recorded.Reason != after.Reason || !reflect.DeepEqual(recorded.MatchedMinute, after.MatchedMinute) {
			out.Changes = append(out.Changes, currentCase{r.Source, r.ID, r.EventID, r.AssetID, r.Player, exp, frames, recorded, after})
		}
		if recorded.Outcome != after.Outcome && r.AssetID != "" {
			changedAssets[r.AssetID] = true
		}
	}
	for _, r := range rows {
		if r.Source == "share" && changedAssets[r.AssetID] {
			out.AffectedLiveShares = append(out.AffectedLiveShares, r)
		}
	}
	enc := json.NewEncoder(output)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
