// Mock tests for the video resolve + download path. httptest stands in for
// both the tweet-result API and the video CDN — the logic (id extraction,
// snowflake check, variant selection, error taxonomy, headers) is covered
// here; the real-CDN path is validated separately by a live probe.
package syndication_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/syndication"
)

func newClient(t *testing.T, baseURL string) *syndication.Client {
	t.Helper()
	ins, _ := newFixture()
	c, err := syndication.NewClient(config.SyndicationConfig{
		BaseURL: baseURL, UserAgent: "test-ua", Timeout: 5 * time.Second,
	}, ins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

const goodTweet = "https://x.com/user/status/1790000000000000000"

func TestResolveVideo_MalformedURL(t *testing.T) {
	c := newClient(t, "http://unused")
	if _, err := c.ResolveVideo(context.Background(), "https://x.com/user/no-status"); !errors.Is(err, syndication.ErrMalformedTweetURL) {
		t.Fatalf("want ErrMalformedTweetURL, got %v", err)
	}
}

func TestResolveVideo_TruncatedSnowflake(t *testing.T) {
	c := newClient(t, "http://unused")
	if _, err := c.ResolveVideo(context.Background(), "https://x.com/i/status/12345"); !errors.Is(err, syndication.ErrTruncatedSnowflake) {
		t.Fatalf("want ErrTruncatedSnowflake, got %v", err)
	}
}

func TestResolveVideo_BestVariantAndDims(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "x" {
			t.Errorf("missing token=x; got %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{
			"mediaDetails": [{
				"original_info": {"width": 1280, "height": 720},
				"video_info": {"duration_millis": 12000, "variants": [
					{"bitrate": 256000, "content_type": "video/mp4", "url": "https://video.twimg.com/lo.mp4"},
					{"bitrate": 2176000, "content_type": "video/mp4", "url": "https://video.twimg.com/hi.mp4"},
					{"content_type": "application/x-mpegURL", "url": "https://video.twimg.com/pl.m3u8"}
				]}
			}]
		}`))
	}))
	defer srv.Close()

	rv, err := newClient(t, srv.URL).ResolveVideo(context.Background(), goodTweet)
	if err != nil {
		t.Fatalf("ResolveVideo: %v", err)
	}
	if rv.VariantURL != "https://video.twimg.com/hi.mp4" || rv.Bitrate != 2176000 {
		t.Errorf("want highest-bitrate mp4, got %q @ %d", rv.VariantURL, rv.Bitrate)
	}
	if rv.Width != 1280 || rv.Height != 720 || rv.DurationMS != 12000 {
		t.Errorf("meta = %dx%d %dms, want 1280x720 12000ms", rv.Width, rv.Height, rv.DurationMS)
	}
	if rv.TweetID != "1790000000000000000" {
		t.Errorf("TweetID = %q", rv.TweetID)
	}
}

func TestResolveVideo_NoMP4Variants(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"mediaDetails":[{"video_info":{"variants":[
			{"content_type":"application/x-mpegURL","url":"https://video.twimg.com/pl.m3u8"}]}}]}`))
	}))
	defer srv.Close()
	if _, err := newClient(t, srv.URL).ResolveVideo(context.Background(), goodTweet); !errors.Is(err, syndication.ErrNoVideoVariants) {
		t.Fatalf("want ErrNoVideoVariants, got %v", err)
	}
}

func TestResolveVideo_StatusTaxonomy(t *testing.T) {
	for _, tc := range []struct {
		code int
		want error
	}{
		{http.StatusNotFound, syndication.ErrVideoNotAvailable},
		{http.StatusForbidden, syndication.ErrGeoRestricted},
		{http.StatusTooManyRequests, syndication.ErrRateLimited},
	} {
		t.Run(fmt.Sprintf("status-%d", tc.code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.code) }))
			defer srv.Close()
			if _, err := newClient(t, srv.URL).ResolveVideo(context.Background(), goodTweet); !errors.Is(err, tc.want) {
				t.Fatalf("status %d: want %v, got %v", tc.code, tc.want, err)
			}
		})
	}
}

func TestResolveVideo_FallbackVideoField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"video":{"variants":[
			{"bitrate":832000,"type":"video/mp4","src":"https://video.twimg.com/fallback.mp4"}]}}`))
	}))
	defer srv.Close()
	rv, err := newClient(t, srv.URL).ResolveVideo(context.Background(), goodTweet)
	if err != nil {
		t.Fatalf("ResolveVideo: %v", err)
	}
	if rv.VariantURL != "https://video.twimg.com/fallback.mp4" {
		t.Errorf("fallback video-field src not selected: %q", rv.VariantURL)
	}
}

func TestDownload_SuccessSetsHeaders(t *testing.T) {
	const body = "fake-mp4-bytes-\x00\x01\x02"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") == "" || r.Header.Get("Origin") == "" {
			t.Errorf("download missing Referer/Origin: %v", r.Header)
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	n, err := newClient(t, "http://unused").Download(context.Background(), srv.URL+"/hi.mp4", &buf)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if int(n) != len(body) || buf.String() != body {
		t.Errorf("got %d bytes %q, want %d %q", n, buf.String(), len(body), body)
	}
}

func TestDownload_CDN403IsTransientForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer srv.Close()
	_, err := newClient(t, "http://unused").Download(context.Background(), srv.URL+"/hi.mp4", &bytes.Buffer{})
	if !errors.Is(err, syndication.ErrCDNForbidden) {
		t.Fatalf("want ErrCDNForbidden on CDN 403, got %v", err)
	}
	if errors.Is(err, syndication.ErrGeoRestricted) {
		t.Fatalf("CDN 403 must not be terminal ErrGeoRestricted: %v", err)
	}
}
