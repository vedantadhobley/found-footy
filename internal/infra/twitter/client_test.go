// Fast-fail + happy-path tests for the internal Twitter service client.
package twitter_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	twittercontract "github.com/vedantadhobley/found-footy/internal/contract/twittersearch"
	"github.com/vedantadhobley/found-footy/internal/infra/twitter"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

func newFixture() (*twitter.Instruments, *logging.TestEmitter) {
	log := &logging.TestEmitter{}
	return twitter.RegisterMetrics(metrics.New(), log), log
}

// mockTwitter stands up a minimal /search server.
func mockTwitter(t *testing.T, searchResp twitter.SearchResponse) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
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

func TestNewClient_DoesNotRequireLiveService(t *testing.T) {
	ins, _ := newFixture()
	c, err := twitter.NewClient(config.TwitterConfig{
		BaseURL: "http://127.0.0.1:1", SearchTimeout: 50 * time.Millisecond,
	}, ins)
	if err != nil {
		t.Fatalf("NewClient with unavailable service: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestSearch_HappyPath(t *testing.T) {
	srv := mockTwitter(t, twitter.SearchResponse{
		Status:      "success",
		ResultState: twittercontract.ResultRendered,
		Evidence: twittercontract.SearchEvidence{
			FinalURL: "https://x.com/search", TimelineSeen: true, TimelineStatus: 200,
		},
		Count:           2,
		StopReason:      "age",
		Scrolls:         3,
		InitialArticles: 4,
		TweetsParsed:    7,
		VideoTweets:     5,
		Videos: []twitter.VideoRef{
			{TweetURL: "https://x.com/a/status/1", VideoPageURL: "http://v1", DurationSeconds: 15.5},
			{TweetURL: "https://x.com/b/status/2", VideoPageURL: "http://v2", DurationSeconds: 22.0},
		},
	})
	defer srv.Close()

	ins, log := newFixture()
	c, err := twitter.NewClient(config.TwitterConfig{
		BaseURL: srv.URL, SearchTimeout: 5 * time.Second,
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
	if res.StopReason != "age" || res.Scrolls != 3 || res.InitialArticles != 4 ||
		res.TweetsParsed != 7 || res.VideoTweets != 5 {
		t.Errorf("Search diagnostics were not preserved: %+v", res)
	}
	if res.ResultState != twittercontract.ResultRendered ||
		res.Evidence.TimelineStatus != 200 {
		t.Errorf("Search classification was not preserved: %+v", res)
	}
	if !log.HasAction(vocabulary.ModuleInfraTwitter, vocabulary.ActionTwitterSearch) {
		t.Errorf("expected ActionTwitterSearch; got %+v", log.Snapshot())
	}
}

func TestSearch_RecoversAfterServiceBecomesReady(t *testing.T) {
	var ready atomic.Bool
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/search", func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if !ready.Load() {
			http.Error(w, `{"status":"starting"}`, http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(twitter.SearchResponse{Status: "success"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ins, _ := newFixture()
	c, err := twitter.NewClient(config.TwitterConfig{
		BaseURL: srv.URL, SearchTimeout: time.Second,
	}, ins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("construction made %d remote calls, want 0", got)
	}

	if _, err := c.Search(context.Background(), "", twitter.SearchRequest{Query: "goal"}); err == nil {
		t.Fatal("Search while service is starting = nil error")
	}
	ready.Store(true)
	if _, err := c.Search(context.Background(), "", twitter.SearchRequest{Query: "goal"}); err != nil {
		t.Fatalf("Search after service recovery: %v", err)
	}
}

func TestSearch_PreservesClassifiedNon2xxEvidence(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/search", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(twittercontract.SearchErrorBody{
			Status:      "error",
			ErrorClass:  "auth_expired",
			Message:     strings.Repeat("session unauthenticated ", 256),
			ResultState: twittercontract.ResultLogin,
			Evidence: twittercontract.SearchEvidence{
				FinalURL: "https://x.com/i/flow/login", AppShell: false,
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ins, _ := newFixture()
	c, err := twitter.NewClient(config.TwitterConfig{
		BaseURL: srv.URL, SearchTimeout: time.Second,
	}, ins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.Search(context.Background(), "", twitter.SearchRequest{Query: "goal"})
	var searchErr *twitter.SearchError
	if !errors.As(err, &searchErr) {
		t.Fatalf("Search error = %T %v, want *SearchError", err, err)
	}
	if searchErr.ResultState != twittercontract.ResultLogin ||
		searchErr.Evidence.FinalURL != "https://x.com/i/flow/login" {
		t.Fatalf("classified error = %+v", searchErr)
	}
}

func TestSearch_LogsClassifiedHTTP200OutageAsFailure(t *testing.T) {
	srv := mockTwitter(t, twitter.SearchResponse{
		Status:      "unavailable",
		ResultState: twittercontract.ResultUnknownTimeout,
		Evidence: twittercontract.SearchEvidence{
			AppShell: true,
		},
		StopReason: "feed_timeout",
	})
	defer srv.Close()

	ins, log := newFixture()
	c, err := twitter.NewClient(config.TwitterConfig{
		BaseURL: srv.URL, SearchTimeout: time.Second,
	}, ins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.Search(context.Background(), "", twitter.SearchRequest{Query: "goal"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !log.HasAction(vocabulary.ModuleInfraTwitter, vocabulary.ActionTwitterSearchFailed) {
		t.Fatalf("expected ActionTwitterSearchFailed; got %+v", log.Snapshot())
	}
}

func TestVerifyPostsToForcedAuthEndpoint(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/verify", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ins, log := newFixture()
	c, err := twitter.NewClient(config.TwitterConfig{BaseURL: srv.URL, SearchTimeout: time.Second}, ins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Verify(context.Background()); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("verify calls = %d, want 1", calls.Load())
	}
	if !log.HasAction(vocabulary.ModuleInfraTwitter, vocabulary.ActionTwitterVerify) {
		t.Fatalf("expected ActionTwitterVerify; got %+v", log.Snapshot())
	}
}

func TestVerifyReturnsNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "expired", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	ins, _ := newFixture()
	c, err := twitter.NewClient(config.TwitterConfig{BaseURL: srv.URL, SearchTimeout: time.Second}, ins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Verify(context.Background()); err == nil {
		t.Fatal("Verify = nil, want non-2xx error")
	}
}

func TestNewClient_FastFailGuards(t *testing.T) {
	ins, _ := newFixture()
	if _, err := twitter.NewClient(
		config.TwitterConfig{BaseURL: "http://x"}, nil); err == nil {
		t.Fatal("nil ins should error")
	}
	if _, err := twitter.NewClient(
		config.TwitterConfig{BaseURL: ""}, ins); err == nil {
		t.Fatal("empty base URL should error")
	}
}
