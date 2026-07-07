// Tests for ListFixtures + ListFixturesByIDs — httptest mock with
// realistic api-sports.io response shape.
package apifootball_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// mockFixturesServer — extends the basic mockAPIServer pattern from
// client_test.go with a /fixtures handler that returns a canned
// response and captures the query string for assertions.
type mockFixturesServer struct {
	srv                *httptest.Server
	receivedQuery      string
	fixturesResponse   any
	fixturesStatusCode int
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
		m.receivedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(m.fixturesStatusCode)
		if m.fixturesStatusCode == http.StatusOK {
			_ = json.NewEncoder(w).Encode(m.fixturesResponse)
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
			},
		},
		"errors": []any{},
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
	if f.Fixture.Status.Short != "1H" {
		t.Errorf("Status.Short = %q, want 1H", f.Fixture.Status.Short)
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
		"errors": []any{},
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
	m.fixturesResponse = map[string]any{"response": []any{}, "errors": []any{}}
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
	m.fixturesResponse = map[string]any{"response": []any{}, "errors": []any{}}
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

// TestListFixturesByIDs — dash-separated ids format.
func TestListFixturesByIDs(t *testing.T) {
	ctx := context.Background()
	m := newMockFixturesServer()
	m.fixturesResponse = canonicalFixturesResponse()
	defer m.Close()

	c := newClientForFixtures(t, ctx, m.URL())
	if _, err := c.ListFixturesByIDs(ctx, []int64{1_515_514, 1_515_515}); err != nil {
		t.Fatalf("ListFixturesByIDs: %v", err)
	}
	if !strings.Contains(m.receivedQuery, "ids=1515514-1515515") {
		t.Errorf("query %q missing ids=1515514-1515515", m.receivedQuery)
	}
}

// TestListFixturesByIDs_Empty — zero-id input returns nil, nil (no
// round-trip).
func TestListFixturesByIDs_Empty(t *testing.T) {
	ctx := context.Background()
	m := newMockFixturesServer()
	defer m.Close()
	c := newClientForFixtures(t, ctx, m.URL())
	got, err := c.ListFixturesByIDs(ctx, nil)
	if err != nil {
		t.Fatalf("ListFixturesByIDs(nil): %v", err)
	}
	if got != nil {
		t.Errorf("returned %v, want nil", got)
	}
}

// TestListFixturesByIDs_MaxCap — >20 IDs rejected client-side.
func TestListFixturesByIDs_MaxCap(t *testing.T) {
	ctx := context.Background()
	m := newMockFixturesServer()
	defer m.Close()
	c := newClientForFixtures(t, ctx, m.URL())
	ids := make([]int64, 21)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	if _, err := c.ListFixturesByIDs(ctx, ids); err == nil {
		t.Fatal("expected error for >20 ids, got nil")
	}
}
