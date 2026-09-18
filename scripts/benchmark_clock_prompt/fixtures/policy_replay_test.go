// Replay archived model responses through the current policy without new inference or image access.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"testing"

	vision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// TestRecordedPeriodResponses pins the v3 offset removal and v4 running-stoppage
// correction against the original v2 experiment without rewriting its archive.
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
	archiveHash := sha256.New()
	decoder := json.NewDecoder(io.TeeReader(file, archiveHash))
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
		want := saved.Evaluation
		if saved.Name == "yamal_first_half_penalty_miss" || saved.Name == "no_half_label" {
			if saved.Evaluation.Outcome != vision.OutcomeUnverified || current.Outcome != vision.OutcomeRejected {
				t.Fatalf("%s: expected unverified -> rejected, got %s -> %s", key, saved.Evaluation.Outcome, current.Outcome)
			}
			changed++
			// All evidence fields must survive the narrower acceptance rule.
			current.Outcome, current.Reason = saved.Evaluation.Outcome, saved.Evaluation.Reason
		}
		if saved.Name == "maitland_running_plus_five" {
			if saved.Evaluation.Outcome != vision.OutcomeUnverified {
				t.Fatalf("%s: archive no longer records the original period failure", key)
			}
			minute := 95
			want.Outcome, want.MatchedMinute = vision.OutcomeVerified, &minute
			want.Reason, want.DetectedPeriod = "clock matches minute+period", "2H"
			want.ClockReadings = append([]vision.ClockReading(nil), saved.Evaluation.ClockReadings...)
			for i := range want.ClockReadings {
				want.ClockReadings[i].Period = vision.PeriodSecondHalf
				want.ClockReadings[i].PeriodPinned, want.ClockReadings[i].Stoppage = true, true
			}
			changed++
		}
		if !reflect.DeepEqual(current, want) {
			t.Fatalf("unexpected evaluation change in %s: expected=%+v current=%+v", key, want, current)
		}
	}
	// New generated controls do not change the frozen September 17 corpus.
	if fmt.Sprintf("%x", archiveHash.Sum(nil)) != "7ee5ed47a05b370dbc7b639e2914f2f43df5bda1b9db837284f619b11e6a3c21" {
		t.Fatal("historical model-response archive changed")
	}
	if len(seen) != 52 || changed != 6 || headers != 4 || completions != 4 {
		t.Fatalf("incomplete replay: results=%d changed=%d headers=%d completions=%d", len(seen), changed, headers, completions)
	}
}
