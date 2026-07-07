// Package syndication is the Twitter guestpass adapter. Public
// endpoints that return tweet content (JSON payloads) without login,
// used as a low-cost fallback before the browser-automation twitter/
// container gets involved.
package syndication

import (
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

// Client is the syndication HTTP wrapper.
type Client struct {
	http      *http.Client
	ins       *Instruments
	baseURL   string
	userAgent string
}

// NewClient — validates config; no probe (no lightweight health path
// on the syndication host).
func NewClient(cfg config.SyndicationConfig, ins *Instruments) (*Client, error) {
	if ins == nil {
		return nil, fmt.Errorf("syndication.NewClient: Instruments is required")
	}
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("syndication.NewClient: SYNDICATION_BASE_URL not set")
	}
	if cfg.UserAgent == "" {
		return nil, fmt.Errorf("syndication.NewClient: SYNDICATION_USER_AGENT not set")
	}
	return &Client{
		http:      &http.Client{Timeout: cfg.Timeout},
		ins:       ins,
		baseURL:   strings.TrimRight(cfg.BaseURL, "/"),
		userAgent: cfg.UserAgent,
	}, nil
}

// FetchJSON GETs the given path and unmarshals the response into dst.
// path should start with "/". Real domain methods (fetch a tweet,
// resolve a video URL) land as activities need them.
func (c *Client) FetchJSON(ctx context.Context, path string, dst interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("syndication.FetchJSON: build request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	start := time.Now()
	resp, err := c.http.Do(req)
	elapsed := time.Since(start)
	c.ins.fetchLatency.WithLabelValues().Observe(elapsed.Seconds())

	if err != nil {
		c.ins.fetches.WithLabelValues("failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionSyndicationFetchFailed,
			"syndication transport error",
			logging.String("path", path),
			logging.Int64("elapsed_ms", elapsed.Milliseconds()),
			logging.Err(err),
		)
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		c.ins.fetches.WithLabelValues("failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionSyndicationFetchFailed,
			"syndication non-2xx response",
			logging.String("path", path),
			logging.Int("status", resp.StatusCode),
			logging.String("body_preview", string(body)),
		)
		return fmt.Errorf("syndication.FetchJSON: %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	if dst != nil {
		if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
			c.ins.fetches.WithLabelValues("failure").Inc()
			return fmt.Errorf("syndication.FetchJSON: decode: %w", err)
		}
	}
	c.ins.fetches.WithLabelValues("success").Inc()
	c.ins.emitEvent(ctx, logging.LevelDebug, vocabulary.ActionSyndicationFetch,
		"syndication fetch ok",
		logging.String("path", path),
		logging.Int64("elapsed_ms", elapsed.Milliseconds()),
	)
	return nil
}
