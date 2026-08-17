// mock_api.go — httptest-backed mock for api-sports.io. Serves the
// endpoints our production code hits: /status (probe) + /fixtures
// (with `?from=&to=`, `?ids=`, or `?live=all` variants).
//
// The mock is stateless — it holds the scenario's APIResponses in a
// struct field and looks up which response to return by inspecting
// the query string. No dynamic state; scenarios that need
// per-cycle-varying responses will get a richer version.
package harness

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// MockAPI is an in-process HTTP server that mimics api-sports.io for
// scenario runs. Point apifootball.Client's BaseURL at MockAPI.URL()
// and it doesn't know it's mocked.
type MockAPI struct {
	srv       *httptest.Server
	responses APIResponses
	// fault primed by SetFault; applyFault consumes remaining count.
	fault *APIFault
}

// APIFault is a fault to inject on the next requests. If Remaining
// decrements to zero AND the fault is not permanent (Remaining
// started >0), the fault clears itself after that request. If
// Remaining is negative, the fault persists for the entire scenario
// segment (until SetFault(nil) or SetResponses clears it).
type APIFault struct {
	StatusCode int
	Body       string
	Remaining  int // >0: applies next N requests then clears; -1: persistent
}

// SetFault primes the mock to fail the next request(s). Pass nil to
// clear any pending fault.
func (m *MockAPI) SetFault(f *APIFault) { m.fault = f }

// NewMockAPI starts an httptest.Server. Cleanup registered via
// t.Cleanup. Call SetResponses / SetFault to configure per-scenario.
func NewMockAPI(t *testing.T) *MockAPI {
	t.Helper()
	m := &MockAPI{}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", m.handleStatus)
	mux.HandleFunc("/fixtures", m.handleFixtures)
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

// URL returns the mock's base URL. Feed this to
// apifootball.NewClient via BaseURL.
func (m *MockAPI) URL() string { return m.srv.URL }

// SetResponses installs the scenario's APIResponses.
func (m *MockAPI) SetResponses(r APIResponses) { m.responses = r }

// handleStatus mimics api-sports.io's /status endpoint — probed by
// apifootball.NewClient on startup. Always returns a "healthy Pro
// plan, well under limit" response so the probe succeeds unless a
// fault is injected.
func (m *MockAPI) handleStatus(w http.ResponseWriter, r *http.Request) {
	if m.applyFault(w) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"response": map[string]any{
			"account":      map[string]any{"firstname": "test", "lastname": "harness", "email": "harness@ff.local"},
			"subscription": map[string]any{"plan": "Pro", "end": "2099-12-31", "active": true},
			"requests":     map[string]any{"current": 1, "limit_day": 7500},
		},
		"errors":  []any{},
		"results": 1,
	})
}

// handleFixtures dispatches on query string to the right APIResponses
// field. Recognizes:
//   ?ids=1-2-3 → FixturesByIDs
//   ?from=&to= → FixturesWindow
//   ?live=all  → FixturesWindow (same data, different endpoint call
//                for Monitor's live-batch path)
func (m *MockAPI) handleFixtures(w http.ResponseWriter, r *http.Request) {
	if m.applyFault(w) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()

	var src *FixturesResponse
	switch {
	case q.Get("ids") != "":
		src = m.responses.FixturesByIDs
	case q.Get("from") != "" || q.Get("to") != "" || q.Get("live") != "":
		src = m.responses.FixturesWindow
	default:
		src = m.responses.FixturesWindow
	}

	if src == nil {
		// No scenario config for this endpoint variant — return empty.
		src = &FixturesResponse{}
	}
	body := map[string]any{
		"response": scenarioFixturesToAPI(src.Fixtures),
		"errors":   []any{},
		"results":  len(src.Fixtures),
	}
	_ = json.NewEncoder(w).Encode(body)
}

// applyFault returns true if a fault was applied (caller returns
// without writing the normal body). Decrements Remaining; clears the
// fault when it hits zero (unless Remaining started negative =
// persistent).
func (m *MockAPI) applyFault(w http.ResponseWriter) bool {
	if m.fault == nil {
		return false
	}
	w.WriteHeader(m.fault.StatusCode)
	body := m.fault.Body
	if body == "" {
		// Realistic api-sports.io error envelope by default. Fits
		// what production code has seen in the wild.
		body = `{"response":[],"errors":{"api":"simulated harness fault"},"results":0}`
	}
	_, _ = w.Write([]byte(body))
	if m.fault.Remaining > 0 {
		m.fault.Remaining--
		if m.fault.Remaining == 0 {
			m.fault = nil
		}
	}
	return true
}

// scenarioFixturesToAPI translates the YAML-friendly APIFixture
// shape to the exact JSON envelope api-sports.io returns —
// production code's json.Unmarshal into apifootball.APIFixture
// consumes this shape directly.
func scenarioFixturesToAPI(fixtures []APIFixture) []map[string]any {
	out := make([]map[string]any, 0, len(fixtures))
	for _, f := range fixtures {
		out = append(out, map[string]any{
			"fixture": map[string]any{
				"id":        f.ID,
				"referee":   nil,
				"timezone":  "UTC",
				"date":      f.Kickoff.UTC().Format("2006-01-02T15:04:05-07:00"),
				"timestamp": f.Kickoff.Unix(),
				"venue":     map[string]any{"id": nil, "name": "", "city": ""},
				"status": map[string]any{
					"long":    f.StatusLong,
					"short":   f.StatusShort,
					"elapsed": f.StatusElapsed,
					"extra":   f.StatusExtra,
				},
			},
			"league": map[string]any{
				"id":      f.LeagueID,
				"name":    f.LeagueName,
				"country": "",
				"logo":    "",
				"flag":    "",
				"season":  f.LeagueSeason,
				"round":   "",
			},
			"teams": map[string]any{
				"home": map[string]any{"id": f.HomeID, "name": f.HomeName, "logo": ""},
				"away": map[string]any{"id": f.AwayID, "name": f.AwayName, "logo": ""},
			},
			"goals": map[string]any{
				"home": f.GoalsHome,
				"away": f.GoalsAway,
			},
			"score": map[string]any{
				"halftime":  map[string]any{"home": nil, "away": nil},
				"fulltime":  map[string]any{"home": nil, "away": nil},
				"extratime": map[string]any{"home": nil, "away": nil},
				"penalty":   map[string]any{"home": nil, "away": nil},
			},
			"events": scenarioEventsToAPI(f.Events),
		})
	}
	return out
}

// scenarioEventsToAPI mirrors api-sports.io's per-fixture events array
// shape. Nil scenario events → empty array in the JSON (matches API's
// behavior on fixtures with no reported events).
func scenarioEventsToAPI(events []APIEvent) []map[string]any {
	if len(events) == 0 {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(events))
	for _, e := range events {
		out = append(out, map[string]any{
			"time": map[string]any{
				"elapsed": e.Minute,
				"extra":   e.Extra,
			},
			"team": map[string]any{
				"id":     e.TeamID,
				"name":   e.TeamName,
				"logo":   "",
				"winner": nil,
			},
			"player": map[string]any{
				"id":   e.PlayerID,
				"name": nullableString(e.PlayerName),
			},
			"assist": map[string]any{
				"id":   nil,
				"name": nil,
			},
			"type":     e.Type,
			"detail":   e.Detail,
			"comments": nil,
		})
	}
	return out
}

// nullableString returns nil for an empty string, matching how
// api-sports.io renders unknown players.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
