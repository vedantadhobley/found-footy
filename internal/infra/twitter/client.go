// Package twitter is the HTTP client for found-footy's own twitter/
// container (Firefox+Selenium browser automation). Not a public
// Twitter API client — that lives at internal/infra/syndication/ (for
// guestpass content) and would live under a hypothetical third
// adapter for the official Twitter API if we ever wire it up.
package twitter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Client is the internal Twitter service HTTP wrapper.
type Client struct {
	http            *http.Client
	ins             *Instruments
	baseURL         string
	searchTimeout   time.Duration
	downloadTimeout time.Duration
}

// SearchRequest is the JSON body of a POST /search call.
type SearchRequest struct {
	Query         string   `json:"query"`
	ExcludeURLs   []string `json:"exclude_urls,omitempty"`
	MaxAgeMinutes int      `json:"max_age_minutes,omitempty"`
}

// SearchResponse is the parsed body of a successful POST /search.
// Videos is the list of discovered tweet + video URLs. Extra
// observability fields (StopReason, Scrolls) are omitempty for
// backward compat — Python service leaves them zero.
type SearchResponse struct {
	Status     string     `json:"status"`
	Videos     []VideoRef `json:"videos"`
	Count      int        `json:"count"`
	StopReason string     `json:"stop_reason,omitempty"`
	Scrolls    int        `json:"scrolls,omitempty"`
}

// VideoRef is one discovered video from a Twitter search. Extra
// omitempty fields (Username, AgeMinutes) match the Twitter service's
// SearchResponse — they're populated by T/c but the older Python
// service leaves them zero, so JSON encoding skips them for backward
// compat.
type VideoRef struct {
	TweetURL        string  `json:"tweet_url"`
	TweetText       string  `json:"tweet_text"`
	VideoPageURL    string  `json:"video_page_url"`
	DurationSeconds float64 `json:"duration_seconds"`
	Username        string  `json:"username,omitempty"`
	AgeMinutes      float64 `json:"age_minutes,omitempty"`
}

// NewClient — validates config + probes /health so a dead twitter
// container fails startup rather than surfacing on first Search.
func NewClient(ctx context.Context, cfg config.TwitterConfig, ins *Instruments) (*Client, error) {
	if ins == nil {
		return nil, fmt.Errorf("twitter.NewClient: Instruments is required")
	}
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("twitter.NewClient: TWITTER_SERVICE_URL not set")
	}
	c := &Client{
		http:            &http.Client{Timeout: 10 * time.Second}, // probe only; per-method calls override
		ins:             ins,
		baseURL:         strings.TrimRight(cfg.BaseURL, "/"),
		searchTimeout:   cfg.SearchTimeout,
		downloadTimeout: cfg.DownloadTimeout,
	}
	if err := c.probeHealth(ctx); err != nil {
		ins.emitEvent(ctx, logging.LevelError, vocabulary.ActionTwitterConnectFailed,
			"twitter service /health probe failed",
			logging.String("base_url", c.baseURL),
			logging.Err(err),
		)
		return nil, fmt.Errorf("twitter.NewClient: /health probe: %w", err)
	}
	ins.emitEvent(ctx, logging.LevelInfo, vocabulary.ActionTwitterConnected,
		"twitter service ready",
		logging.String("base_url", c.baseURL),
	)
	return c, nil
}

func (c *Client) probeHealth(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("health probe: %d %s: %s", resp.StatusCode, http.StatusText(resp.StatusCode), body)
	}
	return nil
}

// Search sends a search request to the twitter container and returns
// discovered video refs. Bounded by cfg.SearchTimeout.
func (c *Client) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("twitter.Search: marshal request: %w", err)
	}
	callCtx, cancel := context.WithTimeout(ctx, c.searchTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost,
		c.baseURL+"/search", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("twitter.Search: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := c.http.Do(httpReq)
	elapsed := time.Since(start)
	c.ins.callDuration.WithLabelValues("search").Observe(elapsed.Seconds())

	if err != nil {
		c.ins.calls.WithLabelValues("search", "failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionTwitterSearchFailed,
			"twitter search transport error",
			logging.String("query", req.Query),
			logging.Err(err),
		)
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		c.ins.calls.WithLabelValues("search", "failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionTwitterSearchFailed,
			"twitter search non-2xx response",
			logging.String("query", req.Query),
			logging.Int("status", resp.StatusCode),
			logging.String("body_preview", string(respBody)),
		)
		return nil, fmt.Errorf("twitter.Search: %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	var out SearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		c.ins.calls.WithLabelValues("search", "failure").Inc()
		return nil, fmt.Errorf("twitter.Search: decode: %w", err)
	}
	c.ins.calls.WithLabelValues("search", "success").Inc()
	c.ins.emitEvent(ctx, logging.LevelInfo, vocabulary.ActionTwitterSearch,
		"twitter search ok",
		logging.String("query", req.Query),
		logging.Int("videos_found", out.Count),
		logging.Int64("elapsed_ms", elapsed.Milliseconds()),
	)
	return &out, nil
}
