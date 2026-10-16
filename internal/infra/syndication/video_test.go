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
	"strings"
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

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

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

func TestResolveVideo_InvalidResponseTaxonomy(t *testing.T) {
	for _, handler := range []http.HandlerFunc{
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) },
		func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not-json")) },
	} {
		srv := httptest.NewServer(handler)
		_, err := newClient(t, srv.URL).ResolveVideo(context.Background(), goodTweet)
		srv.Close()
		if !errors.Is(err, syndication.ErrInvalidResponse) {
			t.Fatalf("want ErrInvalidResponse, got %v", err)
		}
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

// TestDownload_DenialClassification requires complete literal evidence, never
// a substring, path/uploader heuristic, or raw diagnostic in the returned error.
func TestDownload_DenialClassification(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
		body        string
		truncated   bool
		want        error
	}{
		{"explicit", "text/plain", "Geo Forbidden", false, syndication.ErrCDNGeoRestricted},
		{"whitespace-and-charset", "text/plain; charset=utf-8", "\r\nGeo Forbidden\n", false, syndication.ErrCDNGeoRestricted},
		{"empty", "text/plain", "", false, syndication.ErrCDNForbidden},
		{"unknown", "text/plain", "Forbidden PRIVATE_RESPONSE", false, syndication.ErrCDNForbidden},
		{"substring", "text/plain", "not Geo Forbidden", false, syndication.ErrCDNForbidden},
		{"html", "text/html", "<html>Geo Forbidden</html>", false, syndication.ErrCDNForbidden},
		{"json", "application/json", `{"error":"Geo Forbidden"}`, false, syndication.ErrCDNForbidden},
		{"binary", "application/octet-stream", "Geo Forbidden", false, syndication.ErrCDNForbidden},
		{"missing-content-type", "", "Geo Forbidden", false, syndication.ErrCDNForbidden},
		{"malformed-content-type", "text/plain; charset=", "Geo Forbidden", false, syndication.ErrCDNForbidden},
		{"oversized", "text/plain", "Geo Forbidden" + strings.Repeat(" ", 512), false, syndication.ErrCDNForbidden},
		{"truncated", "text/plain", "Geo Forbidden", true, syndication.ErrCDNForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				// An explicit empty value prevents net/http's type sniffing.
				w.Header()["Content-Type"] = []string{tc.contentType}
				if tc.truncated {
					w.Header().Set("Content-Length", "100")
				}
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			var dst bytes.Buffer
			n, err := newClient(t, "http://unused").Download(context.Background(),
				srv.URL+"/PRIVATE_PATH.mp4?token=PRIVATE_TOKEN", &dst)
			if !errors.Is(err, tc.want) {
				t.Fatalf("denial = %v, want %v", err, tc.want)
			}
			other := syndication.ErrCDNGeoRestricted
			if tc.want == other {
				other = syndication.ErrCDNForbidden
			}
			if errors.Is(err, other) || errors.Is(err, syndication.ErrGeoRestricted) {
				t.Fatalf("denial conflated with another class: %v", err)
			}
			if n != 0 || dst.Len() != 0 || strings.Contains(err.Error(), "PRIVATE_") || strings.Contains(err.Error(), "Geo Forbidden") {
				t.Fatalf("denial leaked media/response data: bytes=%d, written=%d, err=%v", n, dst.Len(), err)
			}
		})
	}
}

// TestDownload_DenialBodyHasItsOwnReadBound prevents a stalled error body from
// holding the media download slot until its two-minute transfer timeout.
func TestDownload_DenialBodyHasItsOwnReadBound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Geo Forbidden"))
		w.(http.Flusher).Flush()
		<-r.Context().Done() // no EOF: the apparent literal is incomplete evidence
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := time.Now()
	_, err := newClient(t, "http://unused").Download(ctx, srv.URL, &bytes.Buffer{})
	if !errors.Is(err, syndication.ErrCDNForbidden) || errors.Is(err, syndication.ErrCDNGeoRestricted) {
		t.Fatalf("stalled denial = %v, want unknown/retryable", err)
	}
	if ctx.Err() != nil || time.Since(started) > 5*time.Second {
		t.Fatalf("denial read exceeded its own bound: elapsed=%s, parent=%v", time.Since(started), ctx.Err())
	}
}

// TestDownload_DenialReadRespectsCancellation lets the caller release a stalled
// diagnostic read immediately, without waiting for the adapter's read bound.
func TestDownload_DenialReadRespectsCancellation(t *testing.T) {
	ready := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusForbidden)
		w.(http.Flusher).Flush()
		close(ready)
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newClient(t, "http://unused")
	done := make(chan error, 1)
	go func() {
		_, err := client.Download(ctx, srv.URL, &bytes.Buffer{})
		done <- err
	}()
	<-ready
	cancel()
	select {
	case err := <-done:
		if err == nil || errors.Is(err, syndication.ErrCDNGeoRestricted) {
			t.Fatalf("cancellation became success or geographic evidence: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("caller cancellation did not release the denial read")
	}
}

// TestDownload_GeoLiteralDoesNotOverrideOtherStatuses preserves successful
// byte transfers and the existing rate-limit taxonomy regardless of the body.
func TestDownload_GeoLiteralDoesNotOverrideOtherStatuses(t *testing.T) {
	for _, code := range []int{http.StatusOK, http.StatusTooManyRequests} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(code)
				_, _ = w.Write([]byte("Geo Forbidden"))
			}))
			defer srv.Close()
			var dst bytes.Buffer
			n, err := newClient(t, "http://unused").Download(context.Background(), srv.URL, &dst)
			if code == http.StatusOK {
				if err != nil || n != 13 || dst.String() != "Geo Forbidden" {
					t.Fatalf("success changed: bytes=%d, err=%v", n, err)
				}
			} else if !errors.Is(err, syndication.ErrRateLimited) || n != 0 || dst.Len() != 0 {
				t.Fatalf("429 changed: bytes=%d, err=%v", n, err)
			}
		})
	}
}

func TestDownload_StreamFailureTaxonomy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("fake-mp4"))
	}))
	defer srv.Close()
	_, err := newClient(t, "http://unused").Download(context.Background(), srv.URL+"/hi.mp4", failingWriter{})
	if !errors.Is(err, syndication.ErrCDNStream) {
		t.Fatalf("want ErrCDNStream, got %v", err)
	}
}
