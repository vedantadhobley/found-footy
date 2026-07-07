// Integration + unit tests for the LLM client wrapper. Uses a mock
// OpenAI-compatible server via httptest — no external LLM required.
package llm_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/llm"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

type testFixture struct {
	reg *metrics.Registry
	log *logging.TestEmitter
	ins *llm.Instruments
}

func newTestFixture() *testFixture {
	reg := metrics.New()
	log := &logging.TestEmitter{}
	ins := llm.RegisterMetrics(reg, log)
	return &testFixture{reg: reg, log: log, ins: ins}
}

// mockLLMServer is a minimal /v1/models + /v1/chat/completions handler
// that responds like a real llama.cpp/OpenAI server. Individual tests
// tweak its behavior via closures.
type mockLLMServer struct {
	srv               *httptest.Server
	modelsResponse    openai.ModelsList
	chatDelay         time.Duration
	chatShouldFail    bool
	chatResponse      openai.ChatCompletionResponse
	concurrentPeak    int32
	concurrentCurrent int32
}

func newMockLLMServer() *mockLLMServer {
	m := &mockLLMServer{
		modelsResponse: openai.ModelsList{
			Models: []openai.Model{
				{ID: "test-model-vl", Object: "model", OwnedBy: "test"},
			},
		},
		chatResponse: openai.ChatCompletionResponse{
			ID:      "chatcmpl-test",
			Object:  "chat.completion",
			Model:   "test-model-vl",
			Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Role: "assistant", Content: "test reply"}}},
			Usage:   openai.Usage{PromptTokens: 12, CompletionTokens: 5, TotalTokens: 17},
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(m.modelsResponse)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt32(&m.concurrentCurrent, 1)
		defer atomic.AddInt32(&m.concurrentCurrent, -1)
		// Track high-water mark of concurrent in-flight chat requests
		// so tests can assert the semaphore capped it.
		for {
			peak := atomic.LoadInt32(&m.concurrentPeak)
			if cur <= peak || atomic.CompareAndSwapInt32(&m.concurrentPeak, peak, cur) {
				break
			}
		}
		if m.chatDelay > 0 {
			time.Sleep(m.chatDelay)
		}
		if m.chatShouldFail {
			http.Error(w, `{"error":{"message":"server angry"}}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(m.chatResponse)
	})
	m.srv = httptest.NewServer(mux)
	return m
}

func (m *mockLLMServer) URL() string    { return m.srv.URL }
func (m *mockLLMServer) Close()         { m.srv.Close() }
func (m *mockLLMServer) Peak() int      { return int(atomic.LoadInt32(&m.concurrentPeak)) }
func (m *mockLLMServer) ResetPeak()     { atomic.StoreInt32(&m.concurrentPeak, 0) }

func newClientAgainst(t *testing.T, ctx context.Context, endpoint string, fx *testFixture, cap int) *llm.Client {
	t.Helper()
	c, err := llm.NewClient(ctx, config.LLMConfig{
		Endpoint:           endpoint,
		APIKey:             "test",
		ChatConcurrencyCap: cap,
		ConnectTimeout:     5 * time.Second,
		RequestTimeout:     10 * time.Second,
	}, fx.ins)
	if err != nil {
		t.Fatalf("llm.NewClient: %v", err)
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

// TestNewClient_ConnectsAndAutoDiscoversModel — the ChatModel env var
// is empty, so New should pick the first model from /v1/models.
func TestNewClient_ConnectsAndAutoDiscoversModel(t *testing.T) {
	ctx := context.Background()
	m := newMockLLMServer()
	defer m.Close()

	fx := newTestFixture()
	c := newClientAgainst(t, ctx, m.URL(), fx, 2)
	defer c.Close()

	if !fx.log.HasAction(vocabulary.ModuleInfraLLM, vocabulary.ActionLLMConnected) {
		t.Errorf("expected ActionLLMConnected; got %+v", fx.log.Snapshot())
	}
	if !fx.log.HasAction(vocabulary.ModuleInfraLLM, vocabulary.ActionLLMModelDiscovered) {
		t.Errorf("expected ActionLLMModelDiscovered; got %+v", fx.log.Snapshot())
	}
	if c.ChatModel() != "test-model-vl" {
		t.Errorf("ChatModel = %q, want test-model-vl", c.ChatModel())
	}
}

// TestChat_HappyPath sends a chat request through the wrapper, verifies
// the response comes back intact, and asserts the success counter +
// token counters + histogram all fired.
func TestChat_HappyPath(t *testing.T) {
	ctx := context.Background()
	m := newMockLLMServer()
	defer m.Close()

	fx := newTestFixture()
	c := newClientAgainst(t, ctx, m.URL(), fx, 2)
	defer c.Close()

	resp, err := c.Chat(ctx, openai.ChatCompletionRequest{
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Choices[0].Message.Content != "test reply" {
		t.Errorf("reply = %q, want 'test reply'", resp.Choices[0].Message.Content)
	}

	scrape := scrapeMetrics(t, fx.reg)
	for _, want := range []string{
		`found_footy_llm_calls_total{kind="chat",outcome="success"} 1`,
		`found_footy_llm_tokens_used_total{direction="prompt",kind="chat"} 12`,
		`found_footy_llm_tokens_used_total{direction="completion",kind="chat"} 5`,
		`found_footy_llm_call_duration_seconds_count{kind="chat"} 1`,
	} {
		if !strings.Contains(scrape, want) {
			t.Errorf("scrape missing %q; got:\n%s", want, scrape)
		}
	}
}

// TestChat_ServerError verifies error path — the wrapper reports
// failure metric + WARN log without panicking.
func TestChat_ServerError(t *testing.T) {
	ctx := context.Background()
	m := newMockLLMServer()
	m.chatShouldFail = true
	defer m.Close()

	fx := newTestFixture()
	c := newClientAgainst(t, ctx, m.URL(), fx, 2)
	defer c.Close()

	_, err := c.Chat(ctx, openai.ChatCompletionRequest{
		Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error from mock 500, got nil")
	}
	if !fx.log.HasAction(vocabulary.ModuleInfraLLM, vocabulary.ActionLLMChatFailed) {
		t.Errorf("expected ActionLLMChatFailed; got %+v", fx.log.Snapshot())
	}
	scrape := scrapeMetrics(t, fx.reg)
	if !strings.Contains(scrape, `found_footy_llm_calls_total{kind="chat",outcome="failure"} 1`) {
		t.Errorf("scrape missing failure counter; got:\n%s", scrape)
	}
}

// TestChat_ConcurrencyCap fires N concurrent chat calls with a cap of
// 2 and asserts the server never saw more than 2 in flight at once.
// This is the load-bearing invariant that keeps us within joi's
// max_parallel=2 limit.
func TestChat_ConcurrencyCap(t *testing.T) {
	ctx := context.Background()
	m := newMockLLMServer()
	m.chatDelay = 100 * time.Millisecond // hold each request long enough for concurrency to build
	defer m.Close()

	fx := newTestFixture()
	c := newClientAgainst(t, ctx, m.URL(), fx, 2)
	defer c.Close()
	m.ResetPeak() // /v1/models probe already ran; we care only about chat concurrency

	const N = 6
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Chat(ctx, openai.ChatCompletionRequest{
				Messages: []openai.ChatCompletionMessage{{Role: "user", Content: "hi"}},
			})
		}()
	}
	wg.Wait()

	if got := m.Peak(); got > 2 {
		t.Errorf("mock server saw peak in-flight = %d, want ≤ 2 (semaphore breached)", got)
	}
}

// TestNewClient_NilInstruments_Errors — same fast-fail guard as every
// other adapter.
func TestNewClient_NilInstruments_Errors(t *testing.T) {
	_, err := llm.NewClient(context.Background(),
		config.LLMConfig{Endpoint: "http://x", ChatConcurrencyCap: 2}, nil)
	if err == nil {
		t.Fatal("expected error for nil Instruments, got nil")
	}
}

// TestNewClient_EmptyEndpoint_Errors — no endpoint = no client.
func TestNewClient_EmptyEndpoint_Errors(t *testing.T) {
	fx := newTestFixture()
	_, err := llm.NewClient(context.Background(),
		config.LLMConfig{ChatConcurrencyCap: 2}, fx.ins)
	if err == nil {
		t.Fatal("expected error for empty LLM_ENDPOINT_URL, got nil")
	}
}

// TestNewClient_ZeroConcurrencyCap_Errors — a cap of 0 would deadlock
// on the first Chat call (semaphore never has room). Reject at
// construction.
func TestNewClient_ZeroConcurrencyCap_Errors(t *testing.T) {
	fx := newTestFixture()
	_, err := llm.NewClient(context.Background(),
		config.LLMConfig{Endpoint: "http://x", ChatConcurrencyCap: 0}, fx.ins)
	if err == nil {
		t.Fatal("expected error for ChatConcurrencyCap=0, got nil")
	}
}

// TestNewClient_UnreachableHost_ErrorsQuickly bounds startup by
// ConnectTimeout.
func TestNewClient_UnreachableHost_ErrorsQuickly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration-ish test skipped in -short mode")
	}

	fx := newTestFixture()
	start := time.Now()
	_, err := llm.NewClient(context.Background(), config.LLMConfig{
		Endpoint:           "http://192.0.2.1:8080", // RFC 6890 discard
		APIKey:             "x",
		ChatConcurrencyCap: 2,
		ConnectTimeout:     2 * time.Second,
	}, fx.ins)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error for unreachable host, got nil")
	}
	if elapsed > 6*time.Second {
		t.Errorf("NewClient took %v, want ≤ 6s (timeout was 2s)", elapsed)
	}
	if !fx.log.HasAction(vocabulary.ModuleInfraLLM, vocabulary.ActionLLMConnectFailed) {
		t.Errorf("expected ActionLLMConnectFailed; got %+v", fx.log.Snapshot())
	}
}
