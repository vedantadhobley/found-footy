// video.go — resolve a tweet's best mp4 variant via the syndication
// tweet-result endpoint, then download the bytes directly from Twitter's
// CDN. Cookieless (no auth, no session): works for regular public goal
// clips; amplify/promoted content may 403 (revisit with cookies if it
// surfaces in production). This is the per-candidate download leaf. See
// docs/orchestration.md.
package syndication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"sort"
)

const (
	// minSnowflakeLen — Twitter snowflakes are ≥18 digits since ~2020;
	// shorter = an upstream truncation bug (2026-05-25 Paderborn-Wolfsburg
	// post-mortem). Reject early so it surfaces as truncated_snowflake
	// rather than a generic 404 at the syndication API.
	minSnowflakeLen = 18

	// Browser-like headers the syndication host + video CDN expect. The
	// CDN 403s a byte request without Referer + Origin (roadmap "load-
	// bearing"); they're static, not session-derived.
	refererHeader = "https://twitter.com/"
	originHeader  = "https://twitter.com"
)

// tweetIDRe pulls the numeric status id out of a tweet URL — handles
// x.com/user/status/N, x.com/i/status/N, twitter.com/..., ignoring any
// query/fragment.
var tweetIDRe = regexp.MustCompile(`/status/(\d+)`)

// tweetResult is the (partial) shape of cdn.syndication.twimg.com/tweet-result.
type tweetResult struct {
	MediaDetails []mediaDetail `json:"mediaDetails"`
	Video        *videoInfo    `json:"video"` // fallback path, often lacks bitrates
}

type mediaDetail struct {
	OriginalInfo originalInfo `json:"original_info"`
	VideoInfo    *videoInfo   `json:"video_info"`
}

type originalInfo struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type videoInfo struct {
	DurationMillis int       `json:"duration_millis"`
	Variants       []variant `json:"variants"`
}

type variant struct {
	Bitrate     int    `json:"bitrate"`
	ContentType string `json:"content_type"`
	Type        string `json:"type"`
	URL         string `json:"url"`
	Src         string `json:"src"`
}

func (v variant) isMP4() bool { return v.ContentType == "video/mp4" || v.Type == "video/mp4" }
func (v variant) src() string {
	if v.URL != "" {
		return v.URL
	}
	return v.Src
}

// ResolvedVideo is the metadata needed to pre-filter (on dimensions) then
// download a candidate.
type ResolvedVideo struct {
	TweetID    string
	VariantURL string // full https://video.twimg.com/... CDN url of the best mp4
	Bitrate    int
	Width      int // original dimensions (0 if the response omitted them)
	Height     int
	DurationMS int
}

// ResolveVideo extracts the tweet id, validates the snowflake, fetches the
// syndication tweet-result, and returns the best mp4 variant + original
// dimensions. Every failure class returns a typed (errors.Is-able) error.
func (c *Client) ResolveVideo(ctx context.Context, tweetURL string) (*ResolvedVideo, error) {
	m := tweetIDRe.FindStringSubmatch(tweetURL)
	if m == nil {
		return nil, fmt.Errorf("%w: %q", ErrMalformedTweetURL, tweetURL)
	}
	id := m[1]
	if len(id) < minSnowflakeLen {
		return nil, fmt.Errorf("%w: %s (%d digits)", ErrTruncatedSnowflake, id, len(id))
	}

	var tr tweetResult
	if err := c.fetchTweetResult(ctx, id, &tr); err != nil {
		return nil, err
	}

	variants, w, h, durMS := tr.collectVariants()
	url, bitrate, ok := bestMP4(variants)
	if !ok {
		return nil, fmt.Errorf("%w: tweet_id=%s", ErrNoVideoVariants, id)
	}
	return &ResolvedVideo{
		TweetID: id, VariantURL: url, Bitrate: bitrate,
		Width: w, Height: h, DurationMS: durMS,
	}, nil
}

// collectVariants prefers mediaDetails (carries bitrates + dimensions),
// falling back to the top-level video field. Returns all variants + the
// first media's dimensions + duration.
func (tr tweetResult) collectVariants() (vs []variant, width, height, durMS int) {
	for _, md := range tr.MediaDetails {
		if width == 0 && md.OriginalInfo.Width > 0 && md.OriginalInfo.Height > 0 {
			width, height = md.OriginalInfo.Width, md.OriginalInfo.Height
		}
		if md.VideoInfo != nil {
			if durMS == 0 {
				durMS = md.VideoInfo.DurationMillis
			}
			vs = append(vs, md.VideoInfo.Variants...)
		}
	}
	if len(vs) == 0 && tr.Video != nil {
		vs = tr.Video.Variants
		if durMS == 0 {
			durMS = tr.Video.DurationMillis
		}
	}
	return vs, width, height, durMS
}

// bestMP4 returns the highest-bitrate mp4 variant's url.
func bestMP4(variants []variant) (url string, bitrate int, ok bool) {
	var mp4 []variant
	for _, v := range variants {
		if v.isMP4() && v.src() != "" {
			mp4 = append(mp4, v)
		}
	}
	if len(mp4) == 0 {
		return "", 0, false
	}
	sort.SliceStable(mp4, func(i, j int) bool { return mp4[i].Bitrate > mp4[j].Bitrate })
	return mp4[0].src(), mp4[0].Bitrate, true
}

// fetchTweetResult GETs /tweet-result?id=<id>&token=x and maps status codes
// to the typed taxonomy.
func (c *Client) fetchTweetResult(ctx context.Context, id string, dst *tweetResult) error {
	url := c.baseURL + "/tweet-result?id=" + id + "&token=x"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("syndication.ResolveVideo: build request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", refererHeader)

	resp, err := c.http.Do(req)
	if err != nil {
		if isTimeout(err) {
			return fmt.Errorf("%w: tweet-result id=%s: %v", ErrCDNTimeout, id, err)
		}
		return fmt.Errorf("syndication.ResolveVideo: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if err := statusToErr(resp.StatusCode, id); err != nil {
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("syndication.ResolveVideo: decode: %w", err)
	}
	return nil
}

// Download streams the bytes at variantURL (a full video.twimg.com CDN url)
// into w, cookieless, with the browser headers the CDN requires (Referer +
// Origin — without them the CDN 403s). Returns bytes written.
func (c *Client) Download(ctx context.Context, variantURL string, w io.Writer) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, variantURL, nil)
	if err != nil {
		return 0, fmt.Errorf("syndication.Download: build request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Referer", refererHeader)
	req.Header.Set("Origin", originHeader)

	resp, err := c.dl.Do(req)
	if err != nil {
		if isTimeout(err) {
			return 0, fmt.Errorf("%w: download: %v", ErrCDNTimeout, err)
		}
		return 0, fmt.Errorf("syndication.Download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusForbidden {
		return 0, fmt.Errorf("%w: host=%s", ErrCDNForbidden, req.URL.Host)
	}
	if err := statusToErr(resp.StatusCode, ""); err != nil {
		return 0, err
	}
	n, err := io.Copy(w, resp.Body)
	if err != nil {
		return n, fmt.Errorf("syndication.Download: stream: %w", err)
	}
	return n, nil
}

// statusToErr maps a non-2xx status to the typed taxonomy. 2xx → nil.
func statusToErr(code int, id string) error {
	if code >= 200 && code < 300 {
		return nil
	}
	switch code {
	case http.StatusNotFound:
		return fmt.Errorf("%w: tweet_id=%s", ErrVideoNotAvailable, id)
	case http.StatusForbidden:
		return fmt.Errorf("%w: tweet_id=%s", ErrGeoRestricted, id)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: tweet_id=%s", ErrRateLimited, id)
	default:
		return fmt.Errorf("syndication: unexpected status %d %s (tweet_id=%s)", code, http.StatusText(code), id)
	}
}

// isTimeout reports whether err is a network or context timeout.
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
