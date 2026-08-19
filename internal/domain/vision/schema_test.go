// schema_test.go — constrained-output contract and old-payload compatibility tests.
package vision

import (
	"encoding/json"
	"testing"
)

func TestResponseSchemaIsValidJSON(t *testing.T) {
	if !json.Valid(ResponseSchema) {
		t.Fatal("ResponseSchema is not valid JSON")
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
