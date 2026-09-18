// Capture tests verify actual sampling/model provenance without making a real model call.
package vision

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	dvision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// TestValidateClipCapturesOwnEvidence separates optional capture from clock admission.
func TestValidateClipCapturesOwnEvidence(t *testing.T) {
	response := framesJSON(
		map[string]any{"soccer": true, "clock": "22:10"},
		map[string]any{"soccer": true, "clock": "22:11"},
		map[string]any{"soccer": true, "clock": "22:12"},
	)
	a, ff, model := newActivities(t, response, nil)
	model.model = "actual-returned-model"
	a.Cfg.Prompt = "test effective prompt"
	in := ValidateClipInput{EventID: uuid.New(), FixtureID: 1, StagingKey: "clip",
		APIElapsed: 23, MD5: strings.Repeat("ab", 16), CaptureEvidence: true}
	out, err := a.ValidateClip(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	proof := out.Evidence
	if out.Evaluator != dvision.EvaluatorVersion || proof.Evaluator != out.Evaluator {
		t.Fatal("evaluation version lost across activity/evidence boundary")
	}
	if err := proof.Validate(); err != nil {
		t.Fatal(err)
	}
	if proof.EventID != in.EventID || proof.MD5 != in.MD5 || proof.Expected.Elapsed != 23 || proof.Model != model.model ||
		proof.PromptSHA256 != contractDigest(a.Cfg.Prompt) || proof.SchemaSHA256 != contractDigest(string(dvision.ResponseSchema)) ||
		!reflect.DeepEqual(proof.FramePositions, ff.positions) || !reflect.DeepEqual(proof.Frames, out.Frames) ||
		proof.Evaluation.Outcome != dvision.OutcomeVerified || proof.Evaluation.MatchedMinute == nil || *proof.Evaluation.MatchedMinute != 22 {
		t.Fatalf("lost actual validation inputs: %+v", proof)
	}
	in.CaptureEvidence, in.MD5 = false, ""
	legacy, err := a.ValidateClip(context.Background(), in)
	if err != nil || legacy.Evidence != nil || legacy.Outcome != out.Outcome {
		t.Fatalf("legacy changed: %+v %v", legacy, err)
	}
	encoded, err := json.Marshal(legacy)
	if err != nil || strings.Contains(string(encoded), "Evidence") {
		t.Fatal("legacy payload acquired evidence field")
	}
}

// TestCapturePreservesUnverifiedAndRejectedOutcomes pins the capture/evaluator
// boundary, including partial model responses and Temporal-origin provenance.
func TestCapturePreservesUnverifiedAndRejectedOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, outcome, response string
	}{
		{"unverified", "unverified", framesJSON(
			map[string]any{"soccer": true}, map[string]any{"soccer": true}, map[string]any{"soccer": true})},
		{"partial", "unverified", framesJSON(
			map[string]any{"soccer": true}, map[string]any{"soccer": true})},
		{"rejected", "rejected", framesJSON(
			map[string]any{"soccer": false}, map[string]any{"soccer": false}, map[string]any{"soccer": false})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := newActivities(t, tc.response, nil)
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestActivityEnvironment()
			env.RegisterActivity(a)
			value, err := env.ExecuteActivity(a.ValidateClip, ValidateClipInput{EventID: uuid.New(), FixtureID: 1,
				StagingKey: "clip", APIElapsed: 23, CaptureEvidence: true, MD5: strings.Repeat("ab", 16)})
			require.NoError(t, err)
			var out ValidateClipOutput
			require.NoError(t, value.Get(&out))
			require.Equal(t, tc.outcome, out.Outcome)
			require.Equal(t, dvision.EvaluatorVersion, out.Evaluator)
			if tc.outcome == "rejected" {
				require.Nil(t, out.Evidence)
				return
			}
			require.NoError(t, out.Evidence.Validate())
			require.Nil(t, out.Evidence.Evaluation.MatchedMinute)
			require.Empty(t, out.Evidence.Model, "a server-omitted model ID remains unknown")
			require.NotEmpty(t, out.Evidence.Origin.ActivityID)
			require.Positive(t, out.Evidence.Origin.Attempt)
		})
	}
}

// TestLegacyVerdictDoesNotAcquireEvaluator preserves recorded Temporal result semantics.
func TestLegacyVerdictDoesNotAcquireEvaluator(t *testing.T) {
	var out ValidateClipOutput
	require.NoError(t, json.Unmarshal([]byte(`{"Outcome":"rejected","Reason":"old reason"}`), &out))
	require.Empty(t, out.Evaluator)
	encoded, err := json.Marshal(out)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "Evaluator")
}

// TestRunningStoppageActivityRetainsRawEvidence reproduces Gueye at the activity boundary.
func TestRunningStoppageActivityRetainsRawEvidence(t *testing.T) {
	response := framesJSON(
		map[string]any{"soccer": true, "clock": "46:35", "added": "+4"},
		map[string]any{"soccer": true, "clock": "46:37", "added": "+4"},
		map[string]any{"soccer": true, "clock": "46:39", "added": "+4"},
	)
	a, _, _ := newActivities(t, response, nil)
	out, err := a.ValidateClip(context.Background(), ValidateClipInput{
		EventID: uuid.MustParse("5956a72d-75cc-4f7e-bf54-bdf1afc83cd7"), FixtureID: 1570390,
		StagingKey: "clip", APIElapsed: 45, APIExtra: 2,
		CaptureEvidence: true, MD5: strings.Repeat("ab", 16),
	})
	require.NoError(t, err)
	require.Equal(t, "verified", out.Outcome)
	require.Equal(t, "vision-evaluate-v4", out.Evaluator)
	require.NotNil(t, out.MatchedMinute)
	require.Equal(t, 46, *out.MatchedMinute)
	require.NoError(t, out.Evidence.Validate())
	for i, reading := range out.ClockReadings {
		require.Equal(t, dvision.PeriodFirstHalf, reading.Period)
		require.Nil(t, out.Frames[i].Period, "inference must not rewrite the model observation")
		require.Equal(t, "+4", *out.Frames[i].Added)
	}
	require.Equal(t, out.Frames, out.Evidence.Frames)
	require.Equal(t, out.ClockReadings, out.Evidence.Evaluation.ClockReadings)
}
