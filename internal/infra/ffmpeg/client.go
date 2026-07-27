// client.go — the ffmpeg/ffprobe subprocess wrapper: probe metadata,
// extract a single (vision) frame, extract dense frames for perceptual
// hashing, and faststart-remux. Every invocation runs under an explicit
// ctx (SIGKILL on cancel) and a shared semaphore that caps concurrent
// ffmpeg processes — the CPU governor on this shared host. Dense
// extraction is a SINGLE decode pass (fps filter) rather than one ffmpeg
// per frame (Python's approach) — see decisions.md / rebuild-plan §9.
package ffmpeg

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Frame is one extracted frame: its position in the clip (seconds) + the
// encoded image bytes. Dense frames are PNG (lossless, for hash parity);
// the single vision frame is JPEG.
type Frame struct {
	PositionSecs float64
	Data         []byte
}

// VideoMetadata is the ffprobe summary the pipeline hard-filters on.
type VideoMetadata struct {
	DurationSecs float64
	Width        int
	Height       int
	Bitrate      int
	Codec        string
	ContainerFmt string
	FrameRate    float64
}

// runner runs a subprocess and returns its stdout + stderr. The seam lets
// tests substitute a fake without the real binaries; execRun is the
// production implementation.
type runner func(ctx context.Context, name string, args []string) (stdout, stderr []byte, err error)

// Client wraps the ffmpeg + ffprobe CLIs.
type Client struct {
	ins            *Instruments
	ffmpegPath     string
	ffprobePath    string
	timeout        time.Duration
	threadsPerProc int
	frameQuality   int
	run            runner
	sem            chan struct{} // caps concurrent ffmpeg/ffprobe processes
}

// NewClient validates config + Instruments and sizes the concurrency
// semaphore. No binary probe here — call Ping to verify the CLIs exist.
func NewClient(cfg config.FFmpegConfig, ins *Instruments) (*Client, error) {
	if ins == nil {
		return nil, fmt.Errorf("ffmpeg.NewClient: Instruments is required")
	}
	maxProc := cfg.MaxProcesses
	if maxProc <= 0 {
		maxProc = 4
	}
	ffmpegPath := cfg.FFmpegPath
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	ffprobePath := cfg.FFprobePath
	if ffprobePath == "" {
		ffprobePath = "ffprobe"
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		ins:            ins,
		ffmpegPath:     ffmpegPath,
		ffprobePath:    ffprobePath,
		timeout:        timeout,
		threadsPerProc: cfg.ThreadsPerProc, // 0 = ffmpeg auto-threads
		frameQuality:   cfg.FrameQuality,
		run:            execRun,
		sem:            make(chan struct{}, maxProc),
	}, nil
}

// Ping verifies both binaries are present + runnable. Environment probe.
func (c *Client) Ping(ctx context.Context) error {
	for _, bin := range []string{c.ffmpegPath, c.ffprobePath} {
		cctx, cancel := context.WithTimeout(ctx, c.timeout)
		_, _, err := c.run(cctx, bin, []string{"-version"})
		cancel()
		if err != nil {
			if isNotFound(err) {
				return fmt.Errorf("%w: %s", ErrBinaryNotFound, bin)
			}
			return fmt.Errorf("ffmpeg.Ping(%s): %w", bin, err)
		}
	}
	return nil
}

// ProbeMetadata returns duration + resolution + bitrate + codec via ffprobe.
func (c *Client) ProbeMetadata(ctx context.Context, videoPath string) (*VideoMetadata, error) {
	if err := statInput(videoPath); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := c.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.release()

	start := time.Now()
	args := []string{"-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", videoPath}
	stdout, stderr, err := c.run(ctx, c.ffprobePath, args)
	c.observe(ctx, vocabulary.ActionFFmpegProbe, "probe", start, err)
	if err != nil {
		return nil, classify(ctx, true, err, stderr)
	}
	md, perr := parseProbe(stdout)
	if perr != nil {
		return nil, fmt.Errorf("%w: %v", ErrProbeFailed, perr)
	}
	return md, nil
}

// ExtractFrame extracts a single JPEG frame at positionSecs (input-seek,
// fast). Used for the vision clock-check. quality<=0 uses the configured
// default.
func (c *Client) ExtractFrame(ctx context.Context, videoPath string, positionSecs float64, quality int) ([]byte, error) {
	if err := statInput(videoPath); err != nil {
		return nil, err
	}
	if quality <= 0 {
		quality = c.frameQuality
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := c.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.release()

	start := time.Now()
	args := []string{"-ss", formatSecs(positionSecs), "-i", videoPath, "-vframes", "1", "-f", "image2pipe", "-vcodec", "mjpeg", "-q:v", strconv.Itoa(quality)}
	args = append(args, c.threadArgs()...)
	args = append(args, "-")
	stdout, stderr, err := c.run(ctx, c.ffmpegPath, args)
	c.observe(ctx, vocabulary.ActionFFmpegExtract, "extract_frame", start, err)
	if err != nil {
		return nil, classify(ctx, false, err, stderr)
	}
	if len(stdout) == 0 {
		return nil, fmt.Errorf("%w: empty frame output at %.2fs", ErrExtractionFailed, positionSecs)
	}
	return stdout, nil
}

// ExtractDenseFrames extracts frames at fixed intervalSecs spacing in ONE
// ffmpeg decode pass (fps filter), emitting lossless PNGs. Feeds rung-2
// dHash. intervalSecs is a dedup-tuning param supplied by the caller (not
// baked into the adapter). Position of frame i ≈ i*intervalSecs.
func (c *Client) ExtractDenseFrames(ctx context.Context, videoPath string, intervalSecs float64, quality int) ([]Frame, error) {
	if err := statInput(videoPath); err != nil {
		return nil, err
	}
	if intervalSecs <= 0 {
		return nil, fmt.Errorf("%w: intervalSecs must be > 0", ErrExtractionFailed)
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := c.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.release()

	start := time.Now()
	fps := strconv.FormatFloat(1.0/intervalSecs, 'f', -1, 64)
	args := []string{"-i", videoPath, "-vf", "fps=" + fps, "-f", "image2pipe", "-vcodec", "png"}
	args = append(args, c.threadArgs()...)
	args = append(args, "-")
	stdout, stderr, err := c.run(ctx, c.ffmpegPath, args)
	c.observe(ctx, vocabulary.ActionFFmpegExtract, "extract_dense", start, err)
	if err != nil {
		return nil, classify(ctx, false, err, stderr)
	}
	pngs := splitPNGs(stdout)
	frames := make([]Frame, len(pngs))
	for i, p := range pngs {
		frames[i] = Frame{PositionSecs: float64(i) * intervalSecs, Data: p}
	}
	return frames, nil
}

// Faststart remuxes inPath→outPath moving the moov atom to the front
// (-movflags +faststart, stream copy — no re-encode). Load-bearing for
// browser play-latency (decisions.md 2026-07-02).
func (c *Client) Faststart(ctx context.Context, inPath, outPath string) error {
	if err := statInput(inPath); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := c.acquire(ctx); err != nil {
		return err
	}
	defer c.release()

	start := time.Now()
	args := []string{"-y", "-i", inPath, "-c", "copy", "-movflags", "+faststart"}
	args = append(args, c.threadArgs()...)
	args = append(args, outPath)
	_, stderr, err := c.run(ctx, c.ffmpegPath, args)
	c.observe(ctx, vocabulary.ActionFFmpegFaststart, "faststart", start, err)
	if err != nil {
		return classify(ctx, false, err, stderr)
	}
	return nil
}

// acquire blocks for a semaphore slot, honouring ctx. If the ctx expires
// first, we were saturated → ErrConcurrencyExhausted (retryable).
func (c *Client) acquire(ctx context.Context) error {
	select {
	case c.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: %v", ErrConcurrencyExhausted, ctx.Err())
	}
}

func (c *Client) release() { <-c.sem }

func (c *Client) threadArgs() []string {
	if c.threadsPerProc > 0 {
		return []string{"-threads", strconv.Itoa(c.threadsPerProc)}
	}
	return nil
}

func (c *Client) observe(ctx context.Context, okAction vocabulary.Action, op string, start time.Time, err error) {
	elapsed := time.Since(start)
	outcome := "success"
	if err != nil {
		outcome = "failure"
	}
	c.ins.ops.WithLabelValues(op, outcome).Inc()
	c.ins.opLatency.WithLabelValues(op).Observe(elapsed.Seconds())
	if err != nil {
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionFFmpegOpFailed, "ffmpeg op failed",
			logging.String("op", op), logging.Int64("elapsed_ms", elapsed.Milliseconds()), logging.Err(err))
		return
	}
	c.ins.emitEvent(ctx, logging.LevelDebug, okAction, "ffmpeg op ok",
		logging.String("op", op), logging.Int64("elapsed_ms", elapsed.Milliseconds()))
}

// execRun is the production runner: exec.CommandContext (SIGKILL on ctx
// cancel), stdout captured, stderr bounded to guard against OOM.
func execRun(ctx context.Context, name string, args []string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	errBuf := &capBuf{max: 16 << 10}
	cmd.Stdout = &out
	cmd.Stderr = errBuf
	err := cmd.Run()
	return out.Bytes(), errBuf.Bytes(), err
}

// capBuf discards writes past max so a runaway ffmpeg stderr can't OOM the
// worker. It reports full writes so the child process never blocks on a
// stalled pipe.
type capBuf struct {
	buf bytes.Buffer
	max int
}

func (c *capBuf) Write(p []byte) (int, error) {
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			c.buf.Write(p[:room])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

func (c *capBuf) Bytes() []byte { return c.buf.Bytes() }

// --- pure helpers (unit-tested without the binaries) ---

type probeOut struct {
	Streams []struct {
		CodecType    string `json:"codec_type"`
		CodecName    string `json:"codec_name"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		AvgFrameRate string `json:"avg_frame_rate"`
		BitRate      string `json:"bit_rate"`
	} `json:"streams"`
	Format struct {
		Duration   string `json:"duration"`
		BitRate    string `json:"bit_rate"`
		FormatName string `json:"format_name"`
	} `json:"format"`
}

// parseProbe maps ffprobe JSON to VideoMetadata. Bitrate falls back from
// format-level to the video stream's own bit_rate when the container omits
// it. Errors if there's no video stream / no dimensions.
func parseProbe(b []byte) (*VideoMetadata, error) {
	var p probeOut
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	md := &VideoMetadata{ContainerFmt: p.Format.FormatName}
	md.DurationSecs, _ = strconv.ParseFloat(p.Format.Duration, 64)
	if p.Format.BitRate != "" {
		md.Bitrate, _ = strconv.Atoi(p.Format.BitRate)
	}
	for _, s := range p.Streams {
		if s.CodecType != "video" {
			continue
		}
		md.Width, md.Height = s.Width, s.Height
		md.Codec = s.CodecName
		md.FrameRate = parseFrameRate(s.AvgFrameRate)
		if md.Bitrate == 0 && s.BitRate != "" {
			md.Bitrate, _ = strconv.Atoi(s.BitRate)
		}
		break
	}
	if md.Width == 0 || md.Height == 0 {
		return nil, fmt.Errorf("no video stream / zero dimensions")
	}
	return md, nil
}

// parseFrameRate turns ffprobe's "num/den" (e.g. "30000/1001") into fps.
func parseFrameRate(s string) float64 {
	if s == "" || s == "0/0" {
		return 0
	}
	if num, den, ok := strings.Cut(s, "/"); ok {
		n, _ := strconv.ParseFloat(num, 64)
		d, _ := strconv.ParseFloat(den, 64)
		if d != 0 {
			return n / d
		}
		return 0
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

var pngSig = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

// splitPNGs splits a stream of concatenated PNGs (as ffmpeg image2pipe
// emits) into individual encoded images by walking each PNG's chunk
// structure to its IEND marker. A malformed/truncated tail is dropped.
func splitPNGs(b []byte) [][]byte {
	var out [][]byte
	for len(b) >= len(pngSig) && bytes.Equal(b[:len(pngSig)], pngSig) {
		p := len(pngSig)
		for p+8 <= len(b) {
			clen := int(binary.BigEndian.Uint32(b[p : p+4]))
			ctype := string(b[p+4 : p+8])
			next := p + 8 + clen + 4 // 4 len + 4 type + data + 4 crc
			if next > len(b) || next < p {
				return out // truncated / overflow — drop remainder
			}
			p = next
			if ctype == "IEND" {
				break
			}
		}
		out = append(out, b[:p])
		b = b[p:]
	}
	return out
}

// classify maps a subprocess failure to the typed taxonomy. isProbe picks
// the generic bucket (ErrProbeFailed vs ErrExtractionFailed).
func classify(ctx context.Context, isProbe bool, err error, stderr []byte) error {
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%w", ErrExtractionTimeout)
	}
	if isNotFound(err) {
		return fmt.Errorf("%w", ErrBinaryNotFound)
	}
	s := string(stderr)
	switch {
	case strings.Contains(s, "Invalid data found"),
		strings.Contains(s, "moov atom not found"),
		strings.Contains(s, "Invalid NAL"),
		strings.Contains(s, "could not find codec parameters"):
		return fmt.Errorf("%w: %s", ErrInputCorrupted, stderrTail(stderr))
	case strings.Contains(s, "No space left on device"):
		return fmt.Errorf("%w: %s", ErrOutputWriteFailed, stderrTail(stderr))
	}
	if isProbe {
		return fmt.Errorf("%w: %s", ErrProbeFailed, stderrTail(stderr))
	}
	return fmt.Errorf("%w: %s", ErrExtractionFailed, stderrTail(stderr))
}

func statInput(path string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrInputNotFound, path)
		}
		return fmt.Errorf("%w: %v", ErrInputNotFound, err)
	}
	return nil
}

func isNotFound(err error) bool {
	return errors.Is(err, exec.ErrNotFound) ||
		strings.Contains(err.Error(), "executable file not found")
}

func formatSecs(s float64) string { return strconv.FormatFloat(s, 'f', 3, 64) }

func stderrTail(stderr []byte) string {
	s := strings.TrimSpace(string(stderr))
	const n = 240
	if len(s) > n {
		return "..." + s[len(s)-n:]
	}
	return s
}
