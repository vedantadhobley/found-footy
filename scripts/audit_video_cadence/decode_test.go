// decode_test.go — Real ffmpeg controls; generated pixels, no production media dependency.
package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestFFmpegCadenceControls validates the whole native-frame pipeline when ffmpeg is available.
// Pinned Go-only unit gates skip it; the documented compiled-test run requires the ffmpeg image.
func TestFFmpegCadenceControls(t *testing.T) {
	if testing.Short() {
		t.Skip("ffmpeg-generated media controls")
	}
	for _, binary := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(binary); err != nil {
			if os.Getenv("CADENCE_REQUIRE_FFMPEG") == "1" {
				t.Fatalf("required %s unavailable: %v", binary, err)
			}
			t.Skipf("%s unavailable; run the compiled tests in the documented ffmpeg container", binary)
		}
	}
	cases := []struct {
		name, source, filter string
		factor               int
		want                 classification
		reencode             bool
	}{
		{"native_60", "testsrc2=s=640x360:r=60:d=4", "null", 0, noPattern, false},
		{"native_30", "testsrc2=s=640x360:r=30:d=4", "null", 0, noPattern, false},
		{"repeat_30_to_60", "testsrc2=s=640x360:r=30:d=4", "fps=60", 2, periodicRepeats, false},
		{"repeat_20_to_60", "testsrc2=s=640x360:r=20:d=4", "fps=60", 3, periodicRepeats, false},
		{"repeat_15_to_60", "testsrc2=s=640x360:r=15:d=4", "fps=60", 4, periodicRepeats, false},
		{"lossy_repeat_30_to_60", "testsrc2=s=640x360:r=30:d=4", "fps=60", 2, periodicRepeats, true},
		{"blended_30_to_60", "testsrc2=s=640x360:r=30:d=4", "minterpolate=fps=60:mi_mode=blend", 0, noPattern, false},
		{"repeat_hidden_by_center_animation", "testsrc2=s=640x360:r=30:d=4",
			"fps=60,drawbox=x=100:y=100:w=440:h=100:color=white:t=fill:enable='mod(n,2)'", 0, noPattern, false},
		{"static_60", "color=c=green:s=640x360:r=60:d=4", "null", 0, lowMotion, false},
		{"static_with_edge_overlay", "color=c=green:s=640x360:r=60:d=4",
			"drawbox=x=0:y=0:w=100:h=24:color=white:t=fill:enable='mod(n,2)'", 0, lowMotion, false},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			path := filepath.Join(t.TempDir(), item.name+".mp4")
			generate(t, ctx, []string{"-f", "lavfi", "-i", item.source, "-vf", item.filter}, path, "20")
			if item.reencode {
				next := filepath.Join(t.TempDir(), "reencoded.mp4")
				generate(t, ctx, []string{"-i", path}, next, "36")
				path = next
			}
			result, err := inspect(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Windows) == 0 {
				t.Fatal("missing measurement windows")
			}
			for _, window := range result.Windows {
				if window.Classification != item.want || window.RepeatFactor != item.factor {
					t.Fatalf("want %s factor=%d, got %+v", item.want, item.factor, window)
				}
			}
			t.Logf("reported_fps=%.3f frames=%d windows=%v elapsed_ms=%d",
				result.ReportedFPS, result.DecodedFrames, result.Counts, result.ElapsedMS)
		})
	}
}

// generate confines synthetic H.264 output to the test-owned temporary directory.
func generate(t *testing.T, ctx context.Context, input []string, output, crf string) {
	t.Helper()
	args := append([]string{"-nostdin", "-v", "error", "-threads", "1"}, input...)
	args = append(args, "-an", "-filter_threads", "1", "-c:v", "libx264", "-preset", "veryfast",
		"-crf", crf, "-pix_fmt", "yuv420p", "-threads", "1", output)
	if data, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("generate fixture: %v: %s", err, data)
	}
}
