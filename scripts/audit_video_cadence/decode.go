// decode.go — Bounded local-only ffprobe/ffmpeg reader preserving presentation timestamps.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const maximumFrames = 12_000

// probeData retains timestamps so VFR or broken PTS cannot masquerade as a repeat rate.
type probeData struct {
	Streams []struct {
		AverageFrameRate string `json:"avg_frame_rate"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Frames []struct {
		Timestamp string `json:"best_effort_timestamp_time"`
	} `json:"frames"`
}

// report deliberately has no effective_fps, source_fps, or keeper recommendation.
type report struct {
	Version       string                 `json:"version"`
	File          string                 `json:"file"`
	SHA256        string                 `json:"sha256"`
	ReportedFPS   float64                `json:"reported_fps"`
	DecodedFrames int                    `json:"decoded_frames"`
	ElapsedMS     int64                  `json:"elapsed_ms"`
	Counts        map[classification]int `json:"window_counts"`
	Windows       []window               `json:"windows"`
}

// inspect accepts regular local files only and reports errors before exposing a result.
func inspect(ctx context.Context, path string) (report, error) {
	started := time.Now()
	path, err := filepath.Abs(path)
	if err != nil {
		return report{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return report{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 256<<20 {
		return report{}, fmt.Errorf("input must be a regular file no larger than 256 MiB")
	}
	probe, err := probeFile(ctx, path)
	if err != nil {
		return report{}, err
	}
	rate, err := frameRate(probe.Streams[0].AverageFrameRate)
	if err != nil {
		return report{}, err
	}
	samples, count, err := decodeFile(ctx, path, probe)
	if err != nil {
		return report{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return report{}, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return report{}, err
	}
	result := report{Version: "repeat-pattern-v1", File: filepath.Base(path),
		SHA256: hex.EncodeToString(hash.Sum(nil)), ReportedFPS: rate, DecodedFrames: count,
		Windows: analyze(samples, rate), Counts: make(map[classification]int),
		ElapsedMS: time.Since(started).Milliseconds()}
	for _, item := range result.Windows {
		result.Counts[item.Classification]++
	}
	return result, nil
}

// probeFile caps captured output and subprocess time; network protocols are disallowed.
func probeFile(ctx context.Context, path string) (probeData, error) {
	command := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-threads", "1",
		"-protocol_whitelist", "file,pipe", "-select_streams", "v:0", "-read_intervals", "%+91",
		"-show_entries", "stream=avg_frame_rate:format=duration:frame=best_effort_timestamp_time",
		"-of", "json", path)
	output, stderr := &boundedBuffer{limit: 4 << 20}, &boundedBuffer{limit: 4096}
	command.Stdout, command.Stderr = output, stderr
	if err := command.Run(); err != nil {
		return probeData{}, fmt.Errorf("ffprobe: %w: %s", err, stderr.String())
	}
	if output.truncated {
		return probeData{}, fmt.Errorf("ffprobe exceeded output limit")
	}
	var probe probeData
	if err := json.Unmarshal(output.Bytes(), &probe); err != nil {
		return probeData{}, fmt.Errorf("decode ffprobe: %w", err)
	}
	duration, err := strconv.ParseFloat(probe.Format.Duration, 64)
	if err != nil || math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 || duration > 90 ||
		len(probe.Streams) != 1 || len(probe.Frames) < 2 || len(probe.Frames) > maximumFrames {
		return probeData{}, fmt.Errorf("requires one video stream, 2–12000 frames, and duration in (0,90] seconds")
	}
	return probe, nil
}

// decodeFile does not resample time: fps_mode passthrough preserves repeated frames.
func decodeFile(ctx context.Context, path string, probe probeData) ([]transition, int, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-xerror",
		"-threads", "1", "-protocol_whitelist", "file,pipe", "-i", path, "-map", "0:v:0", "-an",
		"-vf", "scale=320:180:flags=area,format=gray", "-filter_threads", "1",
		"-fps_mode", "passthrough", "-frames:v", strconv.Itoa(maximumFrames+1),
		"-threads", "1", "-f", "rawvideo", "-")
	stderr := &boundedBuffer{limit: 4096}
	command.Stderr = stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, 0, err
	}
	if err := command.Start(); err != nil {
		return nil, 0, err
	}
	samples, count, readErr := readFrames(stdout, probe)
	if readErr != nil {
		cancel()
	}
	waitErr := command.Wait()
	if readErr != nil {
		return nil, 0, readErr
	}
	if waitErr != nil {
		return nil, 0, fmt.Errorf("ffmpeg: %w: %s", waitErr, stderr.String())
	}
	return samples, count, nil
}

// readFrames bounds memory to two scaled frames plus scalar transition records.
func readFrames(reader io.Reader, probe probeData) ([]transition, int, error) {
	previous, current := make([]byte, frameBytes), make([]byte, frameBytes)
	samples := make([]transition, 0, len(probe.Frames))
	count, lastTime := 0, 0.0
	for {
		_, err := io.ReadFull(reader, current)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, count, fmt.Errorf("incomplete raw frame: %w", err)
		}
		if count >= len(probe.Frames) {
			return nil, count, fmt.Errorf("decode has more frames than probe")
		}
		seconds, err := strconv.ParseFloat(probe.Frames[count].Timestamp, 64)
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
			return nil, count, fmt.Errorf("missing or invalid presentation timestamp at frame %d", count)
		}
		if count > 0 {
			full, center := frameDifference(previous, current)
			samples = append(samples, transition{seconds: seconds, delta: seconds - lastTime, full: full, center: center})
		}
		previous, current = current, previous
		lastTime, count = seconds, count+1
	}
	if count != len(probe.Frames) {
		return nil, count, fmt.Errorf("decode/probe frame counts differ: %d/%d", count, len(probe.Frames))
	}
	return samples, count, nil
}

// frameRate rejects malformed or unbounded rates rather than guessing a window size.
func frameRate(value string) (float64, error) {
	numerator, denominator, ok := strings.Cut(value, "/")
	n, nErr := strconv.ParseFloat(numerator, 64)
	d, dErr := strconv.ParseFloat(denominator, 64)
	rate := n / d
	if !ok || nErr != nil || dErr != nil || d <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 1 || rate > 240 {
		return 0, fmt.Errorf("unsupported average frame rate %q", value)
	}
	return rate, nil
}

// boundedBuffer drains excess child output without retaining unbounded diagnostics.
type boundedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

// Write reports consumed bytes even after the retention cap, preventing pipe stalls.
func (b *boundedBuffer) Write(p []byte) (int, error) {
	kept := min(len(p), max(0, b.limit-b.buffer.Len()))
	_, _ = b.buffer.Write(p[:kept])
	b.truncated = b.truncated || kept < len(p)
	return len(p), nil
}

// Bytes exposes retained output without inheriting bytes.Buffer.ReadFrom's uncapped path.
func (b *boundedBuffer) Bytes() []byte { return b.buffer.Bytes() }

// String renders only the retained diagnostic prefix.
func (b *boundedBuffer) String() string { return b.buffer.String() }
