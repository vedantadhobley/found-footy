// Package llm is the OpenAI-compatible LLM adapter. Backs onto joi's
// llama.cpp server today (Qwen3-VL-8B for vision + RAG); designed to
// swap transparently to nexus per decisions.md 2026-07-01.
//
// Phase S6.1: chat completion path with metric + log instrumentation
// and a concurrency-cap semaphore matching joi's max_parallel=2.
// Embedding path is stubbed; the domain layer wires it in when needed.
package llm

import (
	"context"
	"fmt"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Client wraps *openai.Client (pointer embed — every method of the
// upstream client remains directly callable) with metric + log
// instrumentation on Chat completions and a semaphore that bounds
// concurrent in-flight chat calls to cfg.ChatConcurrencyCap.
//
// Embedding path is deferred; the domain layer will land Embed()
// when the vision-embedding classifier work comes online.
type Client struct {
	*openai.Client
	ins       *Instruments
	chatModel string
	sem       chan struct{}
	timeout   time.Duration
}

// NewClient builds an OpenAI-compatible client pointed at cfg.Endpoint,
// verifies reachability via /v1/models, and if cfg.ChatModel is empty
// picks the first advertised model as the default.
//
// The caller registers cleanup with bootstrap.Deps.RegisterCloser
// ("llm", ...). Underlying HTTP client has no persistent connection to
// drain; the closer is a no-op emit for symmetry.
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

	oaiCfg := openai.DefaultConfig(cfg.APIKey)
	oaiCfg.BaseURL = cfg.Endpoint + "/v1"
	oaiClient := openai.NewClientWithConfig(oaiCfg)

	// Probe /v1/models. Bounded by cfg.ConnectTimeout so a dead endpoint
	// fails startup fast. Also lets us auto-populate ChatModel when it's
	// left blank in the env.
	probeCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	models, err := oaiClient.ListModels(probeCtx)
	if err != nil {
		ins.emitEvent(ctx, logging.LevelError, vocabulary.ActionLLMConnectFailed,
			"llm /v1/models probe failed",
			logging.String("endpoint", cfg.Endpoint),
			logging.Err(err),
		)
		return nil, fmt.Errorf("llm.NewClient: /v1/models probe: %w", err)
	}

	chatModel := cfg.ChatModel
	if chatModel == "" {
		if len(models.Models) == 0 {
			ins.emitEvent(ctx, logging.LevelError, vocabulary.ActionLLMConnectFailed,
				"llm /v1/models returned empty list; cannot auto-select chat model",
				logging.String("endpoint", cfg.Endpoint),
			)
			return nil, fmt.Errorf("llm.NewClient: /v1/models returned zero models and LLM_CHAT_MODEL is empty")
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
		logging.String("endpoint", cfg.Endpoint),
		logging.String("chat_model", chatModel),
		logging.Int("concurrency_cap", cfg.ChatConcurrencyCap),
	)

	return &Client{
		Client:    oaiClient,
		ins:       ins,
		chatModel: chatModel,
		sem:       make(chan struct{}, cfg.ChatConcurrencyCap),
		timeout:   cfg.RequestTimeout,
	}, nil
}

// ChatModel returns the chat model this client is bound to (explicit or
// auto-discovered). Callers use this when composing OpenAI request
// bodies without re-reading config.
func (c *Client) ChatModel() string { return c.chatModel }

// Chat wraps openai.CreateChatCompletion with metric + log
// instrumentation and the concurrency-cap semaphore. Callers construct
// the openai.ChatCompletionRequest normally; the wrapper injects the
// model + enforces the timeout.
//
// If the request already carries a Model, it's respected — otherwise
// c.ChatModel() is used. This gives Phase O flexibility to pin
// specific requests to specific models (e.g. an OCR-only path might
// want a smaller model than the vision path).
func (c *Client) Chat(ctx context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if req.Model == "" {
		req.Model = c.chatModel
	}

	// Acquire semaphore slot. Bounded by ctx — if the caller's ctx
	// cancels while we're waiting, bail out instead of holding the
	// call open forever.
	c.ins.concurrentCalls.Inc()
	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		c.ins.concurrentCalls.Dec()
		c.ins.calls.WithLabelValues("chat", "failure").Inc()
		return openai.ChatCompletionResponse{}, ctx.Err()
	}
	defer func() {
		<-c.sem
		c.ins.concurrentCalls.Dec()
	}()

	// Bound the actual request by RequestTimeout on top of whatever the
	// caller passed in.
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	start := time.Now()
	resp, err := c.Client.CreateChatCompletion(reqCtx, req)
	elapsed := time.Since(start)
	c.ins.callDuration.WithLabelValues("chat").Observe(elapsed.Seconds())

	if err != nil {
		c.ins.calls.WithLabelValues("chat", "failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionLLMChatFailed,
			"llm chat call failed",
			logging.String("model", req.Model),
			logging.Int64("elapsed_ms", elapsed.Milliseconds()),
			logging.Err(err),
		)
		return openai.ChatCompletionResponse{}, err
	}
	c.ins.calls.WithLabelValues("chat", "success").Inc()
	c.ins.tokens.WithLabelValues("chat", "prompt").Add(float64(resp.Usage.PromptTokens))
	c.ins.tokens.WithLabelValues("chat", "completion").Add(float64(resp.Usage.CompletionTokens))
	c.ins.emitEvent(ctx, logging.LevelDebug, vocabulary.ActionLLMChatCall,
		"llm chat call ok",
		logging.String("model", req.Model),
		logging.Int64("elapsed_ms", elapsed.Milliseconds()),
		logging.Int("prompt_tokens", resp.Usage.PromptTokens),
		logging.Int("completion_tokens", resp.Usage.CompletionTokens),
	)
	return resp, nil
}

// Close emits a lifecycle log line. The underlying HTTP client has no
// persistent state to release; this exists for symmetry with the other
// adapters + gives the bootstrap Closer registry something to invoke.
func (c *Client) Close() {
	c.ins.connectionState.Set(0)
	c.ins.emitEvent(context.Background(), logging.LevelInfo,
		vocabulary.ActionLLMConnected, // no dedicated "closed" action; the state gauge tells the story
		"llm client closed",
	)
}
