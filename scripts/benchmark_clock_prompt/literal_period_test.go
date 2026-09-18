// Literal-period controls protect the experimental boundary without changing runtime parsing.
package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	vision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// TestLiteralPeriodAliases excludes time-derived labels and preserves explicit half/extra-time aliases.
func TestLiteralPeriodAliases(t *testing.T) {
	for input, want := range map[string]vision.Period{
		"1st": vision.PeriodFirstHalf, "1re": vision.PeriodFirstHalf, "1P": vision.PeriodFirstHalf,
		"1ˢᵗ": vision.PeriodFirstHalf, "HALF 1": vision.PeriodFirstHalf,
		"2nd": vision.PeriodSecondHalf, "2e": vision.PeriodSecondHalf, "2p": vision.PeriodSecondHalf,
		"2ⁿᵈ": vision.PeriodSecondHalf, "half 2": vision.PeriodSecondHalf,
		"ET 1": vision.PeriodExtraFirst, "ET2": vision.PeriodExtraSecond,
		"": "", "1": "", "2": "", "+1": "", "2′": "", "45:00": "", "ADDED TIME 2": "", "ET": "",
	} {
		got := normalizeLiteralPeriod(&input)
		if want == "" && got != nil || want != "" && (got == nil || *got != want) {
			t.Fatalf("%q: got %v want %q", input, got, want)
		}
	}
	if normalizeLiteralPeriod(nil) != nil {
		t.Fatal("absent label became period evidence")
	}
}

// TestLiteralPeriodContract follows the promoted runtime contract exactly.
func TestLiteralPeriodContract(t *testing.T) {
	raw, err := literalPeriodSchema()
	if err != nil {
		t.Fatal(err)
	}
	var after map[string]any
	if err := json.Unmarshal(raw, &after); err != nil {
		t.Fatal(err)
	}
	item := after["properties"].(map[string]any)["frames"].(map[string]any)["items"].(map[string]any)
	properties := item["properties"].(map[string]any)
	if _, exists := properties["period"]; exists || properties["period_label"] == nil {
		t.Fatal("literal field did not replace canonical field")
	}
	var current map[string]any
	if err := json.Unmarshal(vision.ResponseSchema, &current); err != nil || !reflect.DeepEqual(current, after) {
		t.Fatal("literal benchmark schema differs from runtime")
	}
	names, prompts, err := comparisonPrompts("period-literal")
	if err != nil || len(names) != 1 || len(prompts) != 1 || prompts[0] != vision.DefaultPrompt || !strings.Contains(prompts[0], "- period_label:") {
		t.Fatalf("bad literal comparison: %v %v", names, err)
	}
}

// TestLiteralPeriodDecode refuses canonical-field leakage and never promotes an isolated digit.
func TestLiteralPeriodDecode(t *testing.T) {
	response, err := decodeLiteralPeriod(`{"frames":[{"soccer":true,"screen":false,"clock":"05:25","period_label":"2","added":null,"stoppage_clock":null}]}`)
	if err != nil || len(response.Frames) != 1 || response.Frames[0].Period != nil ||
		response.Frames[0].PeriodLabel == nil || *response.Frames[0].PeriodLabel != "2" {
		t.Fatalf("bare digit promoted: %+v %v", response, err)
	}
	if _, err := decodeLiteralPeriod(`{"frames":[{"period":"2H"}]}`); err == nil {
		t.Fatal("accepted unrequested inferred period")
	}
}
