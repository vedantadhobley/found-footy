// Reuse the production evaluator at tolerance one and zero without changing it.
package main

import (
	"fmt"
	"reflect"
	"sort"

	dvision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

type comparison struct {
	Recorded   visionOutput               `json:"recorded"`
	ScheduleID string                     `json:"schedule_id"`
	StagingKey string                     `json:"staging_key"`
	Expected   dvision.Expected           `json:"expected"`
	Frames     []dvision.FrameObservation `json:"frames"`
	Baseline   dvision.Evaluation         `json:"tolerance_one"`
	Strict     dvision.Evaluation         `json:"tolerance_zero"`
}

type eventReport struct {
	Context             eventContext   `json:"context"`
	RunID               string         `json:"run_id"`
	Scheduled           int            `json:"scheduled"`
	Completed           int            `json:"completed"`
	MissingFrames       int            `json:"missing_frames"`
	Transitions         map[string]int `json:"transitions"`
	VerifiedReasonsOne  map[string]int `json:"verified_reasons_one"`
	VerifiedReasonsZero map[string]int `json:"verified_reasons_zero"`
	Observations        []comparison   `json:"observations"`
}

type source struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type report struct {
	Evaluator      string         `json:"evaluator"`
	PolicyChanges  int            `json:"policy_changes"`
	Contract       string         `json:"contract"`
	Sources        []source       `json:"sources"`
	DuplicateFiles int            `json:"duplicate_files"`
	Evaluated      int            `json:"evaluated"`
	MissingFrames  int            `json:"missing_frames"`
	Transitions    map[string]int `json:"transitions"`
	Events         []eventReport  `json:"events"`
}

// compare requires exact replay of each historical verdict before attributing
// any difference to tolerance. Identical run exports count once; drift fails.
func compare(inputs []historyInput) (report, error) {
	return comparePolicy(inputs, false)
}

// comparePolicy explicitly separates policy drift from the current tolerance experiment.
// Recorded observations remain immutable; callers must opt into mixed-version evidence.
func comparePolicy(inputs []historyInput, allowPolicyChanges bool) (report, error) {
	out := report{Contract: "clock-tolerance-v2", Evaluator: dvision.EvaluatorVersion, Transitions: make(map[string]int)}
	runs := make(map[string]historyInput)
	for _, input := range inputs {
		out.Sources = append(out.Sources, source{input.Source, input.SHA256})
		identity := input.Context.EventID + "/" + input.RunID
		if previous, exists := runs[identity]; exists {
			previous.Source, input.Source = "", ""
			previous.SHA256, input.SHA256 = "", ""
			if !reflect.DeepEqual(previous, input) {
				return out, fmt.Errorf("conflicting exports of run %s", identity)
			}
			out.DuplicateFiles++
			continue
		}
		runs[identity] = input
		event := eventReport{
			Context: input.Context, RunID: input.RunID, Scheduled: input.Scheduled,
			Completed: len(input.Observations), Transitions: make(map[string]int),
			VerifiedReasonsOne: make(map[string]int), VerifiedReasonsZero: make(map[string]int),
		}
		for _, observation := range input.Observations {
			if len(observation.Output.Frames) == 0 {
				event.MissingFrames++
				out.MissingFrames++
				continue
			}
			if len(observation.Output.Frames) != 3 {
				return out, fmt.Errorf("%s/%s: expected three saved frames", identity, observation.ScheduleID)
			}
			expected := dvision.Expected{Elapsed: *observation.Input.APIElapsed, Extra: *observation.Input.APIExtra}
			baseline := dvision.Evaluate(observation.Output.Frames, expected, 1)
			if baseline.Outcome != observation.Output.Outcome || baseline.Reason != observation.Output.Reason ||
				!reflect.DeepEqual(baseline.MatchedMinute, observation.Output.MatchedMinute) {
				if !allowPolicyChanges {
					return out, fmt.Errorf("%s/%s: recorded verdict does not reproduce at tolerance one; use --replay-current to report policy changes separately", identity, observation.ScheduleID)
				}
				out.PolicyChanges++
			}
			strict := dvision.Evaluate(observation.Output.Frames, expected, 0)
			transition := string(baseline.Outcome) + " -> " + string(strict.Outcome)
			event.Transitions[transition]++
			out.Transitions[transition]++
			out.Evaluated++
			if baseline.Outcome == dvision.OutcomeVerified {
				event.VerifiedReasonsOne[baseline.Reason]++
			}
			if strict.Outcome == dvision.OutcomeVerified {
				event.VerifiedReasonsZero[strict.Reason]++
			}
			event.Observations = append(event.Observations, comparison{
				Recorded:   observation.Output,
				ScheduleID: observation.ScheduleID, StagingKey: observation.Input.StagingKey,
				Expected: expected, Frames: observation.Output.Frames, Baseline: baseline, Strict: strict,
			})
		}
		out.Events = append(out.Events, event)
	}
	sort.Slice(out.Events, func(i, j int) bool {
		return out.Events[i].Context.EventID+out.Events[i].RunID < out.Events[j].Context.EventID+out.Events[j].RunID
	})
	return out, nil
}
