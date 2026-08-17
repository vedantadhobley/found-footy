// Package apifootball is the api-sports.io REST adapter used by ingest and the
// active/staging poll workflows. All routes share authentication, rate-limit
// parsing, error classification, and instrumentation.
package apifootball

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Client is the API-Football HTTP client wrapper. Every method that
// hits a route uses the internal `do` helper so instrumentation + auth
// + rate-limit-header parsing happens uniformly.
type Client struct {
	http    *http.Client
	ins     *Instruments
	baseURL string
	apiKey  string
}

// NewClient builds a Client from cfg + probes /status to verify
// auth + connectivity before returning. Bounded by cfg.Timeout so a
// dead endpoint fails startup fast.
//
// The underlying http.Client's Timeout is set to cfg.Timeout — every
// subsequent call is bounded without callers needing to pass their own
// ctx timeout (though caller ctx cancellation still works).
func NewClient(ctx context.Context, cfg config.APIFootballConfig, ins *Instruments) (*Client, error) {
	if ins == nil {
		return nil, fmt.Errorf("apifootball.NewClient: Instruments is required (call RegisterMetrics first)")
	}
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("apifootball.NewClient: API_FOOTBALL_BASE_URL not set")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("apifootball.NewClient: API_FOOTBALL_KEY not set")
	}

	c := &Client{
		http:    &http.Client{Timeout: cfg.Timeout},
		ins:     ins,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:  cfg.APIKey,
	}

	// Probe /status. api-sports.io returns account info + quota.
	status, err := c.Status(ctx)
	if err != nil {
		ins.emitEvent(ctx, logging.LevelError, vocabulary.ActionAPIFootballFailed,
			"apifootball /status probe failed",
			logging.String("base_url", c.baseURL),
			logging.Err(err),
		)
		return nil, fmt.Errorf("apifootball.NewClient: /status probe: %w", err)
	}
	// Emit an INFO connected event mirroring pg/nats/s3/llm — every
	// adapter's startup should show a single _connected line so
	// operators can grep "action=*_connected" and see the substrate
	// come up.
	ins.emitEvent(ctx, logging.LevelInfo, vocabulary.ActionAPIFootballConnected,
		"apifootball client ready",
		logging.String("base_url", c.baseURL),
		logging.String("plan", status.Subscription.Plan),
		logging.Int("requests_today", status.Requests.Current),
		logging.Int("daily_limit", status.Requests.LimitDay),
	)
	return c, nil
}

// StatusResponse is a minimal projection of /status — enough to prove
// auth works + surface the daily quota. The remote returns more fields
// we don't consume yet.
type StatusResponse struct {
	Account struct {
		Email     string `json:"email"`
		FirstName string `json:"firstname"`
	} `json:"account"`
	Subscription struct {
		Plan   string `json:"plan"`
		End    string `json:"end"`
		Active bool   `json:"active"`
	} `json:"subscription"`
	Requests struct {
		Current  int `json:"current"`
		LimitDay int `json:"limit_day"`
	} `json:"requests"`
}

// Status calls /status and returns the parsed response.
func (c *Client) Status(ctx context.Context) (*StatusResponse, error) {
	var envelope struct {
		Response StatusResponse `json:"response"`
		Errors   interface{}    `json:"errors"`
	}
	if err := c.getJSON(ctx, "status", "/status", nil, &envelope); err != nil {
		return nil, err
	}
	return &envelope.Response, nil
}

// getJSON is the shared GET helper — builds the URL, injects auth,
// executes with instrumentation, and unmarshals into dst.
//
// endpoint is the bounded label emitted with metrics + logs (small set:
// status/fixtures/events/leagues/teams/etc). path is the URL path from
// the API-Football route table. params are query-string values.
func (c *Client) getJSON(
	ctx context.Context,
	endpoint string,
	path string,
	params url.Values,
	dst interface{},
) error {
	fullURL := c.baseURL + path
	if len(params) > 0 {
		fullURL += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return fmt.Errorf("apifootball.getJSON: build request: %w", err)
	}
	req.Header.Set("x-apisports-key", c.apiKey)
	req.Header.Set("Accept", "application/json")

	start := time.Now()
	resp, err := c.http.Do(req)
	elapsed := time.Since(start)
	c.ins.callDuration.WithLabelValues(endpoint).Observe(elapsed.Seconds())

	if err != nil {
		c.ins.calls.WithLabelValues(endpoint, "failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionAPIFootballFailed,
			"apifootball transport error",
			logging.String("endpoint", endpoint),
			logging.String("path", path),
			logging.Int64("elapsed_ms", elapsed.Milliseconds()),
			logging.Err(err),
		)
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	c.observeRateLimitHeaders(resp)

	if resp.StatusCode == http.StatusTooManyRequests {
		c.ins.calls.WithLabelValues(endpoint, "rate_limited").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionAPIFootballRateLimited,
			"apifootball 429 rate limited",
			logging.String("endpoint", endpoint),
			logging.String("path", path),
		)
		return fmt.Errorf("apifootball.getJSON %s: 429 rate limited", endpoint)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		c.ins.calls.WithLabelValues(endpoint, "failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionAPIFootballFailed,
			"apifootball non-2xx response",
			logging.String("endpoint", endpoint),
			logging.String("path", path),
			logging.Int("status", resp.StatusCode),
			logging.String("body_preview", string(body)),
		)
		return fmt.Errorf("apifootball.getJSON %s: %d %s", endpoint, resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	if dst != nil {
		if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
			c.ins.calls.WithLabelValues(endpoint, "failure").Inc()
			return fmt.Errorf("apifootball.getJSON %s: decode: %w", endpoint, err)
		}
	}
	c.ins.calls.WithLabelValues(endpoint, "success").Inc()
	c.ins.emitEvent(ctx, logging.LevelDebug, vocabulary.ActionAPIFootballCall,
		"apifootball call ok",
		logging.String("endpoint", endpoint),
		logging.String("path", path),
		logging.Int64("elapsed_ms", elapsed.Milliseconds()),
	)
	return nil
}

// observeRateLimitHeaders scrapes rate-limit + quota values from the
// response headers and updates the gauges. Per the vendor doc
// (docs/api-football/rate-limits.md), the API emits two windows with
// similarly-named-but-distinct headers:
//
//	x-ratelimit-requests-remaining — DAILY quota (name has "requests")
//	X-RateLimit-Remaining          — PER-MINUTE burst (no "requests")
//
// Header keys go through Go's canonical form (Textproto), so
// resp.Header.Get is case-insensitive but exact-key on the canonical.
func (c *Client) observeRateLimitHeaders(resp *http.Response) {
	if v := resp.Header.Get("x-ratelimit-requests-remaining"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.ins.dailyQuotaRemain.Set(float64(n))
		}
	}
	if v := resp.Header.Get("X-RateLimit-Remaining"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.ins.rateLimitRemain.Set(float64(n))
		}
	}
}
