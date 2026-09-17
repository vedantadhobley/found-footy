// schema_test.go — constrained-output contract and old-payload compatibility tests.
package vision

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResponseSchemaIsValidJSON(t *testing.T) {
	if !json.Valid(ResponseSchema) {
		t.Fatal("ResponseSchema is not valid JSON")
	}
}

// TestPromptRequiresLiteralIndependentEvidence protects the no-invented-seconds contract.
// This is a wording guard, not a substitute for the fixed-image model benchmark.
func TestPromptRequiresLiteralIndependentEvidence(t *testing.T) {
	for _, instruction := range []string{"Never invent seconds", "Do NOT turn an allowance", "Never copy missing digits", "Do NOT infer the period", "Unchanging text alone"} {
		if !strings.Contains(DefaultPrompt, instruction) {
			t.Errorf("missing prompt constraint: %s", instruction)
		}
	}
}

func TestVisionResponseMissingPeriodDefaultsToNil(t *testing.T) {
	const oldPayload = `{"frames":[{"soccer":true,"screen":false,"clock":"70:17","added":null,"stoppage_clock":null}]}`
	var response VisionResponse
	if err := json.Unmarshal([]byte(oldPayload), &response); err != nil {
		t.Fatalf("unmarshal old activity payload: %v", err)
	}
	if len(response.Frames) != 1 || response.Frames[0].Period != nil {
		t.Fatalf("old payload period = %+v, want nil", response.Frames)
	}
}
