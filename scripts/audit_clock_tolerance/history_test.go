// Temporal export decoding and no-partial-report checks for the offline audit.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// testPayload reproduces Temporal's base64 json/plain envelope.
func testPayload(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"payloads": []any{map[string]any{
		"metadata": map[string]string{"encoding": base64.StdEncoding.EncodeToString([]byte("json/plain"))},
		"data":     base64.StdEncoding.EncodeToString(data),
	}}}
}

// testHistory constructs a minimal export with distinct schedule/completion IDs.
func testHistory(t *testing.T) []map[string]any {
	t.Helper()
	return []map[string]any{
		{"eventId": "1", "workflowExecutionStartedEventAttributes": map[string]any{
			"originalExecutionRunId": "run", "workflowId": "event-event", "workflowType": map[string]string{"name": "EventWorkflow"},
			"input": testPayload(t, eventContext{EventID: "event", FixtureID: 1}),
		}},
		{"eventId": "2", "activityTaskScheduledEventAttributes": map[string]any{
			"activityType": map[string]string{"name": "ValidateClip"},
			"input":        testPayload(t, map[string]any{"EventID": "event", "FixtureID": 1, "StagingKey": "staging/clip.mp4", "APIElapsed": 30, "APIExtra": 0}),
		}},
		{"eventId": "3", "activityTaskCompletedEventAttributes": map[string]any{
			"scheduledEventId": "2", "result": testPayload(t, map[string]any{"Outcome": "verified", "Reason": "clock matches minute+period", "MatchedMinute": 29,
				"Frames": []any{clockFrame("29:10", "", ""), clockFrame("29:11", "", ""), clockFrame("29:12", "", "")}}),
		}},
	}
}

// marshalHistory keeps test evidence construction errors distinct from decoder errors.
func marshalHistory(t *testing.T, events []map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"events": events})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestHistoryDecode verifies identity joins, unsupported encodings, and missing input.
func TestHistoryDecode(t *testing.T) {
	input, err := decodeHistory(marshalHistory(t, testHistory(t)))
	if err != nil || input.Scheduled != 1 || len(input.Observations) != 1 || *input.Observations[0].Input.APIElapsed != 30 {
		t.Fatalf("input=%+v err=%v", input, err)
	}
	for _, tc := range []struct {
		name   string
		mutate func([]map[string]any)
	}{
		{"duplicate_id", func(es []map[string]any) { es[2]["eventId"] = "2" }},
		{"wrong_workflow", func(es []map[string]any) {
			es[0]["workflowExecutionStartedEventAttributes"].(map[string]any)["workflowId"] = "event-other"
		}},
		{"missing_clock_input", func(es []map[string]any) {
			es[1]["activityTaskScheduledEventAttributes"].(map[string]any)["input"] = testPayload(t, map[string]any{"EventID": "event", "FixtureID": 1, "StagingKey": "key"})
		}},
		{"missing_payload", func(es []map[string]any) {
			es[2]["activityTaskCompletedEventAttributes"].(map[string]any)["result"] = map[string]any{}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			es := testHistory(t)
			tc.mutate(es)
			if _, err := decodeHistory(marshalHistory(t, es)); err == nil {
				t.Fatal("accepted invalid history")
			}
		})
	}
	t.Run("unfinished_activity_not_counted_as_rejection", func(t *testing.T) {
		input, err := decodeHistory(marshalHistory(t, testHistory(t)[:2]))
		if err != nil || input.Scheduled != 1 || len(input.Observations) != 0 {
			t.Fatalf("input=%+v err=%v", input, err)
		}
	})
}

// TestRunIsOfflineAndAtomic checks repeatable file reads and no partial output on error.
func TestRunIsOfflineAndAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	if err := os.WriteFile(path, marshalHistory(t, testHistory(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	var first, second bytes.Buffer
	if err := run([]string{path}, &first); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{path}, &second); err != nil || first.String() != second.String() {
		t.Fatalf("non-repeatable report: %v", err)
	}
	first.Reset()
	if err := run([]string{path, path + "-missing"}, &first); err == nil || first.Len() != 0 {
		t.Fatal("missing evidence must not emit a partial report")
	}
	if err := run(nil, &first); err == nil {
		t.Fatal("accepted empty input")
	}
}

// TestSavedObservationCorpus freezes the sanitized historical sample separately
// from constructed boundary controls and from unreviewed semantic judgments.
func TestSavedObservationCorpus(t *testing.T) {
	data, err := os.ReadFile("testdata/recorded-histories.json")
	if err != nil {
		t.Fatal(err)
	}
	var inputs []historyInput
	if err := json.Unmarshal(data, &inputs); err != nil {
		t.Fatal(err)
	}
	if _, err := compare(inputs); err == nil {
		t.Fatal("v1 corpus must not silently reproduce under v2")
	}
	out, err := comparePolicy(inputs, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Events) != 13 || out.Evaluated != 108 || out.MissingFrames != 0 || out.DuplicateFiles != 2 {
		t.Fatalf("corpus inventory drift: %+v", out)
	}
	if out.PolicyChanges != 1 {
		t.Fatalf("policy changes=%d, want Wober only", out.PolicyChanges)
	}
	want := map[string]int{"verified -> verified": 36, "verified -> rejected": 4, "unverified -> unverified": 5, "rejected -> rejected": 63}
	if len(out.Transitions) != len(want) {
		t.Fatalf("unexpected transition classes: %v", out.Transitions)
	}
	for transition, count := range want {
		if out.Transitions[transition] != count {
			t.Fatalf("%s: got %d, want %d", transition, out.Transitions[transition], count)
		}
	}
}
