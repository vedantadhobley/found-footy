// Versioned replay must retain recorded outcomes rather than normalize them into the new policy.
package main

import (
	"bytes"
	"encoding/json"
	"testing"

	vision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// TestCurrentReplayRetainsOriginalVerdict uses a constructed v1 allowance-bypass record.
func TestCurrentReplayRetainsOriginalVerdict(t *testing.T) {
	minute := 90
	r := row{Source: "accepted", ID: "proof", EventID: "event", AssetID: "asset", Evaluator: "vision-evaluate-v1", Tolerance: 1,
		Expected: vision.Expected{Elapsed: 90, Extra: 4}, Frames: repeated(observation("90:37", "+4", "", vision.PeriodSecondHalf)),
		Evaluation: vision.Evaluation{Outcome: vision.OutcomeVerified, MatchedMinute: &minute, Reason: "frozen-boundary stoppage, period match (minute unpinned)"}}
	r.Origin.WorkflowID = "event"
	path := writeRows(t, r, row{Source: "share", ID: "share", EventID: "event", AssetID: "asset"})
	var first, second bytes.Buffer
	if err := replayCurrent([]string{path}, &first); err != nil {
		t.Fatal(err)
	}
	if err := replayCurrent([]string{path}, &second); err != nil || first.String() != second.String() {
		t.Fatal("nonrepeatable report")
	}
	var out currentReport
	if err := json.Unmarshal(first.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Changes) != 1 || out.Changes[0].Recorded.Outcome != vision.OutcomeVerified || out.Changes[0].Current.Outcome != vision.OutcomeRejected || len(out.AffectedLiveShares) != 1 {
		t.Fatalf("report=%+v", out)
	}
	first.Reset()
	if err := replayCurrent([]string{path, path + "-missing"}, &first); err == nil || first.Len() != 0 {
		t.Fatal("partial report on missing evidence")
	}
}

// TestCurrentReplayRetainsV2OffsetVerdict preserves the old soft acceptance,
// permits its explicit replay and still rejects unknown evaluator versions.
func TestCurrentReplayRetainsV2OffsetVerdict(t *testing.T) {
	r := row{Source: "accepted", ID: "proof", EventID: "event", AssetID: "asset", Evaluator: "vision-evaluate-v2", Tolerance: 1,
		Expected: vision.Expected{Elapsed: 89}, Frames: repeated(observation("44:51", "", "", "")),
		Evaluation: vision.Evaluation{Outcome: vision.OutcomeUnverified, Reason: "clock interpretation ambiguous — kept, not clock-verified"}}
	r.Origin.WorkflowID = "event"
	var output bytes.Buffer
	if err := replayCurrent([]string{writeRows(t, r)}, &output); err != nil {
		t.Fatal(err)
	}
	var report currentReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Changes) != 1 || report.Changes[0].Recorded.Outcome != vision.OutcomeUnverified || report.Changes[0].Current.Outcome != vision.OutcomeRejected {
		t.Fatalf("report=%+v", report)
	}
	r.Evaluator = "unknown-policy"
	output.Reset()
	if err := replayCurrent([]string{writeRows(t, r)}, &output); err == nil || output.Len() != 0 {
		t.Fatal("unknown evaluator produced a report")
	}
}

// TestCurrentReplayRetainsV3StoppageVerdict admits old evidence without relabelling it.
func TestCurrentReplayRetainsV3StoppageVerdict(t *testing.T) {
	r := row{Source: "accepted", ID: "proof", EventID: "event", AssetID: "asset", Evaluator: "vision-evaluate-v3", Tolerance: 1,
		Expected: vision.Expected{Elapsed: 90, Extra: 6}, Frames: repeated(observation("95:15", "+5", "", "")),
		Evaluation: vision.Evaluation{Outcome: vision.OutcomeUnverified, Reason: "clock interpretation ambiguous — kept, not clock-verified"}}
	r.Origin.WorkflowID = "event"
	var output bytes.Buffer
	if err := replayCurrent([]string{writeRows(t, r)}, &output); err != nil {
		t.Fatal(err)
	}
	var report currentReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Changes) != 1 || report.Changes[0].Recorded.Outcome != vision.OutcomeUnverified || report.Changes[0].Current.Outcome != vision.OutcomeVerified {
		t.Fatalf("report=%+v", report)
	}
}
