// Fast-fail + happy-path tests for the internal Twitter service client.
package twitter_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/twitter"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

func newFixture() (*twitter.Instruments, *logging.TestEmitter) {
	log := &logging.TestEmitter{}
	return twitter.RegisterMetrics(metrics.New(), log), log
}

// mockTwitter stands up a minimal /health + /search server.
func mockTwitter(t *testing.T, searchResp twitter.SearchResponse, health200 bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if !health200 {
			http.Error(w, `{"status":"down"}`, http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","authenticated":true}`))
	})
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(searchResp)
	})
	return httptest.NewServer(mux)
}

func TestNewClient_HealthProbePasses(t *testing.T) {
	srv := mockTwitter(t, twitter.SearchResponse{}, true)
	defer srv.Close()

	ins, log := newFixture()
	c, err := twitter.NewClient(context.Background(), config.TwitterConfig{
		BaseURL: srv.URL, SearchTimeout: 5 * time.Second, DownloadTimeout: 30 * time.Second,
	}, ins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if !log.HasAction(vocabulary.ModuleInfraTwitter, vocabulary.ActionTwitterConnected) {
		t.Errorf("expected ActionTwitterConnected; got %+v", log.Snapshot())
	}
}

func TestNewClient_HealthProbeFails(t *testing.T) {
	srv := mockTwitter(t, twitter.SearchResponse{}, false)
	defer srv.Close()

	ins, log := newFixture()
	_, err := twitter.NewClient(context.Background(), config.TwitterConfig{
		BaseURL: srv.URL, SearchTimeout: 5 * time.Second, DownloadTimeout: 30 * time.Second,
	}, ins)
	if err == nil {
		t.Fatal("expected error from failing /health probe")
	}
	if !log.HasAction(vocabulary.ModuleInfraTwitter, vocabulary.ActionTwitterConnectFailed) {
		t.Errorf("expected ActionTwitterConnectFailed; got %+v", log.Snapshot())
	}
}

func TestSearch_HappyPath(t *testing.T) {
	srv := mockTwitter(t, twitter.SearchResponse{
		Status: "success",
		Count:  2,
		Videos: []twitter.VideoRef{
			{TweetURL: "https://x.com/a/status/1", VideoPageURL: "http://v1", DurationSeconds: 15.5},
			{TweetURL: "https://x.com/b/status/2", VideoPageURL: "http://v2", DurationSeconds: 22.0},
		},
	}, true)
	defer srv.Close()

	ins, log := newFixture()
	c, err := twitter.NewClient(context.Background(), config.TwitterConfig{
		BaseURL: srv.URL, SearchTimeout: 5 * time.Second, DownloadTimeout: 30 * time.Second,
	}, ins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	res, err := c.Search(context.Background(), "", twitter.SearchRequest{Query: "goal salah"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if res.Count != 2 || len(res.Videos) != 2 {
		t.Errorf("Search result count = %d, videos = %d", res.Count, len(res.Videos))
	}
	if !log.HasAction(vocabulary.ModuleInfraTwitter, vocabulary.ActionTwitterSearch) {
		t.Errorf("expected ActionTwitterSearch; got %+v", log.Snapshot())
	}
}

func TestNewClient_FastFailGuards(t *testing.T) {
	ins, _ := newFixture()
	if _, err := twitter.NewClient(context.Background(),
		config.TwitterConfig{BaseURL: "http://x"}, nil); err == nil {
		t.Fatal("nil ins should error")
	}
	if _, err := twitter.NewClient(context.Background(),
		config.TwitterConfig{BaseURL: ""}, ins); err == nil {
		t.Fatal("empty base URL should error")
	}
}
