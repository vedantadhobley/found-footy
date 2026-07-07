// Integration + unit tests for the API-Football client. Uses an httptest
// mock server to simulate api-sports.io responses.
package apifootball_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

type testFixture struct {
	reg *metrics.Registry
	log *logging.TestEmitter
	ins *apifootball.Instruments
}

func newTestFixture() *testFixture {
	reg := metrics.New()
	log := &logging.TestEmitter{}
	ins := apifootball.RegisterMetrics(reg, log)
	return &testFixture{reg: reg, log: log, ins: ins}
}

// mockAPIServer stands up an httptest server that responds like
// api-sports.io. Individual tests tweak the response via handlers set on the mux.
type mockAPIServer struct {
	srv                  *httptest.Server
	statusStatusCode     int
	statusResponseBody   map[string]interface{}
	statusRatelimitRemain string
	statusQuotaRemain    string
	receivedKey          string
}

func newMockAPIServer() *mockAPIServer {
	m := &mockAPIServer{
		statusStatusCode: http.StatusOK,
		statusResponseBody: map[string]interface{}{
			"response": map[string]interface{}{
				"account": map[string]interface{}{
					"firstname": "test",
					"email":     "test@example.com",
				},
				"subscription": map[string]interface{}{
					"plan":   "Free",
					"end":    "2027-01-01",
					"active": true,
				},
				"requests": map[string]interface{}{
					"current":   1234,
					"limit_day": 100000,
				},
			},
			"errors": []interface{}{},
		},
		statusRatelimitRemain: "97",
		statusQuotaRemain:     "99123",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		m.receivedKey = r.Header.Get("x-rapidapi-key")
		if m.statusRatelimitRemain != "" {
			w.Header().Set("x-ratelimit-requests-remaining", m.statusRatelimitRemain)
		}
		if m.statusQuotaRemain != "" {
			w.Header().Set("x-rapidapi-requests-remaining", m.statusQuotaRemain)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(m.statusStatusCode)
		if m.statusStatusCode == http.StatusOK {
			_ = json.NewEncoder(w).Encode(m.statusResponseBody)
		} else {
			_, _ = w.Write([]byte(`{"error":"simulated"}`))
		}
	})
	m.srv = httptest.NewServer(mux)
	return m
}

func (m *mockAPIServer) URL() string { return m.srv.URL }
func (m *mockAPIServer) Close()      { m.srv.Close() }

func newClientAgainst(t *testing.T, ctx context.Context, baseURL string, fx *testFixture) *apifootball.Client {
	t.Helper()
	c, err := apifootball.NewClient(ctx, config.APIFootballConfig{
		BaseURL: baseURL,
		APIKey:  "test-key-abc123",
		Timeout: 5 * time.Second,
	}, fx.ins)
	if err != nil {
		t.Fatalf("apifootball.NewClient: %v", err)
	}
	return c
}

func scrapeMetrics(t *testing.T, reg *metrics.Registry) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	reg.Handler().ServeHTTP(w, req)
	body, _ := io.ReadAll(w.Result().Body)
	return string(body)
}

// TestClient_ConnectsAndSendsAuth — happy path. NewClient probes
// /status; verify the api-key header actually reached the server.
func TestClient_ConnectsAndSendsAuth(t *testing.T) {
	ctx := context.Background()
	m := newMockAPIServer()
	defer m.Close()

	fx := newTestFixture()
	_ = newClientAgainst(t, ctx, m.URL(), fx)

	if m.receivedKey != "test-key-abc123" {
		t.Errorf("server received x-rapidapi-key = %q, want test-key-abc123", m.receivedKey)
	}
	if !fx.log.HasAction(vocabulary.ModuleInfraAPIFootball, vocabulary.ActionAPIFootballCall) {
		t.Errorf("expected ActionAPIFootballCall from probe; got %+v", fx.log.Snapshot())
	}
}

// TestClient_ParsesRateLimitHeaders — verify the response-header parsing
// populates the gauges. Guards against a header-name typo silently
// dropping the observation.
func TestClient_ParsesRateLimitHeaders(t *testing.T) {
	ctx := context.Background()
	m := newMockAPIServer()
	defer m.Close()

	fx := newTestFixture()
	_ = newClientAgainst(t, ctx, m.URL(), fx)

	scrape := scrapeMetrics(t, fx.reg)
	for _, want := range []string{
		"found_footy_apifootball_ratelimit_remaining 97",
		"found_footy_apifootball_daily_quota_remaining 99123",
		`found_footy_apifootball_calls_total{endpoint="status",outcome="success"} 1`,
	} {
		if !strings.Contains(scrape, want) {
			t.Errorf("scrape missing %q; got:\n%s", want, scrape)
		}
	}
}

// TestClient_HandlesRateLimit429 — 429 gets its own outcome bucket,
// its own action, and does NOT get counted as generic failure.
func TestClient_HandlesRateLimit429(t *testing.T) {
	ctx := context.Background()
	m := newMockAPIServer()
	m.statusStatusCode = http.StatusTooManyRequests
	defer m.Close()

	fx := newTestFixture()
	_, err := apifootball.NewClient(ctx, config.APIFootballConfig{
		BaseURL: m.URL(),
		APIKey:  "k",
		Timeout: 5 * time.Second,
	}, fx.ins)
	if err == nil {
		t.Fatal("expected error from 429 during probe, got nil")
	}
	if !fx.log.HasAction(vocabulary.ModuleInfraAPIFootball, vocabulary.ActionAPIFootballRateLimited) {
		t.Errorf("expected ActionAPIFootballRateLimited; got %+v", fx.log.Snapshot())
	}
	scrape := scrapeMetrics(t, fx.reg)
	if !strings.Contains(scrape, `found_footy_apifootball_calls_total{endpoint="status",outcome="rate_limited"} 1`) {
		t.Errorf("scrape missing rate_limited counter; got:\n%s", scrape)
	}
}

// TestClient_Handles5xx — non-2xx non-429 = generic failure.
func TestClient_Handles5xx(t *testing.T) {
	ctx := context.Background()
	m := newMockAPIServer()
	m.statusStatusCode = http.StatusInternalServerError
	defer m.Close()

	fx := newTestFixture()
	_, err := apifootball.NewClient(ctx, config.APIFootballConfig{
		BaseURL: m.URL(),
		APIKey:  "k",
		Timeout: 5 * time.Second,
	}, fx.ins)
	if err == nil {
		t.Fatal("expected error from 500, got nil")
	}
	scrape := scrapeMetrics(t, fx.reg)
	if !strings.Contains(scrape, `found_footy_apifootball_calls_total{endpoint="status",outcome="failure"} 1`) {
		t.Errorf("scrape missing failure counter; got:\n%s", scrape)
	}
}

// TestNewClient_NilInstruments_Errors — same fast-fail guard as every
// other adapter.
func TestNewClient_NilInstruments_Errors(t *testing.T) {
	_, err := apifootball.NewClient(context.Background(),
		config.APIFootballConfig{BaseURL: "http://x", APIKey: "k"}, nil)
	if err == nil {
		t.Fatal("expected error for nil Instruments, got nil")
	}
}

// TestNewClient_EmptyKey_Errors — no key = no client.
func TestNewClient_EmptyKey_Errors(t *testing.T) {
	fx := newTestFixture()
	_, err := apifootball.NewClient(context.Background(),
		config.APIFootballConfig{BaseURL: "http://x", APIKey: ""}, fx.ins)
	if err == nil {
		t.Fatal("expected error for empty API_FOOTBALL_KEY, got nil")
	}
}

// TestNewClient_EmptyBaseURL_Errors — no base URL = no client.
func TestNewClient_EmptyBaseURL_Errors(t *testing.T) {
	fx := newTestFixture()
	_, err := apifootball.NewClient(context.Background(),
		config.APIFootballConfig{BaseURL: "", APIKey: "k"}, fx.ins)
	if err == nil {
		t.Fatal("expected error for empty API_FOOTBALL_BASE_URL, got nil")
	}
}

// TestNewClient_UnreachableHost_ErrorsQuickly — bounded by Timeout.
func TestNewClient_UnreachableHost_ErrorsQuickly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration-ish test skipped in -short mode")
	}

	fx := newTestFixture()
	start := time.Now()
	_, err := apifootball.NewClient(context.Background(), config.APIFootballConfig{
		BaseURL: "http://192.0.2.1:80", // RFC 6890 discard address
		APIKey:  "k",
		Timeout: 2 * time.Second,
	}, fx.ins)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error for unreachable host, got nil")
	}
	if elapsed > 6*time.Second {
		t.Errorf("NewClient took %v, want ≤ 6s (timeout was 2s)", elapsed)
	}
}
