// response.go strictly decodes the current model wire shape while adapting it
// to the durable observation type that also reads historical period payloads.
package vision

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

type modelFrameObservation struct {
	Soccer        bool    `json:"soccer"`
	Screen        bool    `json:"screen"`
	Clock         *string `json:"clock"`
	PeriodLabel   *string `json:"period_label"`
	Added         *string `json:"added"`
	StoppageClock *string `json:"stoppage_clock"`
}

// DecodeResponse rejects fields outside the current constrained-output
// contract. In particular, a model cannot smuggle the retired normalized
// `period` field back into new evidence.
func DecodeResponse(data []byte) (VisionResponse, error) {
	var wire struct {
		Frames []modelFrameObservation `json:"frames"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return VisionResponse{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return VisionResponse{}, fmt.Errorf("vision response contains multiple JSON values")
		}
		return VisionResponse{}, err
	}
	response := VisionResponse{Frames: make([]FrameObservation, len(wire.Frames))}
	for i, frame := range wire.Frames {
		response.Frames[i] = FrameObservation{
			Soccer: frame.Soccer, Screen: frame.Screen, Clock: frame.Clock,
			PeriodLabel: frame.PeriodLabel, Added: frame.Added, StoppageClock: frame.StoppageClock,
		}
	}
	return response, nil
}
