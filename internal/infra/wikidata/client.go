// Package wikidata is the Wikidata HTTP adapter. Three endpoints:
//   - SPARQL Query Service (Query — the endpoint URL from config)
//   - MediaWiki wbsearchentities action (SearchEntities — fuzzy label search)
//   - Special:EntityData REST (GetEntity — full entity JSON)
//
// Public endpoints, no auth, but the User-Agent header must identify
// the caller per Wikidata's policy. Backs the alias-lookup pipeline.
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

// Client is the Wikidata HTTP wrapper.
type Client struct {
	http      *http.Client
	ins       *Instruments
	endpoint  string // SPARQL endpoint (query.wikidata.org/sparql in prod)
	wwwHost   string // MediaWiki API + Special:EntityData host (www.wikidata.org in prod)
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

// SearchHit is one candidate returned by wbsearchentities.
type SearchHit struct {
	ID          string // QID like "Q1130849"
	Label       string
	Description string // may be empty; used by the alias-lookup pipeline for disambiguation scoring
}

// SearchOpts tunes SearchEntities. Zero-value gives Language=en, Limit=10.
type SearchOpts struct {
	Language string // BCP 47 lang code; empty → "en"
	Limit    int    // 1..50; zero → 10
}

// LangText carries a text value tagged with its Wikidata language code.
// Returned by Entity.DemonymsP1549 / NativeNamesP1448.
type LangText struct {
	Lang string
	Text string
}

// Entity wraps a fetched Wikidata entity JSON response with typed
// accessor methods. The raw JSON is preserved for accessors that
// callers may add later without changing GetEntity's shape.
type Entity struct {
	QID string
	raw *entityJSON
}

// entityJSON mirrors just the parts of Special:EntityData/{QID}.json
// we care about — labels, aliases, and claim mainsnaks.
type entityJSON struct {
	Entities map[string]struct {
		Labels  map[string]langValue    `json:"labels"`
		Aliases map[string][]langValue  `json:"aliases"`
		Claims  map[string][]claimEntry `json:"claims"`
	} `json:"entities"`
}

type langValue struct {
	Language string `json:"language"`
	Value    string `json:"value"`
}

type claimEntry struct {
	Mainsnak struct {
		Datatype  string          `json:"datatype"`
		Datavalue json.RawMessage `json:"datavalue"`
	} `json:"mainsnak"`
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
	wwwHost := cfg.WWWHost
	if wwwHost == "" {
		wwwHost = "https://www.wikidata.org"
	}
	return &Client{
		http:      &http.Client{Timeout: cfg.Timeout},
		ins:       ins,
		endpoint:  strings.TrimRight(cfg.Endpoint, "/"),
		wwwHost:   strings.TrimRight(wwwHost, "/"),
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
	c.ins.requestLatency.WithLabelValues("sparql").Observe(elapsed.Seconds())

	if err != nil {
		c.ins.requests.WithLabelValues("sparql", "failure").Inc()
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
		c.ins.requests.WithLabelValues("sparql", "failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionWikidataQueryFailed,
			"wikidata non-2xx response",
			logging.Int("status", resp.StatusCode),
			logging.String("body_preview", string(body)),
		)
		return nil, fmt.Errorf("wikidata.Query: %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	var out SPARQLResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		c.ins.requests.WithLabelValues("sparql", "failure").Inc()
		return nil, fmt.Errorf("wikidata.Query: decode: %w", err)
	}
	c.ins.requests.WithLabelValues("sparql", "success").Inc()
	c.ins.emitEvent(ctx, logging.LevelDebug, vocabulary.ActionWikidataQuery,
		"wikidata query ok",
		logging.Int64("elapsed_ms", elapsed.Milliseconds()),
		logging.Int("bindings", len(out.Results.Bindings)),
	)
	return &out, nil
}

// searchResponse mirrors just the wbsearchentities fields we consume.
type searchResponse struct {
	Search []struct {
		ID          string `json:"id"`
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"search"`
}

// SearchEntities runs a wbsearchentities fuzzy search against the
// MediaWiki API. Returns up to opts.Limit candidate hits ranked by
// Wikidata's own relevance algorithm. Used by the alias-lookup
// pipeline as the first stage of name → QID resolution — description
// scoring on the returned hits does the disambiguation.
//
// Query variants can be composed by the caller (e.g., "Liverpool FC",
// "Liverpool football club"); each variant is one call to this method.
func (c *Client) SearchEntities(ctx context.Context, term string, opts SearchOpts) ([]SearchHit, error) {
	lang := opts.Language
	if lang == "" {
		lang = "en"
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	params := url.Values{
		"action":   {"wbsearchentities"},
		"search":   {term},
		"language": {lang},
		"format":   {"json"},
		"type":     {"item"},
		"limit":    {fmt.Sprintf("%d", limit)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.wwwHost+"/w/api.php?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("wikidata.SearchEntities: build request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	start := time.Now()
	resp, err := c.http.Do(req)
	elapsed := time.Since(start)
	c.ins.requestLatency.WithLabelValues("search").Observe(elapsed.Seconds())

	if err != nil {
		c.ins.requests.WithLabelValues("search", "failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionWikidataSearchFailed,
			"wikidata search transport error",
			logging.String("term", term),
			logging.Int64("elapsed_ms", elapsed.Milliseconds()),
			logging.Err(err),
		)
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		c.ins.requests.WithLabelValues("search", "failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionWikidataSearchFailed,
			"wikidata search non-2xx",
			logging.String("term", term),
			logging.Int("status", resp.StatusCode),
			logging.String("body_preview", string(body)),
		)
		return nil, fmt.Errorf("wikidata.SearchEntities: %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	var raw searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		c.ins.requests.WithLabelValues("search", "failure").Inc()
		return nil, fmt.Errorf("wikidata.SearchEntities: decode: %w", err)
	}
	hits := make([]SearchHit, 0, len(raw.Search))
	for _, r := range raw.Search {
		hits = append(hits, SearchHit{ID: r.ID, Label: r.Label, Description: r.Description})
	}
	c.ins.requests.WithLabelValues("search", "success").Inc()
	c.ins.emitEvent(ctx, logging.LevelDebug, vocabulary.ActionWikidataSearch,
		"wikidata search ok",
		logging.String("term", term),
		logging.Int64("elapsed_ms", elapsed.Milliseconds()),
		logging.Int("hits", len(hits)),
	)
	return hits, nil
}

// GetEntity fetches full entity JSON from Special:EntityData/{QID}.json.
// One HTTP call returns labels + aliases in every language + all
// claims. Caller navigates via the Entity accessor methods.
//
// Used by the alias-lookup pipeline for country-entity data (P1549
// demonyms + P1448 native names) and — reused by the selection phase
// (#134) — for the team entity's aliases + P1449 nicknames.
func (c *Client) GetEntity(ctx context.Context, qid string) (*Entity, error) {
	if qid == "" {
		return nil, fmt.Errorf("wikidata.GetEntity: qid is required")
	}
	url := c.wwwHost + "/wiki/Special:EntityData/" + qid + ".json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("wikidata.GetEntity: build request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	start := time.Now()
	resp, err := c.http.Do(req)
	elapsed := time.Since(start)
	c.ins.requestLatency.WithLabelValues("entity").Observe(elapsed.Seconds())

	if err != nil {
		c.ins.requests.WithLabelValues("entity", "failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionWikidataEntityFailed,
			"wikidata entity transport error",
			logging.String("qid", qid),
			logging.Int64("elapsed_ms", elapsed.Milliseconds()),
			logging.Err(err),
		)
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		c.ins.requests.WithLabelValues("entity", "failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionWikidataEntityFailed,
			"wikidata entity non-2xx",
			logging.String("qid", qid),
			logging.Int("status", resp.StatusCode),
			logging.String("body_preview", string(body)),
		)
		return nil, fmt.Errorf("wikidata.GetEntity: %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	var raw entityJSON
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		c.ins.requests.WithLabelValues("entity", "failure").Inc()
		return nil, fmt.Errorf("wikidata.GetEntity: decode: %w", err)
	}
	// Wikidata sometimes serves entity redirects; the top-level key may
	// differ from the requested QID. Prefer the requested QID; if the
	// server only returned the redirected one, fall back to the first
	// entry so callers see the data anyway.
	if _, ok := raw.Entities[qid]; !ok {
		if len(raw.Entities) == 0 {
			c.ins.requests.WithLabelValues("entity", "failure").Inc()
			return nil, fmt.Errorf("wikidata.GetEntity: entity JSON empty for %s", qid)
		}
	}
	c.ins.requests.WithLabelValues("entity", "success").Inc()
	c.ins.emitEvent(ctx, logging.LevelDebug, vocabulary.ActionWikidataEntityFetch,
		"wikidata entity ok",
		logging.String("qid", qid),
		logging.Int64("elapsed_ms", elapsed.Milliseconds()),
	)
	return &Entity{QID: qid, raw: &raw}, nil
}

// entityData returns the requested QID's data or the first available
// (Wikidata redirect fall-through).
func (e *Entity) entityData() (map[string][]langValue, map[string][]claimEntry, map[string]langValue) {
	if e == nil || e.raw == nil {
		return nil, nil, nil
	}
	if ed, ok := e.raw.Entities[e.QID]; ok {
		return ed.Aliases, ed.Claims, ed.Labels
	}
	// Fall-through — served entity was a redirect target.
	for _, ed := range e.raw.Entities {
		return ed.Aliases, ed.Claims, ed.Labels
	}
	return nil, nil, nil
}

// LabelEn returns the English label or "".
func (e *Entity) LabelEn() string {
	_, _, labels := e.entityData()
	if lv, ok := labels["en"]; ok {
		return lv.Value
	}
	return ""
}

// LabelsByLang returns the label per language as a map[lang]value.
// Copies out into a fresh map so callers can't mutate the entity.
func (e *Entity) LabelsByLang() map[string]string {
	_, _, labels := e.entityData()
	if labels == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(labels))
	for lang, lv := range labels {
		out[lang] = lv.Value
	}
	return out
}

// AliasesByLang returns the alias list per language. Each language's
// value list is copied to prevent callers from mutating internal state.
func (e *Entity) AliasesByLang() map[string][]string {
	aliases, _, _ := e.entityData()
	if aliases == nil {
		return map[string][]string{}
	}
	out := make(map[string][]string, len(aliases))
	for lang, list := range aliases {
		vals := make([]string, 0, len(list))
		for _, lv := range list {
			vals = append(vals, lv.Value)
		}
		out[lang] = vals
	}
	return out
}

// NicknamesP1449 returns all P1449 nickname property values. Language-
// agnostic because P1449 values are monolingualtext and the pipeline
// treats every nickname as a candidate regardless of language.
func (e *Entity) NicknamesP1449() []string {
	return e.monolingualTextsFromClaim("P1449", "")
}

// DemonymsP1549 returns all P1549 demonym values with their language
// codes. Used by the alias-lookup pipeline's per-country variations
// derivation (English demonyms only for nationals; broader for club
// disambiguation description scoring).
func (e *Entity) DemonymsP1549() []LangText {
	return e.langTextsFromClaim("P1549")
}

// NativeNamesP1448 returns all P1448 official-name values with lang
// codes. Used by the country-variations derivation alongside P1549.
func (e *Entity) NativeNamesP1448() []LangText {
	return e.langTextsFromClaim("P1448")
}

// FirstClaimQID returns the QID value of the first claim for a
// property, or "" if absent. Used for P17 (country), P115 (venue),
// P159 (HQ location).
func (e *Entity) FirstClaimQID(property string) string {
	_, claims, _ := e.entityData()
	entries := claims[property]
	for _, c := range entries {
		if c.Mainsnak.Datatype != "wikibase-item" {
			continue
		}
		var v struct {
			Value struct {
				ID string `json:"id"`
			} `json:"value"`
		}
		if err := json.Unmarshal(c.Mainsnak.Datavalue, &v); err != nil {
			continue
		}
		if v.Value.ID != "" {
			return v.Value.ID
		}
	}
	return ""
}

// monolingualTextsFromClaim extracts .text from all monolingualtext
// claim mainsnaks for a property. If langFilter is non-empty, only
// entries with matching .language are returned; empty → all languages.
func (e *Entity) monolingualTextsFromClaim(property, langFilter string) []string {
	_, claims, _ := e.entityData()
	entries := claims[property]
	out := make([]string, 0, len(entries))
	for _, c := range entries {
		if c.Mainsnak.Datatype != "monolingualtext" {
			continue
		}
		var v struct {
			Value struct {
				Text     string `json:"text"`
				Language string `json:"language"`
			} `json:"value"`
		}
		if err := json.Unmarshal(c.Mainsnak.Datavalue, &v); err != nil {
			continue
		}
		if langFilter != "" && v.Value.Language != langFilter {
			continue
		}
		if v.Value.Text != "" {
			out = append(out, v.Value.Text)
		}
	}
	return out
}

// langTextsFromClaim returns full (lang, text) tuples for a
// monolingualtext-typed property. Callers filter by language.
func (e *Entity) langTextsFromClaim(property string) []LangText {
	_, claims, _ := e.entityData()
	entries := claims[property]
	out := make([]LangText, 0, len(entries))
	for _, c := range entries {
		if c.Mainsnak.Datatype != "monolingualtext" {
			continue
		}
		var v struct {
			Value struct {
				Text     string `json:"text"`
				Language string `json:"language"`
			} `json:"value"`
		}
		if err := json.Unmarshal(c.Mainsnak.Datavalue, &v); err != nil {
			continue
		}
		if v.Value.Text != "" {
			out = append(out, LangText{Lang: v.Value.Language, Text: v.Value.Text})
		}
	}
	return out
}
