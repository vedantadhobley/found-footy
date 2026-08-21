// Package twitter is the HTTP client for found-footy's own twitter
// container (Firefox through Playwright-Go). Not a public
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
	twittercontract "github.com/vedantadhobley/found-footy/internal/contract/twittersearch"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Client is the internal Twitter service HTTP wrapper.
type Client struct {
	http          *http.Client
	ins           *Instruments
	baseURL       string
	searchTimeout time.Duration
}

const maxSearchErrorBodyBytes = 64 << 10

// SearchRequest is the JSON body of a POST /search call.
type SearchRequest = twittercontract.SearchRequest

// SearchResponse is the parsed body of a processed POST /search, including a
// classified HTTP-200 unavailable page. Videos is the list of discovered tweet
// and video URLs; older service versions leave classification fields zero.
type SearchResponse = twittercontract.SearchResponse

// Verify forces the static Twitter service to perform a live authentication
// check and persist the resulting cookie snapshot. It is intentionally
// separate from Search: scheduled maintenance must fail when cookie persistence
// fails, while an event search should still return discovered candidates.
func (c *Client) Verify(ctx context.Context) error {
	callCtx, cancel := context.WithTimeout(ctx, c.searchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.baseURL+"/auth/verify", nil)
	if err != nil {
		return fmt.Errorf("twitter.Verify: build request: %w", err)
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	elapsed := time.Since(start)
	c.ins.callDuration.WithLabelValues("verify").Observe(elapsed.Seconds())
	if err != nil {
		c.ins.calls.WithLabelValues("verify", "failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionTwitterVerifyFailed,
			"twitter authentication verification transport error", logging.Err(err))
		return fmt.Errorf("twitter.Verify: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		c.ins.calls.WithLabelValues("verify", "failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionTwitterVerifyFailed,
			"twitter authentication verification failed",
			logging.Int("status", resp.StatusCode),
			logging.String("body_preview", string(respBody)),
		)
		return fmt.Errorf("twitter.Verify: %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	c.ins.calls.WithLabelValues("verify", "success").Inc()
	c.ins.emitEvent(ctx, logging.LevelInfo, vocabulary.ActionTwitterVerify,
		"twitter authentication verification succeeded",
		logging.Int64("elapsed_ms", elapsed.Milliseconds()),
	)
	return nil
}

// VideoRef is one discovered video from a Twitter search. Extra
// omitempty fields (Username, AgeMinutes) match the Twitter service's
// SearchResponse — they're populated by T/c but the older Python
// service leaves them zero, so JSON encoding skips them for backward
// compat.
type VideoRef = twittercontract.VideoRef

// SearchError preserves a structured non-2xx browser response. It contains
// bounded page evidence only; response bodies and credentials never escape.
type SearchError struct {
	StatusCode  int
	ErrorClass  string
	Message     string
	ResultState twittercontract.ResultState
	Evidence    twittercontract.SearchEvidence
}

func (e *SearchError) Error() string {
	return fmt.Sprintf("twitter.Search: %d %s (%s)", e.StatusCode, http.StatusText(e.StatusCode), e.ErrorClass)
}

// NewClient validates static configuration and constructs a recoverable HTTP
// client. It deliberately performs no network I/O: browser readiness can
// change independently of the worker, and every Search observes current state.
func NewClient(cfg config.TwitterConfig, ins *Instruments) (*Client, error) {
	if ins == nil {
		return nil, fmt.Errorf("twitter.NewClient: Instruments is required")
	}
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("twitter.NewClient: TWITTER_SERVICE_URL not set")
	}
	return &Client{
		// No client-level Timeout. Go's http.Client.Timeout is a hard cap on
		// the ENTIRE request and is NOT lifted by a per-request context — the
		// shorter of the two wins. A 10s cap here strangled every Search: a
		// real search takes 11–30s+ (empty-detection wait + stealth scroll
		// jitter), so nothing ever completed. Each method bounds itself via
		// context instead — Search uses SearchTimeout.
		// See decisions.md 2026-08-05.
		http:          &http.Client{},
		ins:           ins,
		baseURL:       strings.TrimRight(cfg.BaseURL, "/"),
		searchTimeout: cfg.SearchTimeout,
	}, nil
}

// Search sends a search request to a twitter instance and returns
// discovered video refs. addr targets a specific per-event instance
// (#160 Firefox fleet, e.g. http://ff-firefox-ev-<id>:8888); an empty
// addr uses the shared service (c.baseURL) — the pre-#160 path and the
// fallback when the fleet is disabled. Bounded by cfg.SearchTimeout.
func (c *Client) Search(ctx context.Context, addr string, req SearchRequest) (*SearchResponse, error) {
	base := c.baseURL
	if addr != "" {
		base = addr
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("twitter.Search: marshal request: %w", err)
	}
	callCtx, cancel := context.WithTimeout(ctx, c.searchTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost,
		base+"/search", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("twitter.Search: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := c.http.Do(httpReq)
	// Per-event instance unreachable (down / DNS / conn refused) — fall back to
	// the shared service ONCE so a wedged instance does not cost the goal its
	// clips. Only on a transport error (a non-2xx means the instance responded;
	// let the caller's 15-attempt loop handle that), and only when we were
	// dialing an instance (base != baseURL). audit P0-5.
	if err != nil && base != c.baseURL {
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionTwitterSearchFailed,
			"per-event instance unreachable; falling back to shared service",
			logging.String("addr", base),
			logging.Err(err),
		)
		if fbReq, ferr := http.NewRequestWithContext(callCtx, http.MethodPost,
			c.baseURL+"/search", bytes.NewReader(body)); ferr == nil {
			fbReq.Header.Set("Content-Type", "application/json")
			resp, err = c.http.Do(fbReq)
		}
	}
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
		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxSearchErrorBodyBytes))
		if readErr != nil {
			c.ins.calls.WithLabelValues("search", "failure").Inc()
			return nil, fmt.Errorf("twitter.Search: read error response: %w", readErr)
		}
		var errorBody twittercontract.SearchErrorBody
		if err := json.Unmarshal(respBody, &errorBody); err != nil {
			c.ins.calls.WithLabelValues("search", "failure").Inc()
			return nil, fmt.Errorf("twitter.Search: decode error response status %d: %w", resp.StatusCode, err)
		}
		metricOutcome := resultMetricOutcome(errorBody.ResultState, "failure")
		c.ins.calls.WithLabelValues("search", metricOutcome).Inc()
		fields := []logging.Field{
			logging.String("query", req.Query),
			logging.Int("status", resp.StatusCode),
			logging.String("error_class", errorBody.ErrorClass),
			logging.String("result_state", string(errorBody.ResultState)),
		}
		fields = append(fields, logSearchEvidence(errorBody.Evidence)...)
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionTwitterSearchFailed,
			"twitter search non-2xx response", fields...)
		return nil, &SearchError{
			StatusCode: resp.StatusCode, ErrorClass: errorBody.ErrorClass,
			Message: errorBody.Message, ResultState: errorBody.ResultState,
			Evidence: errorBody.Evidence,
		}
	}

	var out SearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		c.ins.calls.WithLabelValues("search", "failure").Inc()
		return nil, fmt.Errorf("twitter.Search: decode: %w", err)
	}
	if out.ResultState != "" && !out.ResultState.Known() {
		c.ins.calls.WithLabelValues("search", "failure").Inc()
		return nil, fmt.Errorf("twitter.Search: unknown result_state %q", out.ResultState)
	}
	c.ins.calls.WithLabelValues("search", resultMetricOutcome(out.ResultState, "success")).Inc()
	fields := []logging.Field{
		logging.String("query", req.Query),
		logging.String("result_state", string(out.ResultState)),
		logging.Int("videos_found", out.Count),
		logging.String("stop_reason", out.StopReason),
		logging.Int("scrolls", out.Scrolls),
		logging.Int("initial_articles", out.InitialArticles),
		logging.Int("tweets_parsed", out.TweetsParsed),
		logging.Int("video_tweets", out.VideoTweets),
		logging.Int64("elapsed_ms", elapsed.Milliseconds()),
	}
	fields = append(fields, logSearchEvidence(out.Evidence)...)
	level := logging.LevelInfo
	action := vocabulary.ActionTwitterSearch
	message := "twitter search completed"
	if out.ResultState.Known() && !out.ResultState.Usable() {
		level = logging.LevelWarn
		action = vocabulary.ActionTwitterSearchFailed
		message = "twitter search unavailable"
	}
	c.ins.emitEvent(ctx, level, action, message, fields...)
	return &out, nil
}

func resultMetricOutcome(state twittercontract.ResultState, fallback string) string {
	if state.Known() {
		return string(state)
	}
	return fallback
}

func logSearchEvidence(e twittercontract.SearchEvidence) []logging.Field {
	return []logging.Field{
		logging.String("final_url", e.FinalURL),
		logging.String("page_title", e.PageTitle),
		logging.Bool("app_shell", e.AppShell),
		logging.Bool("empty_state", e.EmptyState),
		logging.Bool("error_state", e.ErrorState),
		logging.Bool("timeline_seen", e.TimelineSeen),
		logging.Int("timeline_status", e.TimelineStatus),
		logging.String("timeline_failure", e.TimelineFailure),
		logging.String("rate_limit_limit", e.RateLimitLimit),
		logging.String("rate_limit_remaining", e.RateLimitRemain),
		logging.String("rate_limit_reset", e.RateLimitReset),
	}
}
