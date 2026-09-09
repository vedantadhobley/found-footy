// measure_test.go — Controlled repeat, noise, motion, overlay, and timing boundaries.
package main

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"testing"
)

// TestRepeatPatternsRequireSustainedMotion pins supported periods without claiming source FPS.
func TestRepeatPatternsRequireSustainedMotion(t *testing.T) {
	for _, factor := range []int{2, 3, 4, 5, 6} {
		for phase := 0; phase < factor; phase++ {
			t.Run(fmt.Sprintf("factor_%d_phase_%d", factor, phase), func(t *testing.T) {
				samples := changes(120, func(i int) float64 {
					if i%factor == phase {
						return 3 + float64(i%7)/10
					}
					return 0.08 + float64(i%3)/100 // re-encoding noise, not byte equality
				})
				got := measureWindow(samples, 60)
				if got.Classification != periodicRepeats || got.RepeatFactor != factor {
					t.Fatalf("factor %d: %+v", factor, got)
				}
			})
		}
	}
}

// TestAmbiguityStaysVisible prevents static, cut-only, and irregular footage from receiving an FPS claim.
func TestAmbiguityStaysVisible(t *testing.T) {
	cases := []struct {
		name string
		data []transition
		want classification
	}{
		{"changing_each_frame", changes(120, func(i int) float64 { return 2 + float64(i%7)/10 }), noPattern},
		{"interpolated_changes", changes(120, func(int) float64 { return 2 }), noPattern},
		{"static", changes(120, func(int) float64 { return 0 }), lowMotion},
		{"slow_motion", changes(120, func(i int) float64 { return .1 + float64(i%7)/100 }), lowMotion},
		{"cut_then_static", changes(120, func(i int) float64 {
			if i == 60 {
				return 60
			}
			return 0
		}), lowMotion},
		{"short", changes(24, func(int) float64 { return 3 }), shortWindow},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := measureWindow(item.data, 60); got.Classification != item.want || got.RepeatFactor != 0 {
				t.Fatalf("got %+v, want %s without repeat factor", got, item.want)
			}
		})
	}
	for _, delta := range []float64{0, -.01, .1} {
		samples := changes(120, func(int) float64 { return 3 })
		samples[20].delta = delta
		if got := measureWindow(samples, 60); got.Classification != irregularTiming {
			t.Fatalf("bad PTS delta %v: %+v", delta, got)
		}
	}
}

// TestMixedCadenceIsNotAveragedAway preserves a changing pattern and an inconclusive tail.
func TestMixedCadenceIsNotAveragedAway(t *testing.T) {
	samples := changes(263, func(i int) float64 {
		if i < 120 && i%2 == 0 {
			return .05
		}
		return 3
	})
	got := analyze(samples, 60)
	if len(got) != 3 || got[0].RepeatFactor != 2 || got[1].Classification != noPattern ||
		got[2].Classification != shortWindow {
		t.Fatalf("unexpected mixed result: %+v", got)
	}
}

// TestEdgeOverlayDoesNotEstablishFieldMotion pins the crop's limited purpose.
func TestEdgeOverlayDoesNotEstablishFieldMotion(t *testing.T) {
	previous, current := make([]byte, frameBytes), make([]byte, frameBytes)
	for i := 0; i < frameWidth*20; i++ {
		current[i] = 255
	}
	full, center := frameDifference(previous, current)
	if full <= 0 || center != 0 {
		t.Fatalf("edge-only motion full=%f center=%f", full, center)
	}
	current[frameWidth*70+160] = 255
	_, center = frameDifference(previous, current)
	if center <= 0 {
		t.Fatal("center motion was not measured")
	}
}

// TestFrameReaderRejectsIncompleteEvidence prevents partial decodes from looking successful.
func TestFrameReaderRejectsIncompleteEvidence(t *testing.T) {
	var probe probeData
	probe.Frames = append(probe.Frames, struct {
		Timestamp string `json:"best_effort_timestamp_time"`
	}{"0"},
		struct {
			Timestamp string `json:"best_effort_timestamp_time"`
		}{"0.016667"})
	for _, size := range []int{0, frameBytes - 1, frameBytes, 3 * frameBytes} {
		if _, _, err := readFrames(bytes.NewReader(make([]byte, size)), probe); err == nil {
			t.Fatalf("accepted %d raw bytes for two frames", size)
		}
	}
	if samples, count, err := readFrames(bytes.NewReader(make([]byte, 2*frameBytes)), probe); err != nil ||
		count != 2 || len(samples) != 1 {
		t.Fatalf("complete input: %d frames, %d samples, %v", count, len(samples), err)
	}
	probe.Frames[1].Timestamp = "N/A"
	if _, _, err := readFrames(bytes.NewReader(make([]byte, 2*frameBytes)), probe); err == nil {
		t.Fatal("accepted unknown PTS")
	}
}

// TestFrameRateAndOutputBounds pins the CLI's finite resource assumptions.
func TestFrameRateAndOutputBounds(t *testing.T) {
	for _, value := range []string{"", "0/0", "60/0", "NaN/1", "Inf/1", "60", "-30/1", "300/1"} {
		if _, err := frameRate(value); err == nil {
			t.Fatalf("accepted rate %q", value)
		}
	}
	if rate, err := frameRate("60000/1001"); err != nil || math.Abs(rate-59.94006) > .00001 {
		t.Fatalf("fractional rate=%f err=%v", rate, err)
	}
	buffer := &boundedBuffer{limit: 5}
	_, _ = buffer.Write([]byte("abcd"))
	if n, _ := buffer.Write([]byte("efgh")); n != 4 || buffer.String() != "abcde" || !buffer.truncated {
		t.Fatalf("bad bounded output: %+v", buffer)
	}
	// io.Copy must not discover an uncapped promoted bytes.Buffer.ReadFrom method.
	if _, ok := any(buffer).(io.ReaderFrom); ok {
		t.Fatal("bounded output exposes an uncapped ReaderFrom path")
	}
}

// changes builds known-time scalar evidence without depending on a codec.
func changes(count int, value func(int) float64) []transition {
	result := make([]transition, count)
	for i := range result {
		v := value(i)
		result[i] = transition{seconds: float64(i+1) / 60, delta: 1.0 / 60, center: v, full: v}
	}
	return result
}
