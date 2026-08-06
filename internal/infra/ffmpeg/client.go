// client.go — the ffmpeg/ffprobe subprocess wrapper: probe metadata,
// extract a single (vision) frame, extract dense frames for perceptual
// hashing, and faststart-remux. Every invocation runs under an explicit
// ctx (SIGKILL on cancel) and a shared semaphore that caps concurrent
// ffmpeg processes — the CPU governor on this shared host. Dense
// extraction is a SINGLE decode pass (fps filter) rather than one ffmpeg
// per frame (Python's approach) — see decisions.md / rebuild-plan §9.
package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
// production implementation. Used by the small-output ops (probe, single
// frame, faststart) where buffering stdout is fine.
type runner func(ctx context.Context, name string, args []string) (stdout, stderr []byte, err error)

// streamRunner runs a subprocess and hands its LIVE stdout to consume as it
// arrives, so a large output is never fully buffered. Dense frame extraction
// uses this: a 90 s 1080p clip is ~300 MB of PNG, and holding it all (plus
// N of them under the concurrency cap) is what forced the worker memory up —
// see decisions.md 2026-08-06 (streamed dense extraction). execStreamRun is
// the production implementation; consume must fully drain (or the runner
// drains the rest) so the child never blocks on a full pipe.
type streamRunner func(ctx context.Context, name string, args []string, consume func(stdout io.Reader) error) (stderr []byte, err error)

// Client wraps the ffmpeg + ffprobe CLIs.
type Client struct {
	ins            *Instruments
	ffmpegPath     string
	ffprobePath    string
	timeout        time.Duration
	threadsPerProc int
	frameQuality   int
	run            runner
	runStream      streamRunner
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
		runStream:      execStreamRun,
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
//
// STREAMED: rather than buffer the whole PNG stream and return a []Frame,
// each frame is handed to onFrame the instant it's parsed off ffmpeg's
// stdout, then its bytes are free to GC. The caller (HashVideo) reduces each
// frame to an 8-byte dHash on the spot, so peak memory is ~one PNG instead of
// ~all of them (a 90 s 1080p clip is ~300 MB of PNG × the concurrency cap).
// onFrame returning an error aborts extraction with that error. A truncated
// trailing PNG is dropped silently (matches the old buffered splitPNGs).
func (c *Client) ExtractDenseFrames(ctx context.Context, videoPath string, intervalSecs float64, quality int, onFrame func(Frame) error) error {
	if err := statInput(videoPath); err != nil {
		return err
	}
	if intervalSecs <= 0 {
		return fmt.Errorf("%w: intervalSecs must be > 0", ErrExtractionFailed)
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := c.acquire(ctx); err != nil {
		return err
	}
	defer c.release()

	start := time.Now()
	fps := strconv.FormatFloat(1.0/intervalSecs, 'f', -1, 64)
	args := []string{"-i", videoPath, "-vf", "fps=" + fps, "-f", "image2pipe", "-vcodec", "png"}
	args = append(args, c.threadArgs()...)
	args = append(args, "-")
	idx := 0
	stderr, err := c.runStream(ctx, c.ffmpegPath, args, func(stdout io.Reader) error {
		return streamPNGs(stdout, func(png []byte) error {
			fr := Frame{PositionSecs: float64(idx) * intervalSecs, Data: png}
			idx++
			return onFrame(fr)
		})
	})
	c.observe(ctx, vocabulary.ActionFFmpegExtract, "extract_dense", start, err)
	if err != nil {
		return classify(ctx, false, err, stderr)
	}
	return nil
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

// execStreamRun is the production streamRunner: it starts the subprocess,
// hands consume the live stdout pipe, then drains any stdout consume left
// unread (so the child can exit instead of blocking on a full pipe) and
// waits. A consume error takes precedence; otherwise the exit/ctx error is
// returned — so a mid-stream ffmpeg failure or a ctx timeout still surfaces
// via cmd.Wait() even though streamPNGs treats a short read as a clean tail.
// stderr is bounded (capBuf) to guard against an OOM from a runaway error
// stream, matching execRun.
func execStreamRun(ctx context.Context, name string, args []string, consume func(io.Reader) error) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	errBuf := &capBuf{max: 16 << 10}
	cmd.Stderr = errBuf
	if err := cmd.Start(); err != nil {
		return errBuf.Bytes(), err
	}
	consumeErr := consume(stdout)
	_, _ = io.Copy(io.Discard, stdout) // drain leftover so the child can exit
	waitErr := cmd.Wait()
	if consumeErr != nil {
		return errBuf.Bytes(), consumeErr
	}
	return errBuf.Bytes(), waitErr
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

// maxPNGChunk guards streamPNGs against a corrupt chunk-length field
// requesting an absurd allocation; a real PNG frame chunk is far smaller.
// Past it we treat the stream as corrupt and stop, dropping the remainder
// (the old buffered parser was bounded by its finite input slice instead).
const maxPNGChunk = 64 << 20

// streamPNGs reads concatenated PNGs (as ffmpeg image2pipe emits) off r and
// invokes onPNG once per complete image, walking each PNG's chunk structure
// to its IEND marker. The bytes handed to onPNG are byte-identical to what
// the old buffered splitPNGs returned, so dHash values are unchanged — the
// only difference is that at most one PNG is resident at a time instead of
// the whole stream. A malformed/truncated tail is dropped silently (prior
// complete frames stand); onPNG returning an error aborts with that error.
func streamPNGs(r io.Reader, onPNG func([]byte) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	sig := make([]byte, len(pngSig))
	for {
		if _, err := io.ReadFull(br, sig); err != nil {
			return nil // clean EOF between images, or a truncated signature — done
		}
		if !bytes.Equal(sig, pngSig) {
			return nil // not a PNG start — stop, drop remainder (matches splitPNGs)
		}
		var buf bytes.Buffer
		buf.Write(sig)
		var hdr [8]byte
		for {
			if _, err := io.ReadFull(br, hdr[:]); err != nil {
				return nil // truncated chunk header — drop this partial frame
			}
			clen := binary.BigEndian.Uint32(hdr[:4])
			if clen > maxPNGChunk {
				return nil // corrupt length — stop
			}
			ctype := string(hdr[4:8])
			buf.Write(hdr[:])
			body := make([]byte, int(clen)+4) // chunk data + 4-byte CRC
			if _, err := io.ReadFull(br, body); err != nil {
				return nil // truncated chunk body — drop
			}
			buf.Write(body)
			if ctype == "IEND" {
				break
			}
		}
		if err := onPNG(buf.Bytes()); err != nil {
			return err
		}
	}
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
