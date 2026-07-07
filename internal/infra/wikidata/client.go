// Package wikidata is the Wikidata SPARQL adapter. Public endpoint,
// no auth, but the User-Agent header must identify the caller per
// Wikidata's policy. Backs the RAG team-alias pipeline.
package wikidata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Client is the Wikidata SPARQL HTTP wrapper.
type Client struct {
	http      *http.Client
	ins       *Instruments
	endpoint  string
	userAgent string
}

// SPARQLResult is the standard Wikidata SPARQL JSON envelope. Domain
// code walks Results.Bindings to extract bound variables.
type SPARQLResult struct {
	Head struct {
		Vars []string `json:"vars"`
	} `json:"head"`
	Results struct {
		Bindings []map[string]struct {
			Type    string `json:"type"`
			Value   string `json:"value"`
			XMLLang string `json:"xml:lang,omitempty"`
		} `json:"bindings"`
	} `json:"results"`
}

// NewClient — validates config; no probe (Wikidata has no lightweight
// health endpoint we'd want to hit at every binary startup).
func NewClient(cfg config.WikidataConfig, ins *Instruments) (*Client, error) {
	if ins == nil {
		return nil, fmt.Errorf("wikidata.NewClient: Instruments is required")
	}
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("wikidata.NewClient: WIKIDATA_ENDPOINT not set")
	}
	if cfg.UserAgent == "" {
		return nil, fmt.Errorf("wikidata.NewClient: WIKIDATA_USER_AGENT not set (Wikidata policy requires identification)")
	}
	return &Client{
		http:      &http.Client{Timeout: cfg.Timeout},
		ins:       ins,
		endpoint:  strings.TrimRight(cfg.Endpoint, "/"),
		userAgent: cfg.UserAgent,
	}, nil
}

// Query runs a SPARQL query and returns the parsed result envelope.
// Accept: application/sparql-results+json is set automatically.
func (c *Client) Query(ctx context.Context, sparql string) (*SPARQLResult, error) {
	params := url.Values{"query": {sparql}, "format": {"json"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("wikidata.Query: build request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/sparql-results+json")

	start := time.Now()
	resp, err := c.http.Do(req)
	elapsed := time.Since(start)
	c.ins.queryLatency.WithLabelValues().Observe(elapsed.Seconds())

	if err != nil {
		c.ins.queries.WithLabelValues("failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionWikidataQueryFailed,
			"wikidata transport error",
			logging.Int64("elapsed_ms", elapsed.Milliseconds()),
			logging.Err(err),
		)
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		c.ins.queries.WithLabelValues("failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionWikidataQueryFailed,
			"wikidata non-2xx response",
			logging.Int("status", resp.StatusCode),
			logging.String("body_preview", string(body)),
		)
		return nil, fmt.Errorf("wikidata.Query: %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	var out SPARQLResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		c.ins.queries.WithLabelValues("failure").Inc()
		return nil, fmt.Errorf("wikidata.Query: decode: %w", err)
	}
	c.ins.queries.WithLabelValues("success").Inc()
	c.ins.emitEvent(ctx, logging.LevelDebug, vocabulary.ActionWikidataQuery,
		"wikidata query ok",
		logging.Int64("elapsed_ms", elapsed.Milliseconds()),
		logging.Int("bindings", len(out.Results.Bindings)),
	)
	return &out, nil
}
