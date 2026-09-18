// Literal-label experiments separate model transcription from deterministic period interpretation.
package main

import (
	"encoding/json"
	"fmt"
	"strings"

	vision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// literalPeriodPrompt replaces only the normalized-period task with literal transcription.
func literalPeriodPrompt() (string, error) {
	for _, line := range strings.Split(vision.DefaultPrompt, "\n") {
		if strings.HasPrefix(line, "- period_label:") {
			return vision.DefaultPrompt, nil
		}
		if strings.HasPrefix(line, "- period:") {
			return "", fmt.Errorf("runtime still uses inferred period; literal experiment must be reviewed")
		}
	}
	return "", fmt.Errorf("period instruction missing; review benchmark")
}

// literalPeriodSchema leaves all existing fields intact except the model's period output.
func literalPeriodSchema() (json.RawMessage, error) {
	var schema map[string]any
	if err := json.Unmarshal(vision.ResponseSchema, &schema); err != nil {
		return nil, err
	}
	frames := schema["properties"].(map[string]any)["frames"].(map[string]any)
	item := frames["items"].(map[string]any)
	properties := item["properties"].(map[string]any)
	if properties["period_label"] == nil || properties["period"] != nil {
		return nil, fmt.Errorf("runtime schema is not literal-period contract")
	}
	return json.Marshal(schema)
}

// legacyPeriodSchema retains the normalized-period wire shape for rerunning
// the two frozen pre-promotion prompt experiments only.
func legacyPeriodSchema() (json.RawMessage, error) {
	var schema map[string]any
	if err := json.Unmarshal(vision.ResponseSchema, &schema); err != nil {
		return nil, err
	}
	frames := schema["properties"].(map[string]any)["frames"].(map[string]any)
	item := frames["items"].(map[string]any)
	properties := item["properties"].(map[string]any)
	delete(properties, "period_label")
	properties["period"] = map[string]any{
		"type": []any{"string", "null"},
		"enum": []any{"1H", "2H", "ET1", "ET2", nil},
	}
	for i, key := range item["required"].([]any) {
		if key == "period_label" {
			item["required"].([]any)[i] = "period"
		}
	}
	return json.Marshal(schema)
}

// normalizeLiteralPeriod accepts explicit aliases, never a lone number or time allowance.
func normalizeLiteralPeriod(label *string) *vision.Period {
	if label == nil {
		return nil
	}
	period, ok := vision.PeriodFromLabel(*label)
	if !ok {
		return nil
	}
	return &period
}

// decodeLiteralPeriod retains raw wire evidence separately while adapting only the period field.
func decodeLiteralPeriod(content string) (vision.VisionResponse, error) {
	return vision.DecodeResponse([]byte(content))
}
