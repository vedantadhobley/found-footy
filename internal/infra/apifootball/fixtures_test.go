// Tests for ListFixtures + ListFixturesByIDs — httptest mock with
// realistic api-sports.io response shape.
package apifootball_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// mockFixturesServer — extends the basic mockAPIServer pattern from
// client_test.go with a /fixtures handler that returns a canned
// response and captures the query string for assertions. Concurrent-safe:
// parallel chunking in ListFixturesByIDs fires goroutines that all hit
// /fixtures, so state is guarded by mu.
type mockFixturesServer struct {
	srv                *httptest.Server
	mu                 sync.Mutex
	receivedQuery      string   // last query (kept for back-compat with existing tests)
	receivedQueries    []string // ALL queries in call order — used by parallel-chunk tests
	fixturesResponse   any
	fixturesResponder  func(*http.Request) any
	fixturesStatusCode int
	// perQueryStatusCode — optional per-query override. If a request's
	// ids= param string matches a key in this map, respond with that
	// status code instead of fixturesStatusCode. Used to simulate
	// partial failure (chunk X fails while chunk Y succeeds).
	perQueryStatusCode map[string]int
}

func newMockFixturesServer() *mockFixturesServer {
	m := &mockFixturesServer{
		fixturesStatusCode: http.StatusOK,
	}
	mux := http.NewServeMux()
	// /status is required for NewClient's probe.
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"response": map[string]any{
				"account":      map[string]any{"firstname": "test", "email": "test@x.com"},
				"subscription": map[string]any{"plan": "Free", "end": "2027-01-01", "active": true},
				"requests":     map[string]any{"current": 1, "limit_day": 100},
			},
			"errors": []any{},
		})
	})
	mux.HandleFunc("/fixtures", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.RawQuery
		m.mu.Lock()
		m.receivedQuery = q
		m.receivedQueries = append(m.receivedQueries, q)
		status := m.fixturesStatusCode
		if override, ok := m.perQueryStatusCode[r.URL.Query().Get("ids")]; ok {
			status = override
		}
		resp := m.fixturesResponse
		responder := m.fixturesResponder
		m.mu.Unlock()
		if responder != nil {
			resp = responder(r)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusOK {
			_ = json.NewEncoder(w).Encode(resp)
		} else {
			_, _ = w.Write([]byte(`{"error":"simulated"}`))
		}
	})
	m.srv = httptest.NewServer(mux)
	return m
}

func (m *mockFixturesServer) URL() string { return m.srv.URL }
func (m *mockFixturesServer) Close()      { m.srv.Close() }

func newClientForFixtures(t *testing.T, ctx context.Context, url string) *apifootball.Client {
	t.Helper()
	fx := newTestFixture()
	c, err := apifootball.NewClient(ctx, config.APIFootballConfig{
		BaseURL: url,
		APIKey:  "test-key",
		Timeout: 5 * time.Second,
	}, fx.ins)
	if err != nil {
		t.Fatalf("apifootball.NewClient: %v", err)
	}
	return c
}

// realish canned response — one Premier League fixture with the shape
// api-sports.io actually returns. Enough fields to exercise the
// unmarshaling paths (nullable elapsed/extra, home/away goals,
// score sub-object).
func canonicalFixturesResponse() map[string]any {
	elapsed := 45
	extra := 2
	homeGoals := 1
	awayGoals := 0
	return map[string]any{
		"response": []map[string]any{
			{
				"fixture": map[string]any{
					"id":        1_515_514,
					"referee":   "Michael Oliver, England",
					"timezone":  "UTC",
					"date":      "2026-07-08T15:00:00+00:00",
					"timestamp": 1_783_884_400,
					"venue": map[string]any{
						"id":   550,
						"name": "Anfield",
						"city": "Liverpool",
					},
					"status": map[string]any{
						"long":    "First Half",
						"short":   "1H",
						"elapsed": elapsed,
						"extra":   extra,
					},
				},
				"league": map[string]any{
					"id":      39,
					"name":    "Premier League",
					"country": "England",
					"logo":    "https://.../logo.png",
					"flag":    "https://.../flag.png",
					"season":  2026,
					"round":   "Regular Season - 5",
				},
				"teams": map[string]any{
					"home": map[string]any{"id": 40, "name": "Liverpool", "logo": "..."},
					"away": map[string]any{"id": 42, "name": "Arsenal", "logo": "..."},
				},
				"goals": map[string]any{
					"home": homeGoals,
					"away": awayGoals,
				},
				"score": map[string]any{
					"halftime":  map[string]any{"home": nil, "away": nil},
					"fulltime":  map[string]any{"home": nil, "away": nil},
					"extratime": map[string]any{"home": nil, "away": nil},
					"penalty":   map[string]any{"home": nil, "away": nil},
				},
				"events": []any{},
			},
		},
		"errors":  []any{},
		"results": 1,
		"paging":  map[string]any{"current": 1, "total": 1},
	}
}

func fixturesResponseForRequest(r *http.Request) any {
	parts := strings.Split(r.URL.Query().Get("ids"), "-")
	ids := make([]int64, 0, len(parts))
	for _, part := range parts {
		var id int64
		if _, err := fmt.Sscan(part, &id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return fixturesResponseForIDs(ids...)
}

func fixturesResponseForIDs(ids ...int64) map[string]any {
	fixtures := make([]any, 0, len(ids))
	for _, id := range ids {
		fixture := canonicalFixturesResponse()["response"].([]map[string]any)[0]
		fixtureObject := fixture["fixture"].(map[string]any)
		fixtureObject["id"] = id
		fixtures = append(fixtures, fixture)
	}
	return map[string]any{
		"response": fixtures,
		"errors":   []any{},
		"results":  len(fixtures),
		"paging":   map[string]any{"current": 1, "total": 1},
	}
}

func requireFixtureContractReason(t *testing.T, err error, want apifootball.FixtureContractReason) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected fixture contract error %q, got nil", want)
	}
	if !errors.Is(err, apifootball.ErrInvalidFixtureContract) {
		t.Fatalf("error %v does not wrap ErrInvalidFixtureContract", err)
	}
	var contractErr *apifootball.FixtureContractError
	if !errors.As(err, &contractErr) {
		t.Fatalf("error %v has no FixtureContractError", err)
	}
	if contractErr.Reason != want {
		t.Fatalf("reason = %q, want %q (error: %v)", contractErr.Reason, want, err)
	}
}

// TestListFixtures_ParsesCanonicalResponse — the load-bearing case.
func TestListFixtures_ParsesCanonicalResponse(t *testing.T) {
	ctx := context.Background()
	m := newMockFixturesServer()
	m.fixturesResponse = canonicalFixturesResponse()
	defer m.Close()

	c := newClientForFixtures(t, ctx, m.URL())
	got, err := c.ListFixtures(ctx, apifootball.FixtureListParams{
		From: time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ListFixtures: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("returned %d fixtures, want 1", len(got))
	}
	f := got[0]
	if f.Fixture.ID != 1_515_514 {
		t.Errorf("Fixture.ID = %d, want 1_515_514", f.Fixture.ID)
	}
	// Vendor JSON sends "1H" (uppercase); UnmarshalJSON normalizes to
	// canonical lowercase "1h" per the 2026-07-09 lowercase policy.
	if f.Fixture.Status.Short != apifootball.StatusFirstHalf {
		t.Errorf("Status.Short = %q, want %q", f.Fixture.Status.Short, apifootball.StatusFirstHalf)
	}
	if f.Fixture.Status.Elapsed == nil || *f.Fixture.Status.Elapsed != 45 {
		t.Errorf("Status.Elapsed = %v, want 45", f.Fixture.Status.Elapsed)
	}
	if f.League.ID != 39 || f.League.Season != 2026 {
		t.Errorf("League = %+v", f.League)
	}
	if f.Teams.Home.ID != 40 || f.Teams.Home.Name != "Liverpool" {
		t.Errorf("Home = %+v", f.Teams.Home)
	}
	if f.Goals.Home == nil || *f.Goals.Home != 1 {
		t.Errorf("Goals.Home = %v, want 1", f.Goals.Home)
	}
	// Kickoff parses as UTC.
	wantKickoff := time.Date(2026, 7, 8, 15, 0, 0, 0, time.UTC)
	if !f.Fixture.Date.Equal(wantKickoff) {
		t.Errorf("Kickoff = %v, want %v", f.Fixture.Date, wantKickoff)
	}
}

// TestListFixtures_HandlesNullableFields — pre-kickoff state where
// elapsed/extra/goals/scores are all null.
func TestListFixtures_HandlesNullableFields(t *testing.T) {
	ctx := context.Background()
	m := newMockFixturesServer()
	m.fixturesResponse = map[string]any{
		"response": []map[string]any{
			{
				"fixture": map[string]any{
					"id":        1_515_515,
					"referee":   nil,
					"timezone":  "UTC",
					"date":      "2026-07-08T15:00:00+00:00",
					"timestamp": 1_783_884_400,
					"venue":     map[string]any{"id": nil, "name": "Anfield", "city": "Liverpool"},
					"status":    map[string]any{"long": "Not Started", "short": "NS", "elapsed": nil, "extra": nil},
				},
				"league": map[string]any{"id": 39, "name": "PL", "country": "England", "season": 2026},
				"teams": map[string]any{
					"home": map[string]any{"id": 40, "name": "Liverpool", "winner": nil},
					"away": map[string]any{"id": 42, "name": "Arsenal", "winner": nil},
				},
				"goals": map[string]any{"home": nil, "away": nil},
				"score": map[string]any{
					"halftime":  map[string]any{"home": nil, "away": nil},
					"fulltime":  map[string]any{"home": nil, "away": nil},
					"extratime": map[string]any{"home": nil, "away": nil},
					"penalty":   map[string]any{"home": nil, "away": nil},
				},
			},
		},
		"errors":  []any{},
		"results": 1,
		"paging":  map[string]any{"current": 1, "total": 1},
	}
	defer m.Close()

	c := newClientForFixtures(t, ctx, m.URL())
	got, err := c.ListFixtures(ctx, apifootball.FixtureListParams{
		Date: time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ListFixtures: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("returned %d, want 1", len(got))
	}
	f := got[0]
	if f.Fixture.Status.Elapsed != nil {
		t.Errorf("Elapsed = %v, want nil pre-kickoff", f.Fixture.Status.Elapsed)
	}
	if f.Goals.Home != nil || f.Goals.Away != nil {
		t.Errorf("Goals should be nil pre-kickoff: %+v", f.Goals)
	}
	if f.Fixture.Referee != nil {
		t.Errorf("Referee = %v, want nil", f.Fixture.Referee)
	}
}

// TestListFixtures_QueryParamsFromWindow — verify the API sees
// from=YYYY-MM-DD&to=YYYY-MM-DD.
func TestListFixtures_QueryParamsFromWindow(t *testing.T) {
	ctx := context.Background()
	m := newMockFixturesServer()
	m.fixturesResponse = map[string]any{
		"response": []any{}, "errors": []any{}, "results": 0,
		"paging": map[string]any{"current": 1, "total": 1},
	}
	defer m.Close()

	c := newClientForFixtures(t, ctx, m.URL())
	if _, err := c.ListFixtures(ctx, apifootball.FixtureListParams{
		From:   time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC),
		To:     time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC),
		League: 39,
		Season: 2026,
	}); err != nil {
		t.Fatalf("ListFixtures: %v", err)
	}
	for _, want := range []string{"from=2026-07-08", "to=2026-07-10", "league=39", "season=2026"} {
		if !strings.Contains(m.receivedQuery, want) {
			t.Errorf("received query %q missing %q", m.receivedQuery, want)
		}
	}
}

// TestListFixtures_QueryParamsFromDate — Date populates date=.
func TestListFixtures_QueryParamsFromDate(t *testing.T) {
	ctx := context.Background()
	m := newMockFixturesServer()
	m.fixturesResponse = map[string]any{
		"response": []any{}, "errors": []any{}, "results": 0,
		"paging": map[string]any{"current": 1, "total": 1},
	}
	defer m.Close()

	c := newClientForFixtures(t, ctx, m.URL())
	if _, err := c.ListFixtures(ctx, apifootball.FixtureListParams{
		Date: time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("ListFixtures: %v", err)
	}
	if !strings.Contains(m.receivedQuery, "date=2026-07-08") {
		t.Errorf("query %q missing date=2026-07-08", m.receivedQuery)
	}
}

// TestListFixtures_MutuallyExclusiveWindowAndDate — client-side reject.
func TestListFixtures_MutuallyExclusiveWindowAndDate(t *testing.T) {
	ctx := context.Background()
	m := newMockFixturesServer()
	defer m.Close()

	c := newClientForFixtures(t, ctx, m.URL())
	_, err := c.ListFixtures(ctx, apifootball.FixtureListParams{
		From: time.Now(),
		To:   time.Now().Add(24 * time.Hour),
		Date: time.Now(),
	})
	if err == nil {
		t.Fatal("expected client-side error for both Window and Date, got nil")
	}
}

// TestListFixtures_HalfWindow_Errors — From without To (or vice versa)
// is bug-shaped input; catch client-side.
func TestListFixtures_HalfWindow_Errors(t *testing.T) {
	ctx := context.Background()
	m := newMockFixturesServer()
	defer m.Close()

	c := newClientForFixtures(t, ctx, m.URL())
	if _, err := c.ListFixtures(ctx, apifootball.FixtureListParams{
		From: time.Now(),
	}); err == nil {
		t.Fatal("expected error for From without To")
	}
	if _, err := c.ListFixtures(ctx, apifootball.FixtureListParams{
		To: time.Now(),
	}); err == nil {
		t.Fatal("expected error for To without From")
	}
}

// TestListFixturesByIDs — dash-separated ids format, single chunk.
// 2 IDs fits in one chunk (≤ IDsBatchLimit), so exactly one /fixtures
// call. failedIDs is empty on success.
func TestListFixturesByIDs(t *testing.T) {
	ctx := context.Background()
	m := newMockFixturesServer()
	m.fixturesResponder = fixturesResponseForRequest
	defer m.Close()

	c := newClientForFixtures(t, ctx, m.URL())
	_, failedIDs, err := c.ListFixturesByIDs(ctx, []int64{1_515_514, 1_515_515})
	if err != nil {
		t.Fatalf("ListFixturesByIDs: %v", err)
	}
	if len(failedIDs) != 0 {
		t.Errorf("failedIDs = %v; want empty", failedIDs)
	}
	if !strings.Contains(m.receivedQuery, "ids=1515514-1515515") {
		t.Errorf("query %q missing ids=1515514-1515515", m.receivedQuery)
	}
	if got := len(m.receivedQueries); got != 1 {
		t.Errorf("call count %d; want 1", got)
	}
}

// TestListFixturesByIDs_Empty — zero-id input returns (nil, nil, nil)
// with no round-trip.
func TestListFixturesByIDs_Empty(t *testing.T) {
	ctx := context.Background()
	m := newMockFixturesServer()
	defer m.Close()
	c := newClientForFixtures(t, ctx, m.URL())
	got, failedIDs, err := c.ListFixturesByIDs(ctx, nil)
	if err != nil {
		t.Fatalf("ListFixturesByIDs(nil): %v", err)
	}
	if got != nil {
		t.Errorf("returned fixtures %v, want nil", got)
	}
	if failedIDs != nil {
		t.Errorf("returned failedIDs %v, want nil", failedIDs)
	}
	if len(m.receivedQueries) != 0 {
		t.Errorf("expected zero round-trips, got %d", len(m.receivedQueries))
	}
}

// TestListFixturesByIDs_ChunksAndParallels — 25 IDs > IDsBatchLimit (20),
// so the client should split into 2 chunks (20 + 5) and fire them in
// parallel. Verify exactly 2 HTTP calls hit /fixtures.
func TestListFixturesByIDs_ChunksAndParallels(t *testing.T) {
	ctx := context.Background()
	m := newMockFixturesServer()
	m.fixturesResponder = fixturesResponseForRequest
	defer m.Close()

	ids := make([]int64, 25)
	for i := range ids {
		ids[i] = int64(i + 1)
	}

	c := newClientForFixtures(t, ctx, m.URL())
	_, failedIDs, err := c.ListFixturesByIDs(ctx, ids)
	if err != nil {
		t.Fatalf("ListFixturesByIDs(25): %v", err)
	}
	if len(failedIDs) != 0 {
		t.Errorf("failedIDs = %v; want empty", failedIDs)
	}
	if got := len(m.receivedQueries); got != 2 {
		t.Errorf("call count %d; want 2 (20-chunk + 5-chunk)", got)
	}
}

// TestListFixturesByIDs_PartialFailure — 25 IDs → 2 chunks; the
// second chunk (the 5-ID one, dash-joined "21-22-23-24-25") gets a
// 500. Expect fixtures back from chunk 1, failedIDs = chunk 2's IDs,
// err = nil (partial failure is expressed via failedIDs, not err).
func TestListFixturesByIDs_PartialFailure(t *testing.T) {
	ctx := context.Background()
	m := newMockFixturesServer()
	m.fixturesResponder = fixturesResponseForRequest
	m.perQueryStatusCode = map[string]int{
		"21-22-23-24-25": http.StatusInternalServerError,
	}
	defer m.Close()

	ids := make([]int64, 25)
	for i := range ids {
		ids[i] = int64(i + 1)
	}

	c := newClientForFixtures(t, ctx, m.URL())
	_, failedIDs, err := c.ListFixturesByIDs(ctx, ids)
	if err != nil {
		t.Fatalf("expected partial failure to be expressed via failedIDs, not err; got err=%v", err)
	}
	if len(failedIDs) != 5 {
		t.Errorf("failedIDs len = %d; want 5", len(failedIDs))
	}
	for i, want := range []int64{21, 22, 23, 24, 25} {
		if i >= len(failedIDs) || failedIDs[i] != want {
			t.Errorf("failedIDs[%d] = %d; want %d", i, failedIDs[i], want)
		}
	}
}

// TestListFixturesByIDs_AllChunksFail — every chunk returns 500. The
// client should surface this as a real error since no forward progress
// is possible (distinguish from partial: nothing came back at all).
func TestListFixturesByIDs_AllChunksFail(t *testing.T) {
	ctx := context.Background()
	m := newMockFixturesServer()
	m.fixturesStatusCode = http.StatusInternalServerError
	defer m.Close()

	ids := make([]int64, 25)
	for i := range ids {
		ids[i] = int64(i + 1)
	}

	c := newClientForFixtures(t, ctx, m.URL())
	fixtures, failedIDs, err := c.ListFixturesByIDs(ctx, ids)
	if err == nil {
		t.Fatal("expected err on total failure, got nil")
	}
	if len(fixtures) != 0 {
		t.Errorf("fixtures len = %d; want 0", len(fixtures))
	}
	if len(failedIDs) != 25 {
		t.Errorf("failedIDs len = %d; want 25 (all input)", len(failedIDs))
	}
}

func TestListFixtures_RejectsInvalidEnvelope(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   apifootball.FixtureContractReason
	}{
		{
			name: "errors missing",
			mutate: func(envelope map[string]any) {
				delete(envelope, "errors")
			},
			want: apifootball.FixtureContractErrorsMissing,
		},
		{
			name: "errors nonempty",
			mutate: func(envelope map[string]any) {
				envelope["errors"] = map[string]any{"api": "provider failure"}
			},
			want: apifootball.FixtureContractErrorsNonEmpty,
		},
		{
			name: "results missing",
			mutate: func(envelope map[string]any) {
				delete(envelope, "results")
			},
			want: apifootball.FixtureContractResultsMissing,
		},
		{
			name: "results mismatch",
			mutate: func(envelope map[string]any) {
				envelope["results"] = 2
			},
			want: apifootball.FixtureContractResultsMismatch,
		},
		{
			name: "paging missing",
			mutate: func(envelope map[string]any) {
				delete(envelope, "paging")
			},
			want: apifootball.FixtureContractPagingMissing,
		},
		{
			name: "paging incomplete",
			mutate: func(envelope map[string]any) {
				envelope["paging"] = map[string]any{"current": 1, "total": 2}
			},
			want: apifootball.FixtureContractPagingIncomplete,
		},
		{
			name: "response missing",
			mutate: func(envelope map[string]any) {
				delete(envelope, "response")
			},
			want: apifootball.FixtureContractResponseMissing,
		},
		{
			name: "response null",
			mutate: func(envelope map[string]any) {
				envelope["response"] = nil
			},
			want: apifootball.FixtureContractResponseInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			m := newMockFixturesServer()
			envelope := canonicalFixturesResponse()
			tt.mutate(envelope)
			m.fixturesResponse = envelope
			defer m.Close()

			c := newClientForFixtures(t, ctx, m.URL())
			_, err := c.ListFixtures(ctx, apifootball.FixtureListParams{
				Date: time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC),
			})
			requireFixtureContractReason(t, err, tt.want)
		})
	}
}

func TestListFixturesByIDs_RejectsIncompleteOrInvalidFixturePayload(t *testing.T) {
	tests := []struct {
		name   string
		ids    []int64
		mutate func(map[string]any)
		want   apifootball.FixtureContractReason
	}{
		{
			name: "events missing",
			ids:  []int64{101},
			mutate: func(envelope map[string]any) {
				delete(envelope["response"].([]any)[0].(map[string]any), "events")
			},
			want: apifootball.FixtureContractEventsMissing,
		},
		{
			name: "events null",
			ids:  []int64{101},
			mutate: func(envelope map[string]any) {
				envelope["response"].([]any)[0].(map[string]any)["events"] = nil
			},
			want: apifootball.FixtureContractEventsNull,
		},
		{
			name: "requested id missing",
			ids:  []int64{101, 102},
			mutate: func(envelope map[string]any) {
				envelope["response"] = envelope["response"].([]any)[:1]
				envelope["results"] = 1
			},
			want: apifootball.FixtureContractRequestedMissing,
		},
		{
			name: "returned id duplicate",
			ids:  []int64{101, 102},
			mutate: func(envelope map[string]any) {
				items := envelope["response"].([]any)
				items[1].(map[string]any)["fixture"].(map[string]any)["id"] = int64(101)
			},
			want: apifootball.FixtureContractReturnedDuplicate,
		},
		{
			name: "returned id unrequested",
			ids:  []int64{101},
			mutate: func(envelope map[string]any) {
				envelope["response"].([]any)[0].(map[string]any)["fixture"].(map[string]any)["id"] = int64(999)
			},
			want: apifootball.FixtureContractReturnedUnrequested,
		},
		{
			name: "fixture id zero",
			ids:  []int64{101},
			mutate: func(envelope map[string]any) {
				envelope["response"].([]any)[0].(map[string]any)["fixture"].(map[string]any)["id"] = int64(0)
			},
			want: apifootball.FixtureContractFixtureIdentity,
		},
		{
			name: "same team identity",
			ids:  []int64{101},
			mutate: func(envelope map[string]any) {
				teams := envelope["response"].([]any)[0].(map[string]any)["teams"].(map[string]any)
				teams["away"].(map[string]any)["id"] = 40
			},
			want: apifootball.FixtureContractTeamIdentity,
		},
		{
			name: "negative score",
			ids:  []int64{101},
			mutate: func(envelope map[string]any) {
				envelope["response"].([]any)[0].(map[string]any)["goals"].(map[string]any)["home"] = -1
			},
			want: apifootball.FixtureContractNegativeScore,
		},
		{
			name: "event team outside fixture",
			ids:  []int64{101},
			mutate: func(envelope map[string]any) {
				fixture := envelope["response"].([]any)[0].(map[string]any)
				fixture["events"] = []any{map[string]any{
					"time":   map[string]any{"elapsed": 1, "extra": nil},
					"team":   map[string]any{"id": 999, "name": "Other"},
					"player": map[string]any{"id": 1, "name": "Player"},
					"assist": map[string]any{"id": nil, "name": nil},
					"type":   "Goal", "detail": "Normal Goal", "comments": nil,
				}}
			},
			want: apifootball.FixtureContractEventTeamInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			m := newMockFixturesServer()
			envelope := fixturesResponseForIDs(tt.ids...)
			tt.mutate(envelope)
			m.fixturesResponse = envelope
			defer m.Close()

			c := newClientForFixtures(t, ctx, m.URL())
			_, failedIDs, err := c.ListFixturesByIDs(ctx, tt.ids)
			if len(failedIDs) != len(tt.ids) {
				t.Fatalf("failedIDs = %v, want all requested IDs %v", failedIDs, tt.ids)
			}
			requireFixtureContractReason(t, err, tt.want)
		})
	}
}
