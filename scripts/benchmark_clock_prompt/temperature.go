// Benchmark-only temperature injection preserves the adapter's request while supporting explicit zero.
package main

import (
	"fmt"
	"math"
	"strconv"
)

// temperatureOverride distinguishes omission from zero and rejects values outside the verified catalog.
func temperatureOverride(raw string) (*float32, error) {
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseFloat(raw, 32)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 2 {
		return nil, fmt.Errorf("temperature must be omitted or finite within [0,2]")
	}
	result := float32(value)
	return &result, nil
}
