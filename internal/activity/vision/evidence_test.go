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

// TestValidateClipCapturesOwnEvidence separates capture from unchanged clock admission.
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

// TestCapturePreservesUnverifiedAndRejectedOutcomes pins the unchanged evaluator
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
