// Integration + unit tests for the LLM client wrapper. Uses a mock
// OpenAI-compatible server via httptest — no external LLM required.
package llm_test

import (
	"context"
	"encoding/json"
	"errors"
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
// tweak its behavior via closures. The server responds in openai's
// wire-format — that's what the adapter's HTTP client actually
// dispatches; callers of the ADAPTER still only see domain types.
type mockLLMServer struct {
	srv               *httptest.Server
	modelsResponse    openai.ModelsList
	chatDelay         time.Duration
	chatStatusCode    int // 0 = 200 with chatResponse; anything else = that status + JSON error body
	chatRawResponse   []byte
	chatResponse      openai.ChatCompletionResponse
	concurrentPeak    int32
	concurrentCurrent int32
	lastChatBody      []byte // raw request body of the most recent /chat/completions call
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
		if b, err := io.ReadAll(r.Body); err == nil {
			m.lastChatBody = b
		}
		cur := atomic.AddInt32(&m.concurrentCurrent, 1)
		defer atomic.AddInt32(&m.concurrentCurrent, -1)
		for {
			peak := atomic.LoadInt32(&m.concurrentPeak)
			if cur <= peak || atomic.CompareAndSwapInt32(&m.concurrentPeak, peak, cur) {
				break
			}
		}
		if m.chatDelay > 0 {
			time.Sleep(m.chatDelay)
		}
		if m.chatStatusCode != 0 && m.chatStatusCode != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(m.chatStatusCode)
			_, _ = w.Write([]byte(`{"error":{"message":"simulated","type":"simulated","code":"simulated"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if m.chatRawResponse != nil {
			_, _ = w.Write(m.chatRawResponse)
			return
		}
		_ = json.NewEncoder(w).Encode(m.chatResponse)
	})
	m.srv = httptest.NewServer(mux)
	return m
}

func (m *mockLLMServer) URL() string { return m.srv.URL }
func (m *mockLLMServer) Close()      { m.srv.Close() }
func (m *mockLLMServer) Peak() int   { return int(atomic.LoadInt32(&m.concurrentPeak)) }
func (m *mockLLMServer) ResetPeak()  { atomic.StoreInt32(&m.concurrentPeak, 0) }

func newClientAgainst(t *testing.T, ctx context.Context, endpoint string, fx *testFixture, cap int) *llm.Client {
	t.Helper()
	c, err := llm.NewClient(ctx, config.LLMConfig{
		Endpoint:           endpoint,
		APIVersionPath:     "/v1",
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

// simpleChat builds a minimal user-role ChatRequest with the given text.
func simpleChat(text string) llm.ChatRequest {
	return llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: llm.RoleUser, Content: text}},
	}
}

// TestNewClient_ConnectsAndAutoDiscoversModel — probe succeeds, first
// model auto-selected, both actions emitted.
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

// TestClose_EmitsLLMClosedAction — the audit Critical #1 regression
// guard. Close() must emit ActionLLMClosed, NOT ActionLLMConnected.
func TestClose_EmitsLLMClosedAction(t *testing.T) {
	ctx := context.Background()
	m := newMockLLMServer()
	defer m.Close()

	fx := newTestFixture()
	c := newClientAgainst(t, ctx, m.URL(), fx, 2)

	// Reset captured entries after the constructor path so we're only
	// asserting on Close().
	fx.log.Reset()
	c.Close()

	if !fx.log.HasAction(vocabulary.ModuleInfraLLM, vocabulary.ActionLLMClosed) {
		t.Errorf("Close() must emit ActionLLMClosed; captured=%+v", fx.log.Snapshot())
	}
	if fx.log.HasAction(vocabulary.ModuleInfraLLM, vocabulary.ActionLLMConnected) {
		t.Errorf("Close() must NOT emit ActionLLMConnected (audit Critical #1); captured=%+v", fx.log.Snapshot())
	}
}

// TestChat_HappyPath — Chat returns a domain ChatResponse; no openai
// types leak.
func TestChat_HappyPath(t *testing.T) {
	ctx := context.Background()
	m := newMockLLMServer()
	defer m.Close()

	fx := newTestFixture()
	c := newClientAgainst(t, ctx, m.URL(), fx, 2)
	defer c.Close()

	resp, err := c.Chat(ctx, simpleChat("hi"))
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if resp.Content != "test reply" {
		t.Errorf("Content = %q, want 'test reply'", resp.Content)
	}
	if resp.Usage.PromptTokens != 12 || resp.Usage.CompletionTokens != 5 {
		t.Errorf("Usage = %+v, want prompt=12 completion=5", resp.Usage)
	}
	if resp.Model != "test-model-vl" {
		t.Errorf("Model = %q, want test-model-vl", resp.Model)
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

// TestChat_ClassifiesErrors — the load-bearing bit: adapter maps HTTP
// status codes to typed sentinels so Phase O retry policies can classify
// via errors.Is.
func TestChat_ClassifiesErrors(t *testing.T) {
	cases := []struct {
		name         string
		serverStatus int
		wantSentinel error
	}{
		{"429 → ErrRateLimited", http.StatusTooManyRequests, llm.ErrRateLimited},
		{"503 → ErrCapExceeded (llama.cpp max_parallel)", http.StatusServiceUnavailable, llm.ErrCapExceeded},
		{"500 → ErrUnavailable", http.StatusInternalServerError, llm.ErrUnavailable},
		{"502 → ErrUnavailable", http.StatusBadGateway, llm.ErrUnavailable},
		{"504 → ErrUnavailable", http.StatusGatewayTimeout, llm.ErrUnavailable},
		{"404 → ErrModelNotFound", http.StatusNotFound, llm.ErrModelNotFound},
		{"400 → ErrInvalidRequest", http.StatusBadRequest, llm.ErrInvalidRequest},
		{"401 → ErrAuthFailed", http.StatusUnauthorized, llm.ErrAuthFailed},
		{"403 → ErrAuthFailed", http.StatusForbidden, llm.ErrAuthFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			m := newMockLLMServer()
			m.chatStatusCode = tc.serverStatus
			defer m.Close()

			fx := newTestFixture()
			c := newClientAgainst(t, ctx, m.URL(), fx, 2)
			defer c.Close()

			_, err := c.Chat(ctx, simpleChat("hi"))
			if err == nil {
				t.Fatalf("expected error for status %d, got nil", tc.serverStatus)
			}
			if !errors.Is(err, tc.wantSentinel) {
				t.Errorf("errors.Is(err, %v) = false; err = %v", tc.wantSentinel, err)
			}
		})
	}
}

func TestChat_ClassifiesInvalidJSONResponse(t *testing.T) {
	ctx := context.Background()
	m := newMockLLMServer()
	m.chatRawResponse = []byte(`{"choices":`)
	defer m.Close()

	fx := newTestFixture()
	c := newClientAgainst(t, ctx, m.URL(), fx, 2)
	defer c.Close()

	_, err := c.Chat(ctx, simpleChat("hi"))
	if !errors.Is(err, llm.ErrInvalidJSON) {
		t.Fatalf("invalid 2xx JSON error = %v, want ErrInvalidJSON", err)
	}
}

// TestChat_MultimodalRoundtrip — a ChatMessage carrying an image gets
// forwarded to the server as an OpenAI multipart message (visible via
// the response coming back at all — the mock echoes 200 regardless,
// so the meaningful assertion is "no error from translation").
func TestChat_MultimodalRoundtrip(t *testing.T) {
	ctx := context.Background()
	m := newMockLLMServer()
	defer m.Close()

	fx := newTestFixture()
	c := newClientAgainst(t, ctx, m.URL(), fx, 2)
	defer c.Close()

	req := llm.ChatRequest{
		Messages: []llm.ChatMessage{{
			Role:    llm.RoleUser,
			Content: "is this a soccer match?",
			Images:  []llm.ChatImage{{Data: []byte{0xFF, 0xD8, 0xFF, 0xE0}, MimeType: "image/jpeg"}},
		}},
	}
	if _, err := c.Chat(ctx, req); err != nil {
		t.Fatalf("multimodal chat: %v", err)
	}
}

// TestChat_StructuredOutputAndThinkingToggle — the V/4 plumb: a request
// carrying ResponseFormat + DisableThinking must reach the wire as an
// openai response_format=json_schema block and chat_template_kwargs with
// enable_thinking:false. Asserted by inspecting the captured request body.
func TestChat_StructuredOutputAndThinkingToggle(t *testing.T) {
	ctx := context.Background()
	m := newMockLLMServer()
	defer m.Close()

	fx := newTestFixture()
	c := newClientAgainst(t, ctx, m.URL(), fx, 2)
	defer c.Close()

	schema := json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`)
	req := llm.ChatRequest{
		Messages:        []llm.ChatMessage{{Role: llm.RoleUser, Content: "hi"}},
		DisableThinking: true,
		ResponseFormat: &llm.ResponseFormat{
			JSONSchema: &llm.JSONSchema{Name: "verdict", Schema: schema, Strict: true},
		},
	}
	if _, err := c.Chat(ctx, req); err != nil {
		t.Fatalf("structured chat: %v", err)
	}

	var sent map[string]any
	if err := json.Unmarshal(m.lastChatBody, &sent); err != nil {
		t.Fatalf("unmarshal captured body: %v (body=%s)", err, m.lastChatBody)
	}

	rf, ok := sent["response_format"].(map[string]any)
	if !ok {
		t.Fatalf("response_format missing/wrong type in %s", m.lastChatBody)
	}
	if rf["type"] != "json_schema" {
		t.Errorf("response_format.type = %v, want json_schema", rf["type"])
	}
	js, ok := rf["json_schema"].(map[string]any)
	if !ok || js["name"] != "verdict" || js["strict"] != true {
		t.Errorf("json_schema block wrong: %v", rf["json_schema"])
	}

	ctk, ok := sent["chat_template_kwargs"].(map[string]any)
	if !ok || ctk["enable_thinking"] != false {
		t.Errorf("chat_template_kwargs = %v, want enable_thinking:false", sent["chat_template_kwargs"])
	}
}

// TestChat_NoStructuredOutputByDefault — a plain request must NOT carry a
// response_format or chat_template_kwargs (prose is the default; we don't
// want to accidentally constrain every call).
func TestChat_NoStructuredOutputByDefault(t *testing.T) {
	ctx := context.Background()
	m := newMockLLMServer()
	defer m.Close()

	fx := newTestFixture()
	c := newClientAgainst(t, ctx, m.URL(), fx, 2)
	defer c.Close()

	if _, err := c.Chat(ctx, simpleChat("hi")); err != nil {
		t.Fatalf("chat: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal(m.lastChatBody, &sent); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := sent["response_format"]; present {
		t.Errorf("plain request leaked response_format: %s", m.lastChatBody)
	}
	if _, present := sent["chat_template_kwargs"]; present {
		t.Errorf("plain request leaked chat_template_kwargs: %s", m.lastChatBody)
	}
}

// TestChat_ImageEitherDataOrURL — sanity check on the ChatImage
// constraint. Setting both should error at request-build time.
func TestChat_ImageEitherDataOrURL(t *testing.T) {
	ctx := context.Background()
	m := newMockLLMServer()
	defer m.Close()

	fx := newTestFixture()
	c := newClientAgainst(t, ctx, m.URL(), fx, 2)
	defer c.Close()

	req := llm.ChatRequest{
		Messages: []llm.ChatMessage{{
			Role:   llm.RoleUser,
			Images: []llm.ChatImage{{Data: []byte{0xFF}, URL: "http://x/y.jpg"}},
		}},
	}
	_, err := c.Chat(ctx, req)
	if err == nil {
		t.Fatal("expected error when both Data and URL set, got nil")
	}
}

// TestChat_ConcurrencyCap — semaphore invariant preserved through the
// refactor. 6 concurrent goroutines against cap=2 mustn't breach.
func TestChat_ConcurrencyCap(t *testing.T) {
	ctx := context.Background()
	m := newMockLLMServer()
	m.chatDelay = 100 * time.Millisecond
	defer m.Close()

	fx := newTestFixture()
	c := newClientAgainst(t, ctx, m.URL(), fx, 2)
	defer c.Close()
	m.ResetPeak()

	const N = 6
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Chat(ctx, simpleChat("hi"))
		}()
	}
	wg.Wait()

	if got := m.Peak(); got > 2 {
		t.Errorf("mock server saw peak in-flight = %d, want ≤ 2 (semaphore breached)", got)
	}
}

// TestNewClient_APIVersionPathOverride — the nexus-swap knob. Empty
// version path means the client hits the endpoint's root. Verified by
// pointing the mock at a server that only serves the un-versioned
// route.
func TestNewClient_APIVersionPathOverride(t *testing.T) {
	// Server serves /models WITHOUT the /v1 prefix.
	mux := http.NewServeMux()
	mux.HandleFunc("/models", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(openai.ModelsList{
			Models: []openai.Model{{ID: "nexus-test", Object: "model", OwnedBy: "nexus"}},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	fx := newTestFixture()
	c, err := llm.NewClient(context.Background(), config.LLMConfig{
		Endpoint:           srv.URL,
		APIVersionPath:     "", // no version segment
		APIKey:             "test",
		ChatConcurrencyCap: 2,
		ConnectTimeout:     5 * time.Second,
		RequestTimeout:     10 * time.Second,
	}, fx.ins)
	if err != nil {
		t.Fatalf("NewClient with empty APIVersionPath: %v", err)
	}
	defer c.Close()

	if c.ChatModel() != "nexus-test" {
		t.Errorf("ChatModel = %q, want nexus-test (proved probe hit /models not /v1/models)", c.ChatModel())
	}
}

// TestNewClient_NilInstruments_Errors — fast-fail guard.
func TestNewClient_NilInstruments_Errors(t *testing.T) {
	_, err := llm.NewClient(context.Background(),
		config.LLMConfig{Endpoint: "http://x", ChatConcurrencyCap: 2}, nil)
	if err == nil {
		t.Fatal("expected error for nil Instruments, got nil")
	}
}

// TestNewClient_EmptyEndpoint_Errors — fast-fail guard.
func TestNewClient_EmptyEndpoint_Errors(t *testing.T) {
	fx := newTestFixture()
	_, err := llm.NewClient(context.Background(),
		config.LLMConfig{ChatConcurrencyCap: 2}, fx.ins)
	if err == nil {
		t.Fatal("expected error for empty LLM_ENDPOINT_URL, got nil")
	}
}

// TestNewClient_ZeroConcurrencyCap_Errors — 0 cap would deadlock.
func TestNewClient_ZeroConcurrencyCap_Errors(t *testing.T) {
	fx := newTestFixture()
	_, err := llm.NewClient(context.Background(),
		config.LLMConfig{Endpoint: "http://x", ChatConcurrencyCap: 0}, fx.ins)
	if err == nil {
		t.Fatal("expected error for ChatConcurrencyCap=0, got nil")
	}
}

// TestNewClient_UnreachableHost_ErrorsQuickly — startup bounded by
// ConnectTimeout.
func TestNewClient_UnreachableHost_ErrorsQuickly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration-ish test skipped in -short mode")
	}

	fx := newTestFixture()
	start := time.Now()
	_, err := llm.NewClient(context.Background(), config.LLMConfig{
		Endpoint:           "http://192.0.2.1:8080",
		APIVersionPath:     "/v1",
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
