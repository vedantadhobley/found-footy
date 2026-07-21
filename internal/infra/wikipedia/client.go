// Wikipedia MediaWiki API client. One method — SearchAndResolve —
// composes CirrusSearch full-text lookup with Wikidata QID extraction
// via `pageprops.wikibase_item`. See doc.go for design rationale.
package wikipedia

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Client is the Wikipedia MediaWiki API wrapper.
type Client struct {
	http      *http.Client
	ins       *Instruments
	host      string
	userAgent string
}

// Hit is one candidate returned by SearchAndResolve. Fields are
// intentionally minimal — the resolver picks the top P31-passing hit
// and consumes its WikidataQID; the rest is for observability.
type Hit struct {
	Title       string // Wikipedia article title
	WikidataQID string // pageprops.wikibase_item; may be "" if the article lacks a Wikidata sitelink
	PageID      int
	Index       int // Wikipedia's rank position (1-indexed, lower = better)
}

// SearchOpts tunes SearchAndResolve. Zero-value gives Language=en,
// Limit=10.
type SearchOpts struct {
	// Language is currently unused — this v1 hits the English
	// Wikipedia (cfg.Host). Retained in the API for a future
	// country-language fallback per proposal Approach C step 5.
	Language string
	// Limit bounds candidate hits. gsrlimit accepts 1..500; 10 is
	// enough headroom to survive P31-filtering even when a league
	// article and a couple of season articles sit above the target.
	Limit int
}

// NewClient constructs a Client. Validates config; no probe (Wikipedia
// has no cheap health endpoint we'd want to hit at every startup).
func NewClient(cfg config.WikipediaConfig, ins *Instruments) (*Client, error) {
	if ins == nil {
		return nil, fmt.Errorf("wikipedia.NewClient: Instruments is required")
	}
	if cfg.Host == "" {
		return nil, fmt.Errorf("wikipedia.NewClient: WIKIPEDIA_HOST not set")
	}
	if cfg.UserAgent == "" {
		return nil, fmt.Errorf("wikipedia.NewClient: WIKIPEDIA_USER_AGENT not set (Wikimedia policy requires identification)")
	}
	return &Client{
		http:      &http.Client{Timeout: cfg.Timeout},
		ins:       ins,
		host:      strings.TrimRight(cfg.Host, "/"),
		userAgent: cfg.UserAgent,
	}, nil
}

// searchResponse mirrors just the MediaWiki API fields we consume from
// the `generator=search + prop=pageprops` compound query.
//
// The pages map is keyed by pageID; each entry carries a rank `index`
// (1-indexed, injected by generator=search) and a `pageprops` bag from
// which we pull `wikibase_item`. Sorting pages by `index` restores
// Wikipedia's original ranking (map iteration order is randomized in Go).
type searchResponse struct {
	Query struct {
		Pages map[string]struct {
			PageID    int    `json:"pageid"`
			Title     string `json:"title"`
			Index     int    `json:"index"`
			PageProps struct {
				WikibaseItem string `json:"wikibase_item"`
			} `json:"pageprops"`
		} `json:"pages"`
	} `json:"query"`
	Error *struct {
		Code string `json:"code"`
		Info string `json:"info"`
	} `json:"error,omitempty"`
}

// SearchAndResolve runs a CirrusSearch full-text query and returns each
// hit with its Wikidata QID (from `pageprops.wikibase_item`) in a single
// HTTP request.
//
// Query construction — the caller composes the query with the strongest
// disambiguating context available (see the entity-resolution proposal
// § "The general recipe"):
//   - Clubs: `{name} {country} football club`
//   - Nationals: `{country} men's national football team`
//
// Returns hits sorted by Wikipedia's own ranking (position 1 first).
// Hits without a wikibase_item come through with empty WikidataQID —
// callers should filter these before passing to the P31 batch verify.
func (c *Client) SearchAndResolve(ctx context.Context, query string, opts SearchOpts) ([]Hit, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		// gsrlimit accepts 1..500 but 50 is more than enough — beyond
		// that the tail is noise.
		limit = 50
	}

	params := url.Values{
		"action":     {"query"},
		"generator":  {"search"},
		"gsrsearch":  {query},
		"gsrlimit":   {strconv.Itoa(limit)},
		"gsrnamespace": {"0"}, // articles only; skips File:, Category:, etc.
		"prop":       {"pageprops"},
		"format":     {"json"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.host+"/w/api.php?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("wikipedia.SearchAndResolve: build request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	start := time.Now()
	resp, err := c.http.Do(req)
	elapsed := time.Since(start)
	c.ins.requestLatency.WithLabelValues().Observe(elapsed.Seconds())

	if err != nil {
		c.ins.requests.WithLabelValues("failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionWikipediaSearchFailed,
			"wikipedia search transport error",
			logging.String("query", query),
			logging.Int64("elapsed_ms", elapsed.Milliseconds()),
			logging.Err(err),
		)
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		c.ins.requests.WithLabelValues("failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionWikipediaSearchFailed,
			"wikipedia search non-2xx",
			logging.String("query", query),
			logging.Int("status", resp.StatusCode),
			logging.String("body_preview", string(body)),
		)
		return nil, fmt.Errorf("wikipedia.SearchAndResolve: %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	var raw searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		c.ins.requests.WithLabelValues("failure").Inc()
		return nil, fmt.Errorf("wikipedia.SearchAndResolve: decode: %w", err)
	}
	if raw.Error != nil {
		c.ins.requests.WithLabelValues("failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionWikipediaSearchFailed,
			"wikipedia API error",
			logging.String("query", query),
			logging.String("api_code", raw.Error.Code),
			logging.String("api_info", raw.Error.Info),
		)
		return nil, fmt.Errorf("wikipedia.SearchAndResolve: API %s: %s", raw.Error.Code, raw.Error.Info)
	}

	// Sort by index — the pages map is randomized by Go, but each entry
	// carries the original search rank in `index` (1-indexed).
	hits := make([]Hit, 0, len(raw.Query.Pages))
	for _, p := range raw.Query.Pages {
		hits = append(hits, Hit{
			Title:       p.Title,
			WikidataQID: p.PageProps.WikibaseItem,
			PageID:      p.PageID,
			Index:       p.Index,
		})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Index < hits[j].Index })

	c.ins.requests.WithLabelValues("success").Inc()
	c.ins.emitEvent(ctx, logging.LevelDebug, vocabulary.ActionWikipediaSearch,
		"wikipedia search ok",
		logging.String("query", query),
		logging.Int64("elapsed_ms", elapsed.Milliseconds()),
		logging.Int("hits", len(hits)),
	)
	return hits, nil
}
