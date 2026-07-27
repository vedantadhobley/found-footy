//go:build ffmpeg_integration

// Real-binary integration test + dense-extraction benchmark. Excluded from
// the normal suite (needs ffmpeg/ffprobe + real clips). Set FFMPEG_TEST_DIR
// to a directory of *.mp4 files:
//
//	FFMPEG_TEST_DIR=/clips go test -tags ffmpeg_integration -v -count=1 \
//	  ./internal/infra/ffmpeg/
//
// The logged per-clip probe/dense/frame timings are what set the semaphore
// cap + activity timeouts.
package ffmpeg

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
)

func TestIntegration_RealClips(t *testing.T) {
	dir := os.Getenv("FFMPEG_TEST_DIR")
	if dir == "" {
		t.Skip("set FFMPEG_TEST_DIR to a directory of real .mp4 clips")
	}
	clips, _ := filepath.Glob(filepath.Join(dir, "*.mp4"))
	if len(clips) == 0 {
		t.Skipf("no .mp4 clips in %s", dir)
	}

	ins := RegisterMetrics(metrics.New(), &logging.TestEmitter{})
	c, err := NewClient(config.FFmpegConfig{Timeout: 60 * time.Second, ThreadsPerProc: 2}, ins)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v (are ffmpeg + ffprobe installed?)", err)
	}

	for _, clip := range clips {
		t0 := time.Now()
		md, err := c.ProbeMetadata(ctx, clip)
		if err != nil {
			t.Errorf("%s probe: %v", filepath.Base(clip), err)
			continue
		}
		probeMS := time.Since(t0).Milliseconds()

		t1 := time.Now()
		frames, err := c.ExtractDenseFrames(ctx, clip, 0.25, 0)
		if err != nil {
			t.Errorf("%s dense: %v", filepath.Base(clip), err)
			continue
		}
		denseMS := time.Since(t1).Milliseconds()

		t2 := time.Now()
		jpg, err := c.ExtractFrame(ctx, clip, md.DurationSecs/2, 0)
		if err != nil {
			t.Errorf("%s frame: %v", filepath.Base(clip), err)
			continue
		}
		frameMS := time.Since(t2).Milliseconds()

		perFrame := 0.0
		if len(frames) > 0 {
			perFrame = float64(denseMS) / float64(len(frames))
		}
		t.Logf("%s: %dx%d %.1fs %s | probe %dms | dense %d frames in %dms (%.1f ms/frame) | mid-frame %dB in %dms",
			filepath.Base(clip), md.Width, md.Height, md.DurationSecs, md.Codec,
			probeMS, len(frames), denseMS, perFrame, len(jpg), frameMS)
	}
}
