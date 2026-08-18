//go:build ffmpeg_integration

// Real-binary integration test + extract-and-hash benchmark. Excluded from
// the normal suite (needs ffmpeg/ffprobe + real clips). Set FFMPEG_TEST_DIR
// to a directory of *.mp4 files:
//
//	FFMPEG_TEST_DIR=/clips go test -tags ffmpeg_integration -v -count=1 \
//	  ./internal/infra/ffmpeg/
//
// Logs per-clip probe / dense-extract / dHash timings (the numbers that set
// the semaphore cap + activity timeouts), asserts each clip perceptually
// matches itself, and reports cross-clip matches for information.
package ffmpeg

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
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

	seqs := map[string][]uint64{}
	for _, clip := range clips {
		name := filepath.Base(clip)

		t0 := time.Now()
		md, err := c.ProbeMetadata(ctx, clip)
		if err != nil {
			t.Errorf("%s probe: %v", name, err)
			continue
		}
		probeMS := time.Since(t0).Milliseconds()

		t1 := time.Now()
		var frames []Frame
		err = c.ExtractDenseFrames(ctx, clip, 0.1, dvideo.FrameHashWorkingWidth, func(f Frame) error {
			frames = append(frames, f)
			return nil
		})
		if err != nil {
			t.Errorf("%s dense: %v", name, err)
			continue
		}
		denseMS := time.Since(t1).Milliseconds()

		t2 := time.Now()
		hashes := make([]uint64, 0, len(frames))
		for _, f := range frames {
			h, err := dvideo.DHashPNG(f.Data)
			if err != nil {
				t.Errorf("%s hash frame @%.2fs: %v", name, f.PositionSecs, err)
				continue
			}
			hashes = append(hashes, h)
		}
		hashMS := time.Since(t2).Milliseconds()
		seqs[name] = hashes

		// Production dedup params: maxHamming 10, minRun 30 (@0.1s = 3s), maxGaps 3.
		if !dvideo.Match(hashes, hashes, 10, 30, 3) {
			t.Errorf("%s should perceptually match itself", name)
		}

		t.Logf("%s: %dx%d %.1fs %s | probe %dms | dense %d frames %dms | dHash %d %dms | self-match ok",
			name, md.Width, md.Height, md.DurationSecs, md.Codec, probeMS, len(frames), denseMS, len(hashes), hashMS)
	}

	// Informational: do any two clips share footage? (all are Dybala/Roma —
	// could be the same goal or the 34' vs 55'.)
	names := make([]string, 0, len(seqs))
	for n := range seqs {
		names = append(names, n)
	}
	sort.Strings(names)
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			m := dvideo.Match(seqs[names[i]], seqs[names[j]], 10, 30, 3)
			t.Logf("cross-match %s vs %s: %v", names[i], names[j], m)
		}
	}
}
