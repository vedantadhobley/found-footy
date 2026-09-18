// Temperature experiment tests preserve omission, explicit zero and bounds.
package main

import (
	"testing"
)

// TestTemperatureOverride validates omission, exact zero and unsupported inputs.
func TestTemperatureOverride(t *testing.T) {
	for _, raw := range []string{"NaN", "Inf", "-1", "2.1", "abc"} {
		if _, err := temperatureOverride(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	if got, err := temperatureOverride(""); err != nil || got != nil {
		t.Fatalf("omission=%v %v", got, err)
	}
	if got, err := temperatureOverride("0"); err != nil || got == nil || *got != 0 {
		t.Fatalf("zero=%v %v", got, err)
	}
}
