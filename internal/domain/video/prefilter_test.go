// Tests for PreFilter — the pre-download aspect+duration cut. Reuses
// defaultThresholds() from filter_test.go (same package).
package video

import (
	"strings"
	"testing"
)

func TestPreFilter(t *testing.T) {
	th := defaultThresholds()

	// Landscape (incl. the 1.817 cluster) passes → download.
	if reason, ok := PreFilter(1170, 644, 11.5, th); !ok {
		t.Errorf("1.817 landscape should pass pre-filter, rejected %q", reason)
	}
	// FF-053: the live Elche sample at 1.739 must reach download; the exact
	// 1.730 boundary is admitted while the prior known-bad 1.72 band is not.
	if reason, ok := PreFilter(1739, 1000, 15.3, th); !ok {
		t.Errorf("1.739 Elche landscape should pass pre-filter, rejected %q", reason)
	}
	if reason, ok := PreFilter(1730, 1000, 15.3, th); !ok {
		t.Errorf("1.730 minimum boundary should pass pre-filter, rejected %q", reason)
	}
	if reason, ok := PreFilter(1729, 1000, 15.3, th); ok || !strings.Contains(reason, "aspect_too_narrow") {
		t.Errorf("1.729 below boundary: ok=%v reason=%q, want reject aspect_too_narrow", ok, reason)
	}
	if reason, ok := PreFilter(1600, 1000, 15.3, th); ok || !strings.Contains(reason, "aspect_too_narrow") {
		t.Errorf("16:10 letterbox: ok=%v reason=%q, want reject aspect_too_narrow", ok, reason)
	}
	// Portrait rejects without download.
	if reason, ok := PreFilter(720, 1280, 24.8, th); ok || !strings.Contains(reason, "aspect_too_narrow") {
		t.Errorf("portrait: ok=%v reason=%q, want reject aspect_too_narrow", ok, reason)
	}
	// Compilation duration rejects.
	if reason, ok := PreFilter(1280, 720, 200, th); ok || !strings.Contains(reason, "duration_too_long") {
		t.Errorf("200s: ok=%v reason=%q, want reject duration_too_long", ok, reason)
	}
	// Unknown dims → fall through to download (can't pre-judge).
	if _, ok := PreFilter(0, 0, 11, th); !ok {
		t.Error("unknown dims should fall through to download")
	}
	// Unknown duration but landscape → aspect still checked, passes.
	if _, ok := PreFilter(1280, 720, 0, th); !ok {
		t.Error("unknown duration + landscape should pass")
	}
}
