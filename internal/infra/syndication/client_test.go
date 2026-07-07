// Fast-fail + happy-path tests for the syndication client.
package syndication_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/syndication"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

func newFixture() (*syndication.Instruments, *logging.TestEmitter) {
	log := &logging.TestEmitter{}
	return syndication.RegisterMetrics(metrics.New(), log), log
}

func TestNewClient_FastFailGuards(t *testing.T) {
	ins, _ := newFixture()
	cases := []struct {
		name string
		cfg  config.SyndicationConfig
		ins  *syndication.Instruments
	}{
		{"nil-ins", config.SyndicationConfig{BaseURL: "http://x", UserAgent: "test"}, nil},
		{"empty-base", config.SyndicationConfig{BaseURL: "", UserAgent: "test"}, ins},
		{"empty-ua", config.SyndicationConfig{BaseURL: "http://x", UserAgent: ""}, ins},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := syndication.NewClient(tc.cfg, tc.ins); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestFetchJSON_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tweet_id":"123","text":"hi"}`))
	}))
	defer srv.Close()

	ins, log := newFixture()
	c, err := syndication.NewClient(config.SyndicationConfig{
		BaseURL: srv.URL, UserAgent: "test", Timeout: 5 * time.Second,
	}, ins)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	var got struct {
		TweetID string `json:"tweet_id"`
		Text    string `json:"text"`
	}
	if err := c.FetchJSON(context.Background(), "/tweet/123", &got); err != nil {
		t.Fatalf("FetchJSON: %v", err)
	}
	if got.TweetID != "123" || got.Text != "hi" {
		t.Errorf("got %+v", got)
	}
	if !log.HasAction(vocabulary.ModuleInfraSyndication, vocabulary.ActionSyndicationFetch) {
		t.Errorf("expected ActionSyndicationFetch; got %+v", log.Snapshot())
	}
}
