// LLMConfig — env-driven settings for the OpenAI-compatible LLM adapter
// (llama.cpp on joi today; abstracted for future nexus swap per
// decisions.md 2026-07-01).
package config

import "time"

// LLMConfig covers the OpenAI-compatible LLM endpoint the worker binary
// hits for vision validation + RAG alias selection. Per decisions.md
// 2026-07-01, the endpoint is intentionally abstracted — the swap to
// nexus (when it lands) is a config-only change.
//
// Endpoint is not tagged required at the env layer because Phase S1-S5
// binaries don't need LLM yet; the llm package's constructor returns a
// descriptive error when a binary that DOES need it starts without the
// env var set.
type LLMConfig struct {
	// Endpoint is the OpenAI-compatible base URL. Example:
	//   http://llama-small.joi
	// Caddy split-DNS resolves this to joi's llama.cpp server; no port
	// needed. The endpoint MUST expose /v1/models + /v1/chat/completions.
	Endpoint string `env:"LLM_ENDPOINT_URL"`

	// APIKey is passed as Authorization: Bearer <key>. llama.cpp accepts
	// any non-empty value; leave "not-required" for the current joi
	// setup, populate with a real key when nexus (or an external
	// gateway) needs it.
	APIKey string `env:"LLM_API_KEY" envDefault:"not-required"`

	// ChatModel is the model ID the chat completion path targets. Empty
	// = discover from /v1/models at startup (picks the first entry).
	// Explicit value pins us to a specific model even when the server
	// hosts multiple.
	ChatModel string `env:"LLM_CHAT_MODEL"`

	// EmbeddingModel is the model ID for the embedding path. Deferred
	// until the domain layer needs embeddings; unused in Phase S6.1.
	EmbeddingModel string `env:"LLM_EMBEDDING_MODEL"`

	// ChatConcurrencyCap bounds the number of in-flight chat calls this
	// process may have open at once. Load-bearing: joi's llama.cpp
	// server enforces max_parallel=2 and throughput collapses past that.
	// Client-side semaphore keeps us within the cap.
	ChatConcurrencyCap int `env:"LLM_CHAT_CONCURRENCY_CAP" envDefault:"2"`

	// ConnectTimeout bounds the initial /v1/models probe done by the
	// constructor.
	ConnectTimeout time.Duration `env:"LLM_CONNECT_TIMEOUT" envDefault:"10s"`

	// RequestTimeout bounds an individual chat completion request.
	// Vision calls can be slow (multi-second inference on video frames);
	// 60s gives headroom without being wide-open.
	RequestTimeout time.Duration `env:"LLM_REQUEST_TIMEOUT" envDefault:"60s"`
}
