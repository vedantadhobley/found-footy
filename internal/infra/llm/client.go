// Package llm is the OpenAI-compatible LLM adapter. Backs onto joi's
// llama.cpp server today (Qwen3-VL-8B for vision + RAG); designed to
// swap transparently to nexus per decisions.md 2026-07-01.
//
// The public surface (Chat, ChatRequest, ChatResponse, typed errors)
// does NOT expose sashabaranov/go-openai types. Callers depend only on
// this package's exports; the adapter translates internally on both
// the request and response paths, so a future SDK swap or endpoint
// shape change is a single-file update here rather than a rewrite of
// every activity that calls Chat.
package llm

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Client is the LLM adapter's public entry point. The underlying
// sashabaranov openai.Client is held internally, NOT embedded — callers
// can't accidentally reach for openai types through it.
type Client struct {
	oai       *openai.Client
	ins       *Instruments
	chatModel string
	sem       chan struct{}
	timeout   time.Duration
}

// NewClient builds the LLM adapter pointed at cfg.Endpoint. Composes
// the full base URL as cfg.Endpoint + cfg.APIVersionPath (default "/v1"
// covers llama.cpp + OpenAI; nexus can override to "/inference/v1" or
// similar via env).
//
// Probes the models endpoint bounded by cfg.ConnectTimeout. Auto-selects
// the first model when cfg.ChatModel is empty. Fails fast on
// unreachable endpoint, invalid auth, or empty model catalog.
//
// The caller registers cleanup with bootstrap.Deps.RegisterCloser
// ("llm", ...). The underlying HTTP client has no persistent state to
// drain; Close is a lifecycle-log-only operation.
func NewClient(ctx context.Context, cfg config.LLMConfig, ins *Instruments) (*Client, error) {
	if ins == nil {
		return nil, fmt.Errorf("llm.NewClient: Instruments is required (call RegisterMetrics first)")
	}
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("llm.NewClient: LLM_ENDPOINT_URL not set")
	}
	if cfg.ChatConcurrencyCap <= 0 {
		return nil, fmt.Errorf("llm.NewClient: ChatConcurrencyCap must be > 0 (got %d)", cfg.ChatConcurrencyCap)
	}

	// Compose the base URL: <endpoint><version-path>. Trim trailing "/"
	// off endpoint + leading "/" off path so the join is idempotent
	// regardless of how the operator writes them.
	endpoint := strings.TrimRight(cfg.Endpoint, "/")
	versionPath := "/" + strings.Trim(cfg.APIVersionPath, "/")
	if versionPath == "/" {
		// Empty path is legitimate for endpoints that don't segment by
		// version — still needs to start with "/" to be a valid URL.
		versionPath = ""
	}

	oaiCfg := openai.DefaultConfig(cfg.APIKey)
	oaiCfg.BaseURL = endpoint + versionPath
	oai := openai.NewClientWithConfig(oaiCfg)

	// Probe /models. Bounded by cfg.ConnectTimeout so a dead endpoint
	// fails startup fast. Also auto-populates ChatModel when empty.
	probeCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	models, err := oai.ListModels(probeCtx)
	if err != nil {
		classified := classifyError(err)
		ins.emitEvent(ctx, logging.LevelError, vocabulary.ActionLLMConnectFailed,
			"llm models probe failed",
			logging.String("endpoint", oaiCfg.BaseURL),
			logging.Err(classified),
		)
		return nil, fmt.Errorf("llm.NewClient: models probe: %w", classified)
	}

	chatModel := cfg.ChatModel
	if chatModel == "" {
		if len(models.Models) == 0 {
			ins.emitEvent(ctx, logging.LevelError, vocabulary.ActionLLMConnectFailed,
				"llm models list empty; cannot auto-select chat model",
				logging.String("endpoint", oaiCfg.BaseURL),
			)
			return nil, fmt.Errorf("llm.NewClient: models list empty and LLM_CHAT_MODEL is unset")
		}
		chatModel = models.Models[0].ID
		ins.emitEvent(ctx, logging.LevelInfo, vocabulary.ActionLLMModelDiscovered,
			"llm chat model auto-discovered",
			logging.String("model", chatModel),
			logging.Int("available_models", len(models.Models)),
		)
	}
	ins.connectionState.Set(1)

	ins.emitEvent(ctx, logging.LevelInfo, vocabulary.ActionLLMConnected,
		"llm client ready",
		logging.String("endpoint", oaiCfg.BaseURL),
		logging.String("chat_model", chatModel),
		logging.Int("concurrency_cap", cfg.ChatConcurrencyCap),
	)

	return &Client{
		oai:       oai,
		ins:       ins,
		chatModel: chatModel,
		sem:       make(chan struct{}, cfg.ChatConcurrencyCap),
		timeout:   cfg.RequestTimeout,
	}, nil
}

// ChatModel returns the model this client is bound to (explicit config
// or auto-discovered at startup). Callers use this when composing
// ChatRequest instances without re-reading config.
func (c *Client) ChatModel() string { return c.chatModel }

// Chat sends a chat completion request under the concurrency-cap
// semaphore + adapter-imposed timeout, and returns the response
// translated into domain types.
//
// The semaphore matches joi's llama.cpp max_parallel=2 — going past
// that collapses throughput per AGENTS.md. Semaphore acquisition is
// ctx-bounded (caller cancellation releases the wait).
//
// Errors are classified via classifyError before returning: callers
// use errors.Is(err, ErrRateLimited) / ErrCapExceeded / ErrUnavailable
// etc. to gate retry policy. Raw upstream error messages remain
// accessible in err.Error() for logs.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if req.Model == "" {
		req.Model = c.chatModel
	}
	oaiReq, err := toOpenAIRequest(req)
	if err != nil {
		return nil, err
	}

	// Acquire semaphore slot. Bounded by ctx so caller cancellation
	// releases the wait instead of holding it forever.
	c.ins.concurrentCalls.Inc()
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		c.ins.concurrentCalls.Dec()
		c.ins.calls.WithLabelValues("chat", "failure").Inc()
		return nil, ctx.Err()
	}
	defer func() {
		<-c.sem
		c.ins.concurrentCalls.Dec()
	}()

	// Bound the actual request by RequestTimeout on top of whatever
	// context the caller passed in.
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	start := time.Now()
	oaiResp, err := c.oai.CreateChatCompletion(reqCtx, oaiReq)
	elapsed := time.Since(start)
	c.ins.callDuration.WithLabelValues("chat").Observe(elapsed.Seconds())

	if err != nil {
		classified := classifyError(err)
		c.ins.calls.WithLabelValues("chat", "failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionLLMChatFailed,
			"llm chat call failed",
			logging.String("model", req.Model),
			logging.Int64("elapsed_ms", elapsed.Milliseconds()),
			logging.Err(classified),
		)
		return nil, classified
	}

	resp := fromOpenAIResponse(oaiResp)
	c.ins.calls.WithLabelValues("chat", "success").Inc()
	c.ins.tokens.WithLabelValues("chat", "prompt").Add(float64(resp.Usage.PromptTokens))
	c.ins.tokens.WithLabelValues("chat", "completion").Add(float64(resp.Usage.CompletionTokens))
	c.ins.emitEvent(ctx, logging.LevelDebug, vocabulary.ActionLLMChatCall,
		"llm chat call ok",
		logging.String("model", resp.Model),
		logging.Int64("elapsed_ms", elapsed.Milliseconds()),
		logging.Int("prompt_tokens", resp.Usage.PromptTokens),
		logging.Int("completion_tokens", resp.Usage.CompletionTokens),
	)
	return &resp, nil
}

// Close emits a lifecycle log line. The underlying HTTP client has no
// persistent state; the closer registration exists for symmetry with
// the pg/nats/temporal adapters + gives operators a visible shutdown
// marker in Loki.
func (c *Client) Close() {
	c.ins.connectionState.Set(0)
	c.ins.emitEvent(context.Background(), logging.LevelInfo,
		vocabulary.ActionLLMClosed,
		"llm client closed",
	)
}

// toOpenAIRequest translates a domain ChatRequest into a sashabaranov
// ChatCompletionRequest. This is the ONE place openai's request shape
// touches the adapter; when nexus lands with a different shape (or
// when we swap sashabaranov out), only this + fromOpenAIResponse
// change.
func toOpenAIRequest(req ChatRequest) (openai.ChatCompletionRequest, error) {
	out := openai.ChatCompletionRequest{
		Model: req.Model,
		Stop:  req.Stop,
	}
	if req.Temperature != nil {
		out.Temperature = float32(*req.Temperature)
	}
	if req.MaxTokens != nil {
		out.MaxTokens = *req.MaxTokens
	}
	out.Messages = make([]openai.ChatCompletionMessage, 0, len(req.Messages))
	for i, m := range req.Messages {
		msg, err := toOpenAIMessage(m)
		if err != nil {
			return openai.ChatCompletionRequest{}, fmt.Errorf("llm.toOpenAIRequest: message %d: %w", i, err)
		}
		out.Messages = append(out.Messages, msg)
	}
	return out, nil
}

// toOpenAIMessage handles the text-vs-multimodal split. Text-only
// messages populate Content; messages with attached Images use
// MultiContent parts (openai's vision-model input format).
func toOpenAIMessage(m ChatMessage) (openai.ChatCompletionMessage, error) {
	role := string(m.Role)
	if len(m.Images) == 0 {
		return openai.ChatCompletionMessage{Role: role, Content: m.Content}, nil
	}
	parts := make([]openai.ChatMessagePart, 0, 1+len(m.Images))
	if m.Content != "" {
		parts = append(parts, openai.ChatMessagePart{
			Type: openai.ChatMessagePartTypeText,
			Text: m.Content,
		})
	}
	for i, img := range m.Images {
		part, err := toOpenAIImagePart(img)
		if err != nil {
			return openai.ChatCompletionMessage{}, fmt.Errorf("image %d: %w", i, err)
		}
		parts = append(parts, part)
	}
	return openai.ChatCompletionMessage{Role: role, MultiContent: parts}, nil
}

// toOpenAIImagePart converts a domain ChatImage to openai's ImageURL
// part. Data-mode encodes as a base64 data URL; URL-mode passes
// through. Exactly-one-of-Data-or-URL is enforced here.
func toOpenAIImagePart(img ChatImage) (openai.ChatMessagePart, error) {
	hasData := len(img.Data) > 0
	hasURL := img.URL != ""
	if hasData == hasURL {
		return openai.ChatMessagePart{}, fmt.Errorf("ChatImage: exactly one of Data or URL required")
	}
	url := img.URL
	if hasData {
		mime := img.MimeType
		if mime == "" {
			mime = "image/jpeg"
		}
		url = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
	}
	return openai.ChatMessagePart{
		Type: openai.ChatMessagePartTypeImageURL,
		ImageURL: &openai.ChatMessageImageURL{
			URL: url,
		},
	}, nil
}

// fromOpenAIResponse translates the SDK's ChatCompletionResponse into
// the domain ChatResponse. If the upstream returned zero choices (bad
// server, or a streaming response body mis-terminated), Content is left
// empty — callers should treat that as ErrInvalidRequest or similar
// when downstream fails to parse.
func fromOpenAIResponse(resp openai.ChatCompletionResponse) ChatResponse {
	out := ChatResponse{
		Model: resp.Model,
		Usage: Usage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
	}
	if len(resp.Choices) > 0 {
		out.Content = resp.Choices[0].Message.Content
	}
	return out
}
