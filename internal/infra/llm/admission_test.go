// Admission tests separate queued work from active HTTP calls without using a live model.
package llm_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/llm"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// TestChat_AdmissionMetricsAndCancellation proves a waiting call is not active or an HTTP attempt.
func TestChat_AdmissionMetricsAndCancellation(t *testing.T) {
	for _, cancelWaiting := range []bool{false, true} {
		name := "acquired"
		if cancelWaiting {
			name = "canceled"
		}
		t.Run(name, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/models" {
					_, _ = w.Write([]byte(`{"data":[{"id":"test-model"}]}`))
					return
				}
				if requests.Add(1) == 1 {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				_, _ = w.Write([]byte(`{"model":"test-model","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
			}))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			fx := newTestFixture()
			c := newClientAgainst(t, ctx, srv.URL, fx, 1)
			defer c.Close()
			first := make(chan error, 1)
			go func() { _, err := c.Chat(ctx, simpleChat("first")); first <- err }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("first request did not start")
			}
			secondCtx, secondCancel := context.WithCancel(ctx)
			defer secondCancel()
			second := make(chan error, 1)
			go func() { _, err := c.Chat(secondCtx, simpleChat("second")); second <- err }()
			require.Eventually(t, func() bool { return strings.Contains(scrapeMetrics(t, fx.reg), "found_footy_llm_waiting_calls 1\n") }, time.Second, time.Millisecond)
			require.Contains(t, scrapeMetrics(t, fx.reg), "found_footy_llm_concurrent_calls 1\n")
			if cancelWaiting {
				secondCancel()
				err := <-second
				require.ErrorIs(t, err, context.Canceled)
				require.ErrorIs(t, err, llm.ErrLocalAdmission)
				require.Equal(t, int32(1), requests.Load())
				fields := lastCallFields(t, fx.log, vocabulary.ActionLLMChatFailed)
				require.Equal(t, "local_admission", fields["phase"])
				require.Equal(t, int64(0), fields["request_ms"])
				require.Contains(t, fields, "admission_wait_ms")
			}
			close(release)
			require.NoError(t, <-first)
			wantRequests := int32(1)
			if !cancelWaiting {
				require.NoError(t, <-second)
				wantRequests = 2
			}
			require.Equal(t, wantRequests, requests.Load())
			scrape := scrapeMetrics(t, fx.reg)
			require.Contains(t, scrape, "found_footy_llm_waiting_calls 0\n")
			require.Contains(t, scrape, "found_footy_llm_concurrent_calls 0\n")
			if cancelWaiting {
				require.Contains(t, scrape, `found_footy_llm_admission_wait_seconds_count{outcome="canceled"} 1`)
				require.Contains(t, scrape, `found_footy_llm_call_duration_seconds_count{kind="chat"} 1`)
			} else {
				require.Contains(t, scrape, `found_footy_llm_admission_wait_seconds_count{outcome="acquired"} 2`)
				require.Contains(t, scrape, `found_footy_llm_call_duration_seconds_count{kind="chat"} 2`)
			}
			fields := lastCallFields(t, fx.log, vocabulary.ActionLLMChatCall)
			require.Equal(t, "request", fields["phase"])
			require.Contains(t, fields, "admission_wait_ms")
			require.Equal(t, fields["elapsed_ms"], fields["request_ms"])
		})
	}
}

// TestChat_RequestTimeoutIsNotLocalAdmission preserves the actual request deadline and lane release.
func TestChat_RequestTimeoutIsNotLocalAdmission(t *testing.T) {
	m := newMockLLMServer()
	m.chatDelay = 100 * time.Millisecond
	defer m.Close()
	fx := newTestFixture()
	c, err := llm.NewClient(context.Background(), config.LLMConfig{
		Endpoint: m.URL(), APIVersionPath: "/v1", APIKey: "test", ChatConcurrencyCap: 1,
		ConnectTimeout: time.Second, RequestTimeout: 10 * time.Millisecond,
	}, fx.ins)
	require.NoError(t, err)
	defer c.Close()
	_, err = c.Chat(context.Background(), simpleChat("timeout"))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, errors.Is(err, llm.ErrLocalAdmission))
	fields := lastCallFields(t, fx.log, vocabulary.ActionLLMChatFailed)
	require.Equal(t, "request", fields["phase"])
	require.Greater(t, fields["request_ms"].(int64), int64(0))
	require.Contains(t, scrapeMetrics(t, fx.reg), "found_footy_llm_concurrent_calls 0\n")
}

// lastCallFields inspects bounded timing fields without depending on rendered log text.
func lastCallFields(t *testing.T, log *logging.TestEmitter, action vocabulary.Action) map[string]any {
	t.Helper()
	entries := log.Snapshot()
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Action != action {
			continue
		}
		fields := make(map[string]any)
		for _, f := range entries[i].Fields {
			fields[f.Key] = f.Value
		}
		return fields
	}
	t.Fatalf("missing action %s", action)
	return nil
}
