// Unit tests for the V-phase per-candidate activities, driven by fakes for
// all three deps (syndication / ffmpeg / s3) — no real Twitter, ffmpeg, or
// Garage. Covers the outcome-vs-error split + the pre-download-filter skip.
package video

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
	"github.com/vedantadhobley/found-footy/internal/infra/ffmpeg"
	"github.com/vedantadhobley/found-footy/internal/infra/syndication"
)

// --- fakes ---

type fakeSynd struct {
	rv             *syndication.ResolvedVideo
	rvErr          error
	dlBytes        []byte
	dlErr          error
	downloadCalled bool
}

func (f *fakeSynd) ResolveVideo(context.Context, string) (*syndication.ResolvedVideo, error) {
	return f.rv, f.rvErr
}
func (f *fakeSynd) Download(_ context.Context, _ string, w io.Writer) (int64, error) {
	f.downloadCalled = true
	if f.dlErr != nil {
		return 0, f.dlErr
	}
	n, _ := w.Write(f.dlBytes)
	return int64(n), nil
}

type fakeFFmpeg struct {
	md        *ffmpeg.VideoMetadata
	mdErr     error
	frames    []ffmpeg.Frame
	framesErr error
}

func (f *fakeFFmpeg) ProbeMetadata(context.Context, string) (*ffmpeg.VideoMetadata, error) {
	return f.md, f.mdErr
}
func (f *fakeFFmpeg) ExtractDenseFrames(_ context.Context, _ string, _ float64, _ int, onFrame func(ffmpeg.Frame) error) error {
	if f.framesErr != nil {
		return f.framesErr
	}
	for _, fr := range f.frames {
		if err := onFrame(fr); err != nil {
			return err
		}
	}
	return nil
}

type fakeS3 struct {
	uploaded map[string][]byte
	upErr    error
	dlData   []byte
	dlErr    error
}

func (f *fakeS3) Upload(_ context.Context, key string, body io.Reader, _ int64, _ string) error {
	if f.upErr != nil {
		return f.upErr
	}
	b, _ := io.ReadAll(body)
	if f.uploaded == nil {
		f.uploaded = map[string][]byte{}
	}
	f.uploaded[key] = b
	return nil
}
func (f *fakeS3) Download(context.Context, string) (io.ReadCloser, int64, error) {
	if f.dlErr != nil {
		return nil, 0, f.dlErr
	}
	return io.NopCloser(bytes.NewReader(f.dlData)), int64(len(f.dlData)), nil
}

// --- helpers ---

func thresholds() dvideo.FilterThresholds {
	return dvideo.FilterThresholds{
		MinDurationSecs: 3, MaxDurationSecs: 90,
		MinAspectRatio: 1.75, MaxAspectRatio: 1.82,
		MinShortEdge: 600, MinFrameRate: 20,
	}
}

func newActs(t *testing.T, s *fakeSynd, ff *fakeFFmpeg, s3 *fakeS3) *Activities {
	t.Helper()
	return &Activities{
		Syndication: s, FFmpeg: ff, S3: s3,
		ScratchDir: t.TempDir(), StagingPrefix: "staging",
		Thresholds: thresholds(), FrameIntervalSecs: 0.25,
	}
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func input() DownloadAndStageInput {
	return DownloadAndStageInput{EventID: uuid.New(), FixtureID: 1589332, TweetURL: "https://x.com/u/status/123"}
}

// --- DownloadAndStage ---

func TestDownloadAndStage_Passed(t *testing.T) {
	body := []byte("fake-mp4-bytes")
	s := &fakeSynd{
		rv:      &syndication.ResolvedVideo{TweetID: "123", VariantURL: "https://v/hi.mp4", Width: 1280, Height: 720, DurationMS: 11500},
		dlBytes: body,
	}
	ff := &fakeFFmpeg{md: &ffmpeg.VideoMetadata{Width: 1280, Height: 720, DurationSecs: 11.5, FrameRate: 25, Bitrate: 2_000_000}}
	s3 := &fakeS3{}
	out, err := newActs(t, s, ff, s3).DownloadAndStage(context.Background(), input())
	if err != nil {
		t.Fatalf("DownloadAndStage: %v", err)
	}
	if out.Outcome != OutcomePassed {
		t.Fatalf("outcome=%q reason=%q, want passed", out.Outcome, out.RejectReason)
	}
	if out.StagingKey == "" {
		t.Error("passed clip should have a staging key")
	}
	if _, ok := s3.uploaded[out.StagingKey]; !ok {
		t.Errorf("bytes not staged at %q", out.StagingKey)
	}
	if want := fmt.Sprintf("%x", md5.Sum(body)); out.MD5 != want {
		t.Errorf("md5=%s, want %s", out.MD5, want)
	}
}

func TestDownloadAndStage_PreFilterSkipsDownload(t *testing.T) {
	s := &fakeSynd{rv: &syndication.ResolvedVideo{TweetID: "1", Width: 720, Height: 1280, DurationMS: 24800}} // portrait
	out, err := newActs(t, s, &fakeFFmpeg{}, &fakeS3{}).DownloadAndStage(context.Background(), input())
	if err != nil {
		t.Fatal(err)
	}
	if out.Outcome != OutcomeRejected || !strings.Contains(out.RejectReason, "aspect_too_narrow") {
		t.Fatalf("out=%+v, want rejected aspect_too_narrow", out)
	}
	if s.downloadCalled {
		t.Error("pre-filter should have skipped the download entirely")
	}
}

func TestDownloadAndStage_GeoIsOutcomeNotError(t *testing.T) {
	s := &fakeSynd{rvErr: syndication.ErrGeoRestricted}
	out, err := newActs(t, s, &fakeFFmpeg{}, &fakeS3{}).DownloadAndStage(context.Background(), input())
	if err != nil {
		t.Fatalf("geo should be an outcome, not error: %v", err)
	}
	if out.Outcome != OutcomeRejected || out.RejectReason != "geo_restricted" {
		t.Fatalf("out=%+v, want rejected geo_restricted", out)
	}
}

func TestDownloadAndStage_RateLimitIsError(t *testing.T) {
	s := &fakeSynd{rvErr: syndication.ErrRateLimited}
	if _, err := newActs(t, s, &fakeFFmpeg{}, &fakeS3{}).DownloadAndStage(context.Background(), input()); err == nil {
		t.Fatal("rate-limit should be a (retryable) error, not an outcome")
	}
}

func TestDownloadAndStage_PostProbeReject(t *testing.T) {
	// Unknown resolve dims (0) → pre-filter falls through → download → probe
	// returns a too-low framerate → HardFilter rejects, no stage.
	s := &fakeSynd{rv: &syndication.ResolvedVideo{TweetID: "1"}, dlBytes: []byte("x")}
	ff := &fakeFFmpeg{md: &ffmpeg.VideoMetadata{Width: 1280, Height: 720, DurationSecs: 11, FrameRate: 12}}
	s3 := &fakeS3{}
	out, err := newActs(t, s, ff, s3).DownloadAndStage(context.Background(), input())
	if err != nil {
		t.Fatal(err)
	}
	if out.Outcome != OutcomeRejected || !strings.Contains(out.RejectReason, "framerate_too_low") {
		t.Fatalf("out=%+v, want rejected framerate_too_low", out)
	}
	if !s.downloadCalled {
		t.Error("unknown dims should have led to a download")
	}
	if len(s3.uploaded) != 0 {
		t.Error("rejected clip must not be staged")
	}
}

func TestDownloadAndStage_CorruptIsReject(t *testing.T) {
	s := &fakeSynd{rv: &syndication.ResolvedVideo{TweetID: "1", Width: 1280, Height: 720, DurationMS: 11000}, dlBytes: []byte("x")}
	ff := &fakeFFmpeg{mdErr: ffmpeg.ErrInputCorrupted}
	out, err := newActs(t, s, ff, &fakeS3{}).DownloadAndStage(context.Background(), input())
	if err != nil {
		t.Fatalf("corrupt should be an outcome: %v", err)
	}
	if out.Outcome != OutcomeRejected || out.RejectReason != "corrupt" {
		t.Fatalf("out=%+v, want rejected corrupt", out)
	}
}

// --- HashVideo ---

func TestHashVideo_Success(t *testing.T) {
	ff := &fakeFFmpeg{frames: []ffmpeg.Frame{
		{PositionSecs: 0, Data: tinyPNG(t)},
		{PositionSecs: 0.25, Data: tinyPNG(t)},
	}}
	s3 := &fakeS3{dlData: []byte("staged-bytes")}
	out, err := newActs(t, &fakeSynd{}, ff, s3).HashVideo(context.Background(), HashVideoInput{StagingKey: "staging/1/e/1.mp4"})
	if err != nil {
		t.Fatalf("HashVideo: %v", err)
	}
	if len(out.FrameHashes) != 2 {
		t.Fatalf("got %d hashes, want 2", len(out.FrameHashes))
	}
}

func TestHashVideo_FetchError(t *testing.T) {
	s3 := &fakeS3{dlErr: errors.New("garage down")}
	if _, err := newActs(t, &fakeSynd{}, &fakeFFmpeg{}, s3).HashVideo(context.Background(), HashVideoInput{StagingKey: "k"}); err == nil {
		t.Fatal("a fetch error should propagate (retryable)")
	}
}
