// fixtures.go — the /fixtures endpoint. The IngestWorkflow calls
// ListFixtures for its configured rolling window; ActivePollWorkflow uses the
// batch-by-ID variant to refresh live match state at active cadence.
//
// Response types are DOMAIN-agnostic — they mirror what api-sports.io
// actually returns and get translated to internal/domain/fixture types
// at the activity layer (not here). Keeps this file swappable if we
// ever mock the endpoint or migrate off api-sports.io.
package apifootball

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// IDsBatchLimit is the api-sports.io `/fixtures?ids=` per-call cap
// (docs/api-football/fixtures-endpoint.md → query params table). Callers
// that need to fetch more than this should use ListFixturesByIDs, which
// chunks internally and fires per-chunk requests in parallel via
// goroutines — see the method comment for partial-failure semantics.
const IDsBatchLimit = 20

// FixtureFetchFailureKind separates malformed provider data from ordinary
// transport failure. Only contract failures are provider-integrity evidence.
type FixtureFetchFailureKind string

const (
	FixtureFetchFailureTransport FixtureFetchFailureKind = "transport"
	FixtureFetchFailureContract  FixtureFetchFailureKind = "contract"
)

// FixtureFetchFailure describes one failed by-ID request chunk without
// exposing raw provider bodies or unbounded error strings to workflows.
type FixtureFetchFailure struct {
	IDs            []int64
	Kind           FixtureFetchFailureKind
	ContractReason FixtureContractReason
}

// FixturesByIDsResult preserves successful fixtures and typed failed chunks.
// Partial and total provider failure both return this evidence to the caller.
type FixturesByIDsResult struct {
	Fixtures []APIFixture
	Failures []FixtureFetchFailure
}

// FailedIDs flattens the failed chunks in request order.
func (r FixturesByIDsResult) FailedIDs() []int64 {
	var ids []int64
	for _, failure := range r.Failures {
		ids = append(ids, failure.IDs...)
	}
	return ids
}

// APIFixture is one item in the /fixtures response array. Only the
// fields we consume are named; unknown fields get dropped by
// json.Decode (the SDK uses reflection + tags). If api-sports.io adds
// fields we need later, add them here — nothing else changes.
type APIFixture struct {
	Fixture APIFixtureFixture `json:"fixture"`
	League  APIFixtureLeague  `json:"league"`
	Teams   APIFixtureTeams   `json:"teams"`
	Goals   APIFixtureGoals   `json:"goals"`
	Score   APIFixtureScore   `json:"score"`
	Events  []APIFixtureEvent `json:"events,omitempty"` // present only on GetFixture, not List

	// eventsState is populated only while decoding the provider wire payload.
	// It lets the by-ID contract reject missing/null events while accepting an
	// explicit empty array; it is intentionally absent from downstream JSON.
	eventsState fixtureEventsState
}

// APIFixtureFixture is the "fixture" sub-object — identity + status +
// timing.
type APIFixtureFixture struct {
	ID        int64            `json:"id"`
	Referee   *string          `json:"referee"`
	Timezone  string           `json:"timezone"`
	Date      time.Time        `json:"date"` // kickoff — the API returns RFC3339
	Timestamp int64            `json:"timestamp"`
	Venue     APIFixtureVenue  `json:"venue"`
	Status    APIFixtureStatus `json:"status"`
}

type APIFixtureVenue struct {
	ID   *int   `json:"id"`
	Name string `json:"name"`
	City string `json:"city"`
}

// APIFixtureStatus mirrors the short + long status codes we care about.
// Short is the enum-like code (NS, 1H, HT, FT, PST, CANC, etc.);
// Elapsed is the current match minute (nullable pre-kickoff); Extra is
// stoppage-time minutes (nullable outside stoppage).
type APIFixtureStatus struct {
	Long    string        `json:"long"`
	Short   APIStatusCode `json:"short"` // typed enum, normalized at unmarshal
	Elapsed *int          `json:"elapsed"`
	Extra   *int          `json:"extra"`
}

type APIFixtureLeague struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Country string `json:"country"`
	Logo    string `json:"logo"`
	Flag    string `json:"flag"`
	Season  int    `json:"season"`
	Round   string `json:"round"`
}

type APIFixtureTeams struct {
	Home APIFixtureTeam `json:"home"`
	Away APIFixtureTeam `json:"away"`
}

type APIFixtureTeam struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Logo   string `json:"logo"`
	Winner *bool  `json:"winner"` // current leader; null pre-match or while tied
}

type APIFixtureGoals struct {
	Home *int `json:"home"`
	Away *int `json:"away"`
}

// APIFixtureScore includes multiple breakdowns (halftime / fulltime /
// extratime / penalty). Domain code reads Penalty; the other breakdowns remain
// available for transport completeness while aggregate goals drive match score.
type APIFixtureScore struct {
	Halftime  APIFixtureScoreLine `json:"halftime"`
	Fulltime  APIFixtureScoreLine `json:"fulltime"`
	Extratime APIFixtureScoreLine `json:"extratime"`
	Penalty   APIFixtureScoreLine `json:"penalty"`
}

type APIFixtureScoreLine struct {
	Home *int `json:"home"`
	Away *int `json:"away"`
}

// APIFixtureEvent — only populated when the fixture was fetched via
// GetFixture (not on the list-by-date path). Represented for
// completeness; the Monitor path uses a separate /fixtures/events
// endpoint that returns just the event list.
type APIFixtureEvent struct {
	Time     APIFixtureEventTime `json:"time"`
	Team     APIFixtureTeam      `json:"team"`
	Player   APIFixturePlayerRef `json:"player"`
	Assist   APIFixturePlayerRef `json:"assist"`
	Type     APIEventType        `json:"type"`   // typed enum, normalized at unmarshal
	Detail   APIEventDetail      `json:"detail"` // typed enum, normalized at unmarshal (incl. Substitution N → Substitution)
	Comments *string             `json:"comments"`
}

type APIFixtureEventTime struct {
	Elapsed int  `json:"elapsed"`
	Extra   *int `json:"extra"`
}

type APIFixturePlayerRef struct {
	ID   *int    `json:"id"`
	Name *string `json:"name"`
}

// FixtureListParams narrows the /fixtures query. All fields optional;
// the API interprets an empty request as "no filter" which is
// legitimate but rarely useful (returns everything). At least one of
// From+To, Date, Ids, or League should be set.
type FixtureListParams struct {
	// From + To bracket the kickoff window (inclusive). Both must be set
	// or neither — mixed produces a 400 from the API.
	From time.Time
	To   time.Time

	// Date narrows to a single YYYY-MM-DD (mutually exclusive with From/To
	// on the API side; we send whichever set of fields is non-zero).
	Date time.Time

	// League optionally scopes to one league ID. Combined with Season it
	// pins a specific season.
	League int
	Season int

	// Timezone forces the API to interpret Date / From / To in a specific
	// zone. Default (empty) = UTC.
	Timezone string
}

// ListFixtures calls GET /fixtures with the given filters. Returns the
// full response envelope — activity code translates each APIFixture
// into a domain fixture.Fixture.
//
// The bounded-cardinality endpoint label on metrics is "fixtures"
// regardless of query — we don't want a per-date label explosion.
func (c *Client) ListFixtures(ctx context.Context, params FixtureListParams) ([]APIFixture, error) {
	query, err := buildFixtureListQuery(params)
	if err != nil {
		return nil, err
	}
	var envelope fixtureEnvelope
	if err := c.getJSON(ctx, "fixtures", "/fixtures", query, &envelope); err != nil {
		return nil, err
	}
	fixtures, err := decodeFixtureEnvelope(envelope, nil, false)
	if err != nil {
		c.recordFixtureContractFailure(ctx, err)
		return nil, err
	}
	return fixtures, nil
}

// buildFixtureListQuery composes the URL query params from
// FixtureListParams. Enforces the mutually-exclusive Date vs
// From/To constraint at the client layer so callers get a clean Go
// error instead of a 400 from the API.
func buildFixtureListQuery(p FixtureListParams) (url.Values, error) {
	q := url.Values{}
	hasWindow := !p.From.IsZero() && !p.To.IsZero()
	hasDate := !p.Date.IsZero()
	if hasWindow && hasDate {
		return nil, fmt.Errorf("apifootball.ListFixtures: Date and From/To are mutually exclusive")
	}
	if !p.From.IsZero() != !p.To.IsZero() {
		return nil, fmt.Errorf("apifootball.ListFixtures: From and To must both be set or neither")
	}
	if hasWindow {
		q.Set("from", p.From.UTC().Format("2006-01-02"))
		q.Set("to", p.To.UTC().Format("2006-01-02"))
	}
	if hasDate {
		q.Set("date", p.Date.UTC().Format("2006-01-02"))
	}
	if p.League > 0 {
		q.Set("league", strconv.Itoa(p.League))
	}
	if p.Season > 0 {
		q.Set("season", strconv.Itoa(p.Season))
	}
	if p.Timezone != "" {
		q.Set("timezone", p.Timezone)
	}
	return q, nil
}

// ListFixturesByIDs fetches multiple fixtures via the `/fixtures?ids=`
// endpoint, splitting the input into IDsBatchLimit-sized chunks and
// firing per-chunk HTTP calls in PARALLEL via an errgroup. The vendor
// caps at 20 IDs per request (docs/api-football/fixtures-endpoint.md).
//
// PARTIAL-FAILURE SEMANTICS. When one chunk's HTTP call or fixture-contract
// validation fails while others succeed, the result retains the successful
// fixtures and one typed failure for the rejected chunk. Callers decide
// whether to retry result.FailedIDs(). err is set only on catastrophic
// failure: context cancellation or every chunk failing transport/validation.
// Even then, the result preserves the bounded failure evidence.
//
// Zero-length input short-circuits with no round trip.
func (c *Client) ListFixturesByIDs(ctx context.Context, ids []int64) (FixturesByIDsResult, error) {
	var result FixturesByIDsResult
	if len(ids) == 0 {
		return result, nil
	}

	chunks := chunkIDs(ids, IDsBatchLimit)
	chunkResults := make([][]APIFixture, len(chunks))
	failedChunks := make([]bool, len(chunks))
	chunkErrors := make([]error, len(chunks))

	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	for i, chunk := range chunks {
		i, chunk := i, chunk
		g.Go(func() error {
			got, err := c.fetchFixturesByIDsChunk(gctx, chunk)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				// Record + swallow — siblings continue. The caller
				// gets the partial + explicit failedIDs list.
				failedChunks[i] = true
				chunkErrors[i] = err
				return nil
			}
			chunkResults[i] = got
			return nil
		})
	}
	// g.Wait always returns nil here because all goroutines return nil
	// (errors get recorded into failedChunks instead of aborting the
	// group). Kept for API consistency.
	_ = g.Wait()

	// If ctx was cancelled underneath us, surface that as the error.
	// Chunks that hadn't started yet count as failed.
	if ctxErr := ctx.Err(); ctxErr != nil {
		result.Failures = []FixtureFetchFailure{fixtureFetchFailure(ids, ctxErr)}
		return result, ctxErr
	}

	// Flatten successes + collect failed IDs.
	successCount := 0
	for i, fixtures := range chunkResults {
		if failedChunks[i] {
			result.Failures = append(result.Failures,
				fixtureFetchFailure(chunks[i], chunkErrors[i]))
			continue
		}
		successCount++
		result.Fixtures = append(result.Fixtures, fixtures...)
	}

	// Catastrophic: every chunk failed. Return error so caller can
	// distinguish "API is down" from "some IDs didn't resolve."
	if successCount == 0 {
		var causes []error
		for _, chunkErr := range chunkErrors {
			if chunkErr != nil {
				causes = append(causes, chunkErr)
			}
		}
		return result, fmt.Errorf(
			"apifootball.ListFixturesByIDs: all %d chunks failed: %w", len(chunks), errors.Join(causes...))
	}

	return result, nil
}

func fixtureFetchFailure(ids []int64, err error) FixtureFetchFailure {
	failure := FixtureFetchFailure{
		IDs:  append([]int64(nil), ids...),
		Kind: FixtureFetchFailureTransport,
	}
	var contractErr *FixtureContractError
	if errors.As(err, &contractErr) {
		failure.Kind = FixtureFetchFailureContract
		failure.ContractReason = contractErr.Reason
	}
	return failure
}

// fetchFixturesByIDsChunk is one HTTP call to /fixtures?ids= for a
// single chunk that MUST already be ≤ IDsBatchLimit. Callers should
// use ListFixturesByIDs, which chunks + parallelizes.
func (c *Client) fetchFixturesByIDsChunk(ctx context.Context, ids []int64) ([]APIFixture, error) {
	// api-sports.io wants ids as "1234-5678-9012" (dash-separated).
	var b []byte
	for i, id := range ids {
		if i > 0 {
			b = append(b, '-')
		}
		b = strconv.AppendInt(b, id, 10)
	}
	q := url.Values{"ids": {string(b)}}

	var envelope fixtureEnvelope
	if err := c.getJSON(ctx, "fixtures", "/fixtures", q, &envelope); err != nil {
		return nil, err
	}
	fixtures, err := decodeFixtureEnvelope(envelope, ids, true)
	if err != nil {
		c.recordFixtureContractFailure(ctx, err)
		return nil, err
	}
	return fixtures, nil
}

// chunkIDs splits ids into batches of at most n. Preserves ordering;
// the last chunk may be shorter than n. Empty input returns nil.
func chunkIDs(ids []int64, n int) [][]int64 {
	if len(ids) == 0 || n <= 0 {
		return nil
	}
	if len(ids) <= n {
		return [][]int64{ids}
	}
	out := make([][]int64, 0, (len(ids)+n-1)/n)
	for i := 0; i < len(ids); i += n {
		end := i + n
		if end > len(ids) {
			end = len(ids)
		}
		out = append(out, ids[i:end])
	}
	return out
}
