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
	for _, instruction := range []string{"Never invent seconds", "Do NOT turn an allowance", "Never copy missing digits", "Do NOT infer a label", "Unchanging text alone"} {
		if !strings.Contains(DefaultPrompt, instruction) {
			t.Errorf("missing prompt constraint: %s", instruction)
		}
	}
}

// TestPromptSeparatesPeriodLabelsFromAllowances pins wording, not model accuracy.
func TestPromptSeparatesPeriodLabelsFromAllowances(t *testing.T) {
	var periodLine string
	for _, line := range strings.Split(DefaultPrompt, "\n") {
		if strings.HasPrefix(line, "- period_label:") {
			periodLine = line
		}
	}
	for _, instruction := range []string{
		"copy the literal text", "exactly as printed", "Do not translate or normalize",
		"Do NOT infer a label", "Keep a printed plus/minute-mark allowance in added",
		"superscript suffix", "retain its letters", "never supply missing letters",
	} {
		if !strings.Contains(periodLine, instruction) {
			t.Errorf("missing period-role guard: %s", instruction)
		}
	}
}

func TestVisionResponseMissingPeriodDefaultsToNil(t *testing.T) {
	const oldPayload = `{"frames":[{"soccer":true,"screen":false,"clock":"70:17","added":null,"stoppage_clock":null}]}`
	var response VisionResponse
	if err := json.Unmarshal([]byte(oldPayload), &response); err != nil {
		t.Fatalf("unmarshal old activity payload: %v", err)
	}
	if len(response.Frames) != 1 || response.Frames[0].Period != nil || response.Frames[0].PeriodLabel != nil {
		t.Fatalf("old payload period = %+v, want nil", response.Frames)
	}
}

func TestPeriodFromLabelIsLiteralAndConservative(t *testing.T) {
	for input, want := range map[string]Period{
		"1st": PeriodFirstHalf, "1re": PeriodFirstHalf, "1P": PeriodFirstHalf,
		"1ˢᵗ": PeriodFirstHalf, "HALF 1": PeriodFirstHalf,
		"2nd": PeriodSecondHalf, "2e": PeriodSecondHalf, "2p": PeriodSecondHalf,
		"2ⁿᵈ": PeriodSecondHalf, "half 2": PeriodSecondHalf,
		"ET 1": PeriodExtraFirst, "ET2": PeriodExtraSecond,
	} {
		got, ok := PeriodFromLabel(input)
		if !ok || got != want {
			t.Fatalf("%q: got %q/%t want %q", input, got, ok, want)
		}
	}
	for _, input := range []string{"", "1", "2", "+1", "2′", "45:00", "ADDED TIME 2", "ET", "2²"} {
		if got, ok := PeriodFromLabel(input); ok || got != PeriodUnknown {
			t.Fatalf("%q became period evidence: %q/%t", input, got, ok)
		}
	}
}

func TestDecodeResponseRetainsLiteralLabelAndRejectsLegacyWireField(t *testing.T) {
	response, err := DecodeResponse([]byte(`{"frames":[{"soccer":true,"screen":false,"clock":"05:25","period_label":"2P","added":null,"stoppage_clock":null}]}`))
	if err != nil || len(response.Frames) != 1 || response.Frames[0].Period != nil ||
		response.Frames[0].PeriodLabel == nil || *response.Frames[0].PeriodLabel != "2P" {
		t.Fatalf("literal response = %+v, %v", response, err)
	}
	_, err = DecodeResponse([]byte(`{"frames":[{"soccer":true,"screen":false,"clock":"05:25","period":"2H","added":null,"stoppage_clock":null}]}`))
	if err == nil {
		t.Fatal("accepted retired normalized period field from new model wire")
	}
}
