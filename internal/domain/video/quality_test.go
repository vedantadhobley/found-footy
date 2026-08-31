// quality_test.go — dedup winner-selection scoring: capped-relative duration
// (Python's 15% band, flattened past 60s) → spatial bitrate density → resolution.
package video

import "testing"

func bp(v int) *int { return &v }

func TestIsUpgrade(t *testing.T) {
	tests := []struct {
		name                  string
		challenger, incumbent ClipQuality
		want                  bool
	}{
		{
			// Meaningfully longer (>15%) wins on completeness before quality is
			// even weighed — a 30s clip beats a 4s pristine one.
			name:       "meaningfully longer wins (30s over 4s)",
			challenger: ClipQuality{DurationMS: 30000, Bitrate: bp(2_000_000), Width: 1280, Height: 720},
			incumbent:  ClipQuality{DurationMS: 4000, Bitrate: bp(9_000_000), Width: 1920, Height: 1080},
			want:       true,
		},
		{
			name:       "shorter never upgrades (4s vs 30s)",
			challenger: ClipQuality{DurationMS: 4000, Bitrate: bp(9_000_000), Width: 1920, Height: 1080},
			incumbent:  ClipQuality{DurationMS: 30000, Bitrate: bp(2_000_000), Width: 1280, Height: 720},
			want:       false,
		},
		{
			// The cap: both clamp to 60s → same length → length gives no edge →
			// equal quality means no upgrade (a padded 90s ties a 65s).
			name:       "past 60s cap, length goes flat (90s vs 65s, equal quality)",
			challenger: ClipQuality{DurationMS: 90000, Bitrate: bp(3_000_000), Width: 1280, Height: 720},
			incumbent:  ClipQuality{DurationMS: 65000, Bitrate: bp(3_000_000), Width: 1280, Height: 720},
			want:       false,
		},
		{
			// Length is primary within the cap: 90s→60 vs 40s is >15% apart, so
			// the longer clip wins even though it's lower quality.
			name:       "longer within cap wins on completeness (90s vs 40s)",
			challenger: ClipQuality{DurationMS: 90000, Bitrate: bp(1_000_000), Width: 1280, Height: 720},
			incumbent:  ClipQuality{DurationMS: 40000, Bitrate: bp(9_000_000), Width: 1920, Height: 1080},
			want:       true,
		},
		{
			// 50s→50 vs 70s→60 is ~17% apart → the longer (capped) clip wins.
			name:       "50s loses to 70s (capped 50 vs 60, beyond band)",
			challenger: ClipQuality{DurationMS: 50000, Bitrate: bp(9_000_000), Width: 1920, Height: 1080},
			incumbent:  ClipQuality{DurationMS: 70000, Bitrate: bp(1_000_000), Width: 1280, Height: 720},
			want:       false,
		},
		{
			name:       "same length → denser encode wins",
			challenger: ClipQuality{DurationMS: 20000, Bitrate: bp(5_000_000), Width: 1280, Height: 720},
			incumbent:  ClipQuality{DurationMS: 20000, Bitrate: bp(2_000_000), Width: 1280, Height: 720},
			want:       true,
		},
		{
			// Python's blind spot: same length, upscaled 1080p (low density)
			// loses to a denser 720p despite more pixels.
			name:       "same length → upscaled hi-res low-density loses to denser lower-res",
			challenger: ClipQuality{DurationMS: 20000, Bitrate: bp(1_500_000), Width: 1920, Height: 1080}, // ~0.72 bit/(px·s)
			incumbent:  ClipQuality{DurationMS: 20000, Bitrate: bp(3_000_000), Width: 1280, Height: 720},  // ~3.26 bit/(px·s)
			want:       false,
		},
		{
			// Same length, same density band → resolution breaks the tie.
			name:       "same length + density → more pixels wins",
			challenger: ClipQuality{DurationMS: 20000, Bitrate: bp(4_000_000), Width: 1920, Height: 1080}, // ~1.93 bit/(px·s)
			incumbent:  ClipQuality{DurationMS: 20000, Bitrate: bp(1_780_000), Width: 1280, Height: 720},  // ~1.93 bit/(px·s)
			want:       true,
		},
		{
			name:       "unknown bitrate falls through to resolution",
			challenger: ClipQuality{DurationMS: 20000, Bitrate: nil, Width: 1920, Height: 1080},
			incumbent:  ClipQuality{DurationMS: 20000, Bitrate: nil, Width: 1280, Height: 720},
			want:       true,
		},
		{
			name:       "identical → no upgrade (stability)",
			challenger: ClipQuality{DurationMS: 20000, Bitrate: bp(3_000_000), Width: 1280, Height: 720},
			incumbent:  ClipQuality{DurationMS: 20000, Bitrate: bp(3_000_000), Width: 1280, Height: 720},
			want:       false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsUpgrade(tt.challenger, tt.incumbent); got != tt.want {
				t.Errorf("IsUpgrade = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestIsUpgrade_DansoProductionCycle preserves FF-081's production evidence:
// the current thresholded pairwise policy is not a total order. B beats A on
// density, C beats B on duration, and A beats C on density. A future cluster
// comparator must make the keeper independent of arrival/match order without
// losing the completeness preference this case exposed.
func TestIsUpgrade_DansoProductionCycle(t *testing.T) {
	a := ClipQuality{DurationMS: 8_900, Bitrate: bp(1_308_000), Width: 1280, Height: 720}
	b := ClipQuality{DurationMS: 8_400, Bitrate: bp(1_475_000), Width: 1280, Height: 720}
	c := ClipQuality{DurationMS: 10_100, Bitrate: bp(960_000), Width: 1280, Height: 720}

	if !IsUpgrade(b, a) {
		t.Fatal("B should beat A on encoding density")
	}
	if !IsUpgrade(c, b) {
		t.Fatal("C should beat B on completeness")
	}
	if !IsUpgrade(a, c) {
		t.Fatal("A should beat C on encoding density")
	}
}
