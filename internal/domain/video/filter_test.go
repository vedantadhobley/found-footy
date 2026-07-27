// Tests for the hard-filter, driven by the real Dybala/Chiesa clip
// dimensions we sampled 2026-07-27 — so the thresholds are validated
// against actual goal-clip aspect distributions, not invented numbers.
package video

import (
	"strings"
	"testing"
)

func defaultThresholds() FilterThresholds {
	return FilterThresholds{
		MinDurationSecs: 3, MaxDurationSecs: 90,
		MinAspectRatio: 1.75, MaxAspectRatio: 1.82,
		MinShortEdge: 600, MinFrameRate: 20,
	}
}

func TestHardFilter(t *testing.T) {
	th := defaultThresholds()
	cases := []struct {
		name           string
		w, h           int
		dur, fps       float64
		wantOK         bool
		reasonContains string
	}{
		// Real landscape goal clips — must pass (the 1.817 pass is why we
		// loosened the max from 1.80).
		{"dybala_1.817", 1170, 644, 11.5, 25, true, ""},
		{"dybala_1.800", 1170, 650, 5.1, 25, true, ""},
		{"clean_16:9", 1280, 720, 6.2, 25, true, ""},
		// Portrait / social — reject (narrow).
		{"vertical_9:16", 720, 1280, 24.8, 30, false, "aspect_too_narrow"},
		{"instagram_4:5", 1080, 1350, 23.3, 30, false, "aspect_too_narrow"},
		{"square", 720, 720, 25.8, 30, false, "aspect_too_narrow"},
		// Over-cropped ultrawide — reject (wide).
		{"ultrawide_2.336", 1280, 548, 23.2, 25, false, "aspect_too_wide"},
		{"lowres_1.839", 640, 348, 6.9, 25, false, "aspect_too_wide"},
		// Duration bounds.
		{"too_short", 1280, 720, 2.0, 25, false, "duration_too_short"},
		{"compilation", 1280, 720, 120, 25, false, "duration_too_long"},
		// Framerate + short edge.
		{"low_fps", 1280, 720, 6, 12, false, "framerate_too_low"},
		{"tiny_16:9", 800, 450, 6, 25, false, "short_edge_too_small"},
		// Degenerate.
		{"zero_dims", 0, 0, 6, 25, false, "invalid_dimensions"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, ok := HardFilter(c.w, c.h, c.dur, c.fps, th)
			if ok != c.wantOK {
				t.Fatalf("HardFilter(%dx%d %.1fs %.0ffps) = ok:%v reason:%q, want ok:%v",
					c.w, c.h, c.dur, c.fps, ok, reason, c.wantOK)
			}
			if !c.wantOK && c.reasonContains != "" && !strings.Contains(reason, c.reasonContains) {
				t.Errorf("reason = %q, want contains %q", reason, c.reasonContains)
			}
		})
	}
}
