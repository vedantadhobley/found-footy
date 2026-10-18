// Gateway contract tests exercise the pinned SDK's real error and header paths.
package llm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vedantadhobley/found-footy/internal/infra/llm"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// gatewayServer keeps synthetic inference independent of the real Control service.
func gatewayServer(t *testing.T, chat http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"test-model"}]}`))
			return
		}
		chat(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestChatControlErrors preserves status/code evidence without conflating 503 with admission.
func TestChatControlErrors(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		status     int
		want       error
	}{
		{"capacity", "capacity_exceeded", 429, llm.ErrCapExceeded},
		{"generic_limit", "rate_limit_exceeded", 429, llm.ErrRateLimited},
		{"not_ready", "model_unavailable", 503, llm.ErrUnavailable},
		{"backend_unavailable", "backend_unavailable", 503, llm.ErrUnavailable},
		{"mismatched_capacity", "capacity_exceeded", 503, llm.ErrUnavailable},
		{"backend_timeout", "backend_timeout", 504, llm.ErrUnavailable},
		{"bad_request", "unsupported_parameter", 400, llm.ErrInvalidRequest},
		{"not_configured", "model_not_found", 404, llm.ErrModelNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := gatewayServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("X-Request-ID", "req_"+tc.name)
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": tc.code, "message": "synthetic", "type": "server_error"}})
			})
			fx := newTestFixture()
			client := newClientAgainst(t, context.Background(), srv.URL, fx, 1)
			defer client.Close()
			_, err := client.Chat(context.Background(), simpleChat("ocr"))
			require.ErrorIs(t, err, tc.want)
			var responseErr *llm.ResponseError
			require.ErrorAs(t, err, &responseErr)
			require.Equal(t, llm.ResponseInfo{RequestID: "req_" + tc.name, StatusCode: tc.status, ErrorCode: tc.code, RetryAfter: 7 * time.Second}, responseErr.Info)
			require.True(t, responseErr.Info.Valid())
			fields := lastCallFields(t, fx.log, vocabulary.ActionLLMChatFailed)
			require.Equal(t, "req_"+tc.name, fields["model_request_id"])
			require.Equal(t, tc.code, fields["gateway_error_code"])
			require.Equal(t, int64(7000), fields["retry_after_ms"])
		})
	}
}

// TestChatControlMalformedBody retains headers/status when the SDK cannot decode an error body.
func TestChatControlMalformedBody(t *testing.T) {
	srv := gatewayServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-ID", "req_malformed")
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(503)
		_, _ = w.Write([]byte("not a JSON error"))
	})
	fx := newTestFixture()
	client := newClientAgainst(t, context.Background(), srv.URL, fx, 1)
	defer client.Close()
	_, err := client.Chat(context.Background(), simpleChat("ocr"))
	require.ErrorIs(t, err, llm.ErrUnavailable)
	var responseErr *llm.ResponseError
	require.ErrorAs(t, err, &responseErr)
	require.Equal(t, "req_malformed", responseErr.Info.RequestID)
	require.Equal(t, 503, responseErr.Info.StatusCode)
	require.Empty(t, responseErr.Info.ErrorCode)
}

// TestChatControlResponseIDs are request-scoped even when responses complete concurrently.
func TestChatControlResponseIDs(t *testing.T) {
	srv := gatewayServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Messages []struct{ Content string } }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) != 1 {
			http.Error(w, "invalid synthetic request", 400)
			return
		}
		id := req.Messages[0].Content
		w.Header().Set("X-Request-ID", id)
		_, _ = w.Write([]byte(`{"model":"test-model","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	})
	fx := newTestFixture()
	client := newClientAgainst(t, context.Background(), srv.URL, fx, 4)
	defer client.Close()
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() {
			id := "req_" + strconv.Itoa(i)
			resp, err := client.Chat(context.Background(), simpleChat(id))
			if err != nil {
				t.Errorf("chat: %v", err)
				return
			}
			if resp.ResponseInfo.RequestID != id {
				t.Errorf("request %s received metadata %+v", id, resp.ResponseInfo)
			}
		})
	}
	wg.Wait()
	fields := lastCallFields(t, fx.log, vocabulary.ActionLLMChatCall)
	require.NotEmpty(t, fields["model_request_id"])
}

// TestChatControlInvalidMetadata drops unsafe identifiers and invalid retry guidance.
func TestChatControlInvalidMetadata(t *testing.T) {
	srv := gatewayServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Request-ID", strings.Repeat("x", 129))
		w.Header().Set("Retry-After", "-1")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"code":"raw response text","message":"synthetic"}}`))
	})
	fx := newTestFixture()
	client := newClientAgainst(t, context.Background(), srv.URL, fx, 1)
	defer client.Close()
	_, err := client.Chat(context.Background(), simpleChat("ocr"))
	var responseErr *llm.ResponseError
	require.ErrorAs(t, err, &responseErr)
	require.Equal(t, llm.ResponseInfo{StatusCode: 429}, responseErr.Info)
}
