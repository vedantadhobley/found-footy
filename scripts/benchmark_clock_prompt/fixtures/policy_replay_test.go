// Replay archived model responses through the current policy without new inference or image access.
package main

import (
	"encoding/json"
	"io"
	"os"
	"reflect"
	"testing"

	vision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// TestRecordedPeriodResponses isolates the v3 policy change from the original
// v2 prompt experiment. Controls are not counted as natural-video evidence.
func TestRecordedPeriodResponses(t *testing.T) {
	expected := map[string]vision.Expected{}
	for _, c := range controls() {
		expected[c.Name] = c.Expected
	}
	natural, err := os.ReadFile("natural-20260917.json")
	if err != nil {
		t.Fatal(err)
	}
	var samples []struct {
		Name     string
		Expected vision.Expected
	}
	if err := json.Unmarshal(natural, &samples); err != nil {
		t.Fatal(err)
	}
	for _, s := range samples {
		expected[s.Name] = s.Expected
	}
	file, err := os.Open("../testdata/period-label-results-20260917.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	seen := map[string]bool{}
	changed, headers, completions := 0, 0, 0
	for {
		var saved struct {
			Evaluator  string
			Complete   bool
			Name       string
			Variant    string
			Error      string
			Response   vision.VisionResponse
			Evaluation vision.Evaluation
		}
		if err := decoder.Decode(&saved); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if saved.Name == "" {
			switch {
			case saved.Evaluator == "vision-evaluate-v2":
				headers++
			case saved.Complete:
				completions++
			default:
				t.Fatal("unrecognized archive record")
			}
			continue
		}
		exp, known := expected[saved.Name]
		key := saved.Name + "/" + saved.Variant
		if !known || seen[key] || saved.Error != "" || len(saved.Response.Frames) != 3 ||
			(saved.Variant != "default" && saved.Variant != "period_roles") {
			t.Fatalf("invalid or repeated saved result: %s", key)
		}
		seen[key] = true
		current := vision.Evaluate(saved.Response.Frames, exp, 1)
		if saved.Name == "yamal_first_half_penalty_miss" || saved.Name == "no_half_label" {
			if saved.Evaluation.Outcome != vision.OutcomeUnverified || current.Outcome != vision.OutcomeRejected {
				t.Fatalf("%s: expected unverified -> rejected, got %s -> %s", key, saved.Evaluation.Outcome, current.Outcome)
			}
			changed++
			// All evidence fields must survive the narrower acceptance rule.
			current.Outcome, current.Reason = saved.Evaluation.Outcome, saved.Evaluation.Reason
		}
		if !reflect.DeepEqual(current, saved.Evaluation) {
			t.Fatalf("unexpected evaluation change in %s: recorded=%+v current=%+v", key, saved.Evaluation, current)
		}
	}
	if len(seen) != len(expected)*2 || len(seen) != 52 || changed != 4 || headers != 4 || completions != 4 {
		t.Fatalf("incomplete replay: results=%d changed=%d headers=%d completions=%d", len(seen), changed, headers, completions)
	}
}
