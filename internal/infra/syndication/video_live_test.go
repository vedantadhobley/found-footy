//go:build live

// Live probe — excluded from the normal suite by the `live` build tag.
// Resolves + downloads a REAL tweet's video against the actual
// cdn.syndication.twimg.com + video.twimg.com hosts, proving the one thing
// the mocks can't: that the live CDN still returns the tweet-result shape we
// parse and accepts our cookieless browser headers. Drive it with a known
// goal-clip URL:
//
//	LIVE_TWEET_URL=https://x.com/user/status/<id> \
//	  go test -tags live -run TestLive_ResolveDownload -count=1 -v \
//	  ./internal/infra/syndication/
package syndication_test

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/syndication"
)

// liveUA — a real Firefox UA; the CDN is pickier about byte requests than
// the JSON endpoint, and a bot-shaped UA invites a 403.
const liveUA = "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0"

func TestLive_ResolveDownload(t *testing.T) {
	url := os.Getenv("LIVE_TWEET_URL")
	if url == "" {
		t.Skip("set LIVE_TWEET_URL to a real tweet URL that has a video")
	}
	ins, _ := newFixture()
	c, err := syndication.NewClient(config.SyndicationConfig{
		BaseURL:   "https://cdn.syndication.twimg.com",
		UserAgent: liveUA,
		Timeout:   15 * time.Second,
	}, ins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	rv, err := c.ResolveVideo(ctx, url)
	if err != nil {
		t.Fatalf("ResolveVideo(%s): %v", url, err)
	}
	t.Logf("resolved: tweet=%s bitrate=%d dims=%dx%d dur=%dms\n  variant=%s",
		rv.TweetID, rv.Bitrate, rv.Width, rv.Height, rv.DurationMS, rv.VariantURL)

	var buf bytes.Buffer
	n, err := c.Download(ctx, rv.VariantURL, &buf)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if n < 1024 {
		t.Fatalf("suspiciously small download: %d bytes", n)
	}
	// mp4 sanity: an ISO-BMFF file carries "ftyp" at bytes 4..8 of box 1.
	isMP4 := buf.Len() >= 8 && string(buf.Bytes()[4:8]) == "ftyp"
	head := buf.Bytes()[:min(16, buf.Len())]
	t.Logf("downloaded %d bytes, ftyp=%v, head=%x", n, isMP4, head)
	if !isMP4 {
		t.Errorf("downloaded bytes are not an mp4 (no ftyp box); head=%x", head)
	}
}
