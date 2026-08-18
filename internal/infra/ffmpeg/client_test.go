// White-box unit tests for the ffmpeg adapter: the pure helpers (probe
// JSON parse, PNG stream split, error classification) plus the seam-driven
// methods via a fake runner — all without the real binaries. Real-binary
// extraction + the dense-hash benchmark live in a tagged integration test.
package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
)

func mustClient(t *testing.T, cfg config.FFmpegConfig) *Client {
	t.Helper()
	ins := RegisterMetrics(metrics.New(), &logging.TestEmitter{})
	c, err := NewClient(cfg, ins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// tempFile makes an (empty) input path that statInput accepts.
func tempFile(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "vid-*.mp4")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	return f.Name()
}

func tinyPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return b.Bytes()
}

func TestNewClient_NilInstruments(t *testing.T) {
	if _, err := NewClient(config.FFmpegConfig{}, nil); err == nil {
		t.Fatal("want error on nil Instruments")
	}
}

func TestParseProbe(t *testing.T) {
	const j = `{"streams":[
		{"codec_type":"video","codec_name":"h264","width":1280,"height":720,"avg_frame_rate":"25/1","bit_rate":"2176000"},
		{"codec_type":"audio","codec_name":"aac"}],
		"format":{"duration":"11.401000","bit_rate":"2200000","format_name":"mov,mp4,m4a"}}`
	md, err := parseProbe([]byte(j))
	if err != nil {
		t.Fatalf("parseProbe: %v", err)
	}
	if md.Width != 1280 || md.Height != 720 {
		t.Errorf("dims = %dx%d, want 1280x720", md.Width, md.Height)
	}
	if md.Codec != "h264" || md.FrameRate != 25 {
		t.Errorf("codec/fps = %s/%v, want h264/25", md.Codec, md.FrameRate)
	}
	if md.DurationSecs < 11.4 || md.DurationSecs > 11.41 {
		t.Errorf("duration = %v, want ~11.401", md.DurationSecs)
	}
	if md.Bitrate != 2200000 { // format-level wins over stream bit_rate
		t.Errorf("bitrate = %d, want 2200000", md.Bitrate)
	}
}

func TestParseProbe_NoVideoStream(t *testing.T) {
	const j = `{"streams":[{"codec_type":"audio","codec_name":"aac"}],"format":{"duration":"5.0"}}`
	if _, err := parseProbe([]byte(j)); err == nil {
		t.Fatal("want error when no video stream present")
	}
}

func TestParseFrameRate(t *testing.T) {
	cases := map[string]float64{"25/1": 25, "": 0, "0/0": 0, "24": 24}
	for in, want := range cases {
		if got := parseFrameRate(in); got != want {
			t.Errorf("parseFrameRate(%q) = %v, want %v", in, got, want)
		}
	}
	if got := parseFrameRate("30000/1001"); got < 29.9 || got > 30.0 {
		t.Errorf("parseFrameRate(30000/1001) = %v, want ~29.97", got)
	}
}

func TestStreamPNGs(t *testing.T) {
	a, b := tinyPNG(t, 2, 2), tinyPNG(t, 3, 3)
	stream := append(append([]byte{}, a...), b...)
	var got [][]byte
	collect := func(r io.Reader) int {
		got = got[:0]
		_ = streamPNGs(r, func(p []byte) error {
			got = append(got, append([]byte(nil), p...))
			return nil
		})
		return len(got)
	}
	if n := collect(bytes.NewReader(stream)); n != 2 {
		t.Fatalf("stream → %d images, want 2", n)
	}
	for i, p := range got {
		if _, err := png.Decode(bytes.NewReader(p)); err != nil {
			t.Errorf("image %d not a valid PNG: %v", i, err)
		}
	}
	// Byte-identical to the source PNGs — this is what keeps dHash values
	// unchanged across the streaming refactor.
	if !bytes.Equal(got[0], a) || !bytes.Equal(got[1], b) {
		t.Error("streamPNGs output not byte-identical to source PNGs")
	}
	if collect(bytes.NewReader(nil)) != 0 || collect(bytes.NewReader([]byte("not-a-png"))) != 0 {
		t.Error("nil / junk should stream to 0 images")
	}
	// A truncated trailing PNG is dropped; the complete leading one stands.
	if n := collect(bytes.NewReader(append(append([]byte{}, a...), b[:len(b)-5]...))); n != 1 {
		t.Errorf("truncated tail → %d images, want 1 (leading intact)", n)
	}
}

func TestClassify(t *testing.T) {
	bg := context.Background()

	tctx, cancel := context.WithTimeout(bg, time.Nanosecond)
	defer cancel()
	<-tctx.Done()
	if !errors.Is(classify(tctx, false, errors.New("killed"), nil), ErrExtractionTimeout) {
		t.Error("deadline-exceeded ctx → ErrExtractionTimeout")
	}
	if !errors.Is(classify(bg, false, exec.ErrNotFound, nil), ErrBinaryNotFound) {
		t.Error("exec.ErrNotFound → ErrBinaryNotFound")
	}
	if !errors.Is(classify(bg, false, errors.New("x"), []byte("moov atom not found")), ErrInputCorrupted) {
		t.Error("moov-atom stderr → ErrInputCorrupted")
	}
	if !errors.Is(classify(bg, false, errors.New("x"), []byte("No space left on device")), ErrOutputWriteFailed) {
		t.Error("no-space stderr → ErrOutputWriteFailed")
	}
	if !errors.Is(classify(bg, true, errors.New("x"), []byte("weird")), ErrProbeFailed) {
		t.Error("generic probe → ErrProbeFailed")
	}
	if !errors.Is(classify(bg, false, errors.New("x"), []byte("weird")), ErrExtractionFailed) {
		t.Error("generic extract → ErrExtractionFailed")
	}
}

func TestRetryable(t *testing.T) {
	for _, e := range []error{ErrExtractionTimeout, ErrOutputWriteFailed, ErrConcurrencyExhausted} {
		if !Retryable(e) {
			t.Errorf("%v should be retryable", e)
		}
	}
	for _, e := range []error{ErrBinaryNotFound, ErrInputNotFound, ErrInputCorrupted, ErrProbeFailed, ErrExtractionFailed} {
		if Retryable(e) {
			t.Errorf("%v should NOT be retryable", e)
		}
	}
}

func TestProbeMetadata_FakeRunner(t *testing.T) {
	c := mustClient(t, config.FFmpegConfig{})
	c.run = func(_ context.Context, _ string, _ []string) ([]byte, []byte, error) {
		return []byte(`{"streams":[{"codec_type":"video","codec_name":"h264","width":640,"height":360,"avg_frame_rate":"30/1"}],"format":{"duration":"9.0","format_name":"mp4"}}`), nil, nil
	}
	md, err := c.ProbeMetadata(context.Background(), tempFile(t))
	if err != nil {
		t.Fatalf("ProbeMetadata: %v", err)
	}
	if md.Width != 640 || md.Height != 360 || md.FrameRate != 30 {
		t.Errorf("md = %+v", md)
	}
}

func TestProbeMetadata_InputNotFound(t *testing.T) {
	c := mustClient(t, config.FFmpegConfig{})
	if _, err := c.ProbeMetadata(context.Background(), "/no/such/file.mp4"); !errors.Is(err, ErrInputNotFound) {
		t.Fatalf("want ErrInputNotFound, got %v", err)
	}
}

func TestExtractDenseFrames_FakeRunner(t *testing.T) {
	c := mustClient(t, config.FFmpegConfig{})
	stream := bytes.Join([][]byte{tinyPNG(t, 2, 2), tinyPNG(t, 2, 2), tinyPNG(t, 2, 2)}, nil)
	var gotArgs []string
	c.runStream = func(_ context.Context, _ string, args []string, consume func(io.Reader) error) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return nil, consume(bytes.NewReader(stream))
	}
	var frames []Frame
	err := c.ExtractDenseFrames(context.Background(), tempFile(t), 0.1, 640, func(fr Frame) error {
		frames = append(frames, fr)
		return nil
	})
	if err != nil {
		t.Fatalf("ExtractDenseFrames: %v", err)
	}
	if len(frames) != 3 {
		t.Fatalf("want 3 frames, got %d", len(frames))
	}
	// 0.1 s = the production sampling interval (DEDUP_FRAME_INTERVAL_SECS).
	for i, want := range []float64{0, 0.1, 0.2} {
		if frames[i].PositionSecs != want {
			t.Errorf("frame %d pos = %v, want %v", i, frames[i].PositionSecs, want)
		}
	}
	wantFilter := "fps=10,format=gray,scale=640:-2:flags=area"
	if len(gotArgs) < 4 || gotArgs[3] != wantFilter {
		t.Fatalf("ffmpeg args = %q, want -vf %q", gotArgs, wantFilter)
	}
}

func TestExtractDenseFrames_RejectsInvalidWorkingWidth(t *testing.T) {
	c := mustClient(t, config.FFmpegConfig{})
	err := c.ExtractDenseFrames(context.Background(), tempFile(t), 0.1, 0, func(Frame) error { return nil })
	if !errors.Is(err, ErrExtractionFailed) {
		t.Fatalf("error = %v, want ErrExtractionFailed", err)
	}
}

func TestExtractFrame_EmptyOutput(t *testing.T) {
	c := mustClient(t, config.FFmpegConfig{})
	c.run = func(_ context.Context, _ string, _ []string) ([]byte, []byte, error) {
		return nil, nil, nil // ran clean but produced no bytes
	}
	if _, err := c.ExtractFrame(context.Background(), tempFile(t), 1.0, 0); !errors.Is(err, ErrExtractionFailed) {
		t.Fatalf("want ErrExtractionFailed on empty output, got %v", err)
	}
}

func TestConcurrencyExhausted(t *testing.T) {
	c := mustClient(t, config.FFmpegConfig{MaxProcesses: 1})
	c.sem <- struct{}{} // occupy the only slot
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // ctx already done → acquire can't get a slot
	if _, err := c.ProbeMetadata(ctx, tempFile(t)); !errors.Is(err, ErrConcurrencyExhausted) {
		t.Fatalf("want ErrConcurrencyExhausted, got %v", err)
	}
}
