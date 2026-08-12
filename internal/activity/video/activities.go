// activities.go — the V-phase per-candidate activities (rung 3b):
//
//   DownloadAndStage — external Twitter fetch (once) → pre-download filter →
//     download to worker-local scratch (md5 inline) → probe → hard-filter →
//     stage the raw bytes to Garage. Returns md5 + staging pointer + metadata.
//   HashVideo — internal Garage fetch → dense frame extract → dHash each.
//
// Split at the staging boundary so a HashVideo retry re-fetches from Garage
// (cheap, internal) rather than re-hitting Twitter. Terminal-but-normal
// results (reject / geo / deleted) are OUTCOMES (nil error); transients
// (rate-limit / timeout / blip) are errors the workflow's RetryPolicy bounds
// — matching the codebase's "plain errors + RetryPolicy" convention (no
// NonRetryableApplicationError needed).
package video

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"

	"github.com/vedantadhobley/found-footy/internal/config"
	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
	"github.com/vedantadhobley/found-footy/internal/infra/ffmpeg"
	"github.com/vedantadhobley/found-footy/internal/infra/syndication"
)

// --- dep interfaces (interface-shaped so tests inject fakes) ---

type syndicationClient interface {
	ResolveVideo(ctx context.Context, tweetURL string) (*syndication.ResolvedVideo, error)
	Download(ctx context.Context, variantURL string, w io.Writer) (int64, error)
}

type ffmpegClient interface {
	ProbeMetadata(ctx context.Context, path string) (*ffmpeg.VideoMetadata, error)
	ExtractDenseFrames(ctx context.Context, path string, intervalSecs float64, quality int, onFrame func(ffmpeg.Frame) error) error
}

type s3Client interface {
	Upload(ctx context.Context, key string, body io.Reader, size int64, contentType string) error
	Download(ctx context.Context, key string) (io.ReadCloser, int64, error)
}

// Activities bundles the V-phase per-candidate activities + their deps.
type Activities struct {
	Syndication syndicationClient
	FFmpeg      ffmpegClient
	S3          s3Client

	ScratchDir        string
	StagingPrefix     string
	Thresholds        dvideo.FilterThresholds
	FrameIntervalSecs float64
}

// ThresholdsFromConfig maps env-driven config onto the domain type, so the
// worker can populate Activities.Thresholds without importing the domain.
func ThresholdsFromConfig(c config.HardFilterConfig) dvideo.FilterThresholds {
	return dvideo.FilterThresholds{
		MinDurationSecs: c.MinDurationSecs,
		MaxDurationSecs: c.MaxDurationSecs,
		MinAspectRatio:  c.MinAspectRatio,
		MaxAspectRatio:  c.MaxAspectRatio,
		MinShortEdge:    c.MinShortEdge,
		MinFrameRate:    c.MinFrameRate,
	}
}

// DownloadAndStage fetches + fingerprints one candidate. It only returns an
// error on a transient failure the workflow should retry; every definitive
// verdict (passed / rejected) comes back as a nil-error Outcome.
func (a *Activities) DownloadAndStage(ctx context.Context, in DownloadAndStageInput) (DownloadAndStageOutput, error) {
	var out DownloadAndStageOutput

	rv, err := a.Syndication.ResolveVideo(ctx, in.TweetURL)
	if err != nil {
		if reason, terminal := classifySyndication(err); terminal {
			return rejected(reason), nil
		}
		return out, fmt.Errorf("video.DownloadAndStage: resolve: %w", err)
	}

	// Pre-download filter — reject portrait / compilation with 0 bytes fetched.
	if reason, ok := dvideo.PreFilter(rv.Width, rv.Height, float64(rv.DurationMS)/1000, a.Thresholds); !ok {
		return rejected(reason), nil
	}

	dir := filepath.Join(a.ScratchDir, fmt.Sprint(in.FixtureID), in.EventID.String(), rv.TweetID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return out, fmt.Errorf("video.DownloadAndStage: scratch: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	srcPath := filepath.Join(dir, "source.mp4")

	md5hex, size, derr := a.downloadTo(ctx, rv.VariantURL, srcPath)
	if derr != nil {
		if reason, terminal := classifySyndication(derr); terminal {
			return rejected(reason), nil
		}
		return out, fmt.Errorf("video.DownloadAndStage: download: %w", derr)
	}
	out.MD5, out.SizeBytes = md5hex, size

	meta, err := a.FFmpeg.ProbeMetadata(ctx, srcPath)
	if err != nil {
		if errors.Is(err, ffmpeg.ErrInputCorrupted) {
			return rejectedMeta(out, "corrupt"), nil
		}
		return out, fmt.Errorf("video.DownloadAndStage: probe: %w", err)
	}
	out.Width, out.Height = meta.Width, meta.Height
	out.DurationMS = int(meta.DurationSecs * 1000)
	out.Bitrate, out.FrameRate = meta.Bitrate, meta.FrameRate

	if reason, ok := dvideo.HardFilter(meta.Width, meta.Height, meta.DurationSecs, meta.FrameRate, a.Thresholds); !ok {
		return rejectedMeta(out, reason), nil
	}

	key := a.stagingKey(in.FixtureID, in.EventID, rv.TweetID)
	if err := a.uploadFile(ctx, srcPath, key, size); err != nil {
		return out, fmt.Errorf("video.DownloadAndStage: stage: %w", err)
	}
	out.Outcome, out.StagingKey = OutcomePassed, key
	return out, nil
}

// HashVideo fetches staged bytes from Garage, dense-extracts, and dHashes
// each frame. Retries re-fetch from Garage (internal) — never Twitter.
func (a *Activities) HashVideo(ctx context.Context, in HashVideoInput) (HashVideoOutput, error) {
	var out HashVideoOutput
	if err := os.MkdirAll(a.ScratchDir, 0o755); err != nil {
		return out, fmt.Errorf("video.HashVideo: scratch: %w", err)
	}
	dir, err := os.MkdirTemp(a.ScratchDir, "hash-*")
	if err != nil {
		return out, fmt.Errorf("video.HashVideo: tmp: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	vidPath := filepath.Join(dir, "video.mp4")
	if err := a.fetchStaged(ctx, in.StagingKey, vidPath); err != nil {
		return out, fmt.Errorf("video.HashVideo: fetch %s: %w", in.StagingKey, err)
	}

	// Stream frames: hash each PNG to an 8-byte dHash the moment it's parsed
	// off ffmpeg's stdout, so peak memory is ~one frame, not the whole clip's
	// worth of PNGs (~300 MB for a 90 s 1080p clip × the concurrency cap).
	hashes := make([]uint64, 0, 256)
	hb := newHeartbeater(ctx, 5*time.Second)
	err = a.FFmpeg.ExtractDenseFrames(ctx, vidPath, a.FrameIntervalSecs, 0, func(fr ffmpeg.Frame) error {
		// Heartbeat on frame progress so a long extraction runs to its
		// StartToClose budget instead of being killed at 30s (#184).
		hb.tick()
		h, herr := dvideo.DHashPNG(fr.Data)
		if herr != nil {
			return nil // skip one unreadable frame rather than fail the clip
		}
		hashes = append(hashes, h)
		return nil
	})
	if err != nil {
		return out, fmt.Errorf("video.HashVideo: extract: %w", err)
	}
	out.FrameHashes = hashes
	return out, nil
}

// --- internal helpers ---

// heartbeater emits an activity heartbeat at most once per interval, driven by
// real progress (a frame parsed, a chunk written). This makes the activity's
// HeartbeatTimeout track genuine liveness: a stalled op (no frames / no bytes)
// stops heartbeating and Temporal fails it fast, while a slow-but-progressing
// op runs to its full StartToClose budget instead of being killed at 30s
// (#184). No-op outside an activity context — the unit tests call the
// activities directly with context.Background(), where RecordHeartbeat panics;
// activity.IsActivity gates that at construction.
type heartbeater struct {
	ctx   context.Context
	every time.Duration
	last  time.Time
	on    bool
}

func newHeartbeater(ctx context.Context, every time.Duration) *heartbeater {
	return &heartbeater{ctx: ctx, every: every, on: activity.IsActivity(ctx)}
}

// tick heartbeats if we're in an activity and at least `every` has elapsed
// since the last one. Cheap to call on every unit of progress.
func (h *heartbeater) tick() {
	if !h.on || time.Since(h.last) < h.every {
		return
	}
	h.last = time.Now()
	activity.RecordHeartbeat(h.ctx)
}

// hbWriter ticks a heartbeater as bytes flow through it, so a long-but-
// progressing download keeps the activity alive (#184).
type hbWriter struct {
	w  io.Writer
	hb *heartbeater
}

func (hw hbWriter) Write(p []byte) (int, error) {
	hw.hb.tick()
	return hw.w.Write(p)
}

// downloadTo streams the variant into dstPath, computing md5 inline.
func (a *Activities) downloadTo(ctx context.Context, variantURL, dstPath string) (md5hex string, size int64, err error) {
	f, err := os.Create(dstPath)
	if err != nil {
		return "", 0, err
	}
	h := md5.New()
	// Heartbeat on byte progress so a large/slow CDN fetch (>30s) isn't killed
	// by the activity's HeartbeatTimeout mid-download (#184).
	hb := newHeartbeater(ctx, 5*time.Second)
	n, derr := a.Syndication.Download(ctx, variantURL, hbWriter{w: io.MultiWriter(f, h), hb: hb})
	cerr := f.Close()
	if derr != nil {
		return "", 0, derr
	}
	if cerr != nil {
		return "", 0, cerr
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func (a *Activities) uploadFile(ctx context.Context, srcPath, key string, size int64) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return a.S3.Upload(ctx, key, f, size, "video/mp4")
}

func (a *Activities) fetchStaged(ctx context.Context, key, dstPath string) error {
	rc, _, err := a.S3.Download(ctx, key)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	f, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, rc); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// stagingKey mirrors the local scratch layout: <prefix>/<fixture>/<event>/<tweet>.mp4.
func (a *Activities) stagingKey(fixtureID int64, eventID uuid.UUID, tweetID string) string {
	return path.Join(a.StagingPrefix, fmt.Sprint(fixtureID), eventID.String(), tweetID+".mp4")
}

func rejected(reason string) DownloadAndStageOutput {
	return DownloadAndStageOutput{Outcome: OutcomeRejected, RejectReason: reason}
}

// rejectedMeta keeps the probed metadata already set on out (for the
// candidate record) while marking the terminal reject.
func rejectedMeta(out DownloadAndStageOutput, reason string) DownloadAndStageOutput {
	out.Outcome, out.RejectReason = OutcomeRejected, reason
	return out
}

// classifySyndication maps a syndication error to a terminal reject reason,
// or terminal=false for transient errors the workflow should retry.
func classifySyndication(err error) (reason string, terminal bool) {
	switch {
	case errors.Is(err, syndication.ErrMalformedTweetURL):
		return "malformed_url", true
	case errors.Is(err, syndication.ErrTruncatedSnowflake):
		return "truncated_snowflake", true
	case errors.Is(err, syndication.ErrVideoNotAvailable):
		return "not_available", true
	case errors.Is(err, syndication.ErrGeoRestricted):
		return "geo_restricted", true
	case errors.Is(err, syndication.ErrNoVideoVariants):
		return "no_video_variant", true
	default: // ErrRateLimited / ErrCDNTimeout / unexpected → transient
		return "", false
	}
}
