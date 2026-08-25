// LLMConfig — env-driven settings for the OpenAI-compatible LLM adapter
// (llama.cpp on joi today; abstracted for future nexus swap per
// decisions.md 2026-07-01).
package config

import "time"

// LLMConfig covers the OpenAI-compatible LLM endpoint the worker binary
// hits for vision validation. Per decisions.md
// 2026-07-01, the endpoint is intentionally abstracted — the swap to
// nexus (when it lands) is a config-only change.
//
// LoadFor validates Endpoint before the worker opens external connections.
// Constructor checks remain as defense in depth for direct use.
type LLMConfig struct {
	// Endpoint is the OpenAI-compatible base URL. Example:
	//   http://llama-small.joi
	// Caddy split-DNS resolves this to joi's llama.cpp server; no port
	// needed. The full model + chat URLs are composed as
	// Endpoint + APIVersionPath + "/models" or "/chat/completions".
	Endpoint string `env:"LLM_ENDPOINT_URL"`

	// APIVersionPath is the URL segment between Endpoint and the OpenAI
	// route names. llama.cpp + OpenAI both use "/v1"; nexus may use
	// "/v1", "/inference/v1", or something else. Empty defaults to
	// "/v1" for backwards compatibility with the current joi setup.
	APIVersionPath string `env:"LLM_API_VERSION_PATH" envDefault:"/v1"`

	// APIKey is passed as Authorization: Bearer <key>. llama.cpp accepts
	// any non-empty value; leave "not-required" for the current joi
	// setup, populate with a real key when nexus (or an external
	// gateway) needs it.
	APIKey string `env:"LLM_API_KEY" envDefault:"not-required"`

	// ChatModel is the model ID the chat completion path targets. Empty
	// = discover from /v1/models at startup (picks the first entry).
	// Explicit value pins us to a specific model even when the server
	// hosts multiple.
	ChatModel string `env:"LLM_CHAT_MODEL" envDefault:""`

	// ChatConcurrencyCap bounds the number of in-flight chat calls this
	// process may have open at once, via a client-side semaphore that
	// tracks the configured Joi gateway's per-node capacity — gemma runs
	// --parallel 4 (gateway max_slots=4), so 4 is the default; wider just
	// queues on the gateway. STOPGAP pending control-plane request
	// queueing, after which apps stop managing this — see decisions.md
	// 2026-08-11.
	ChatConcurrencyCap int `env:"LLM_CHAT_CONCURRENCY_CAP" envDefault:"4"`

	// ConnectTimeout bounds the initial /v1/models probe done by the
	// constructor.
	ConnectTimeout time.Duration `env:"LLM_CONNECT_TIMEOUT" envDefault:"10s"`

	// RequestTimeout bounds an individual chat completion request.
	// Vision calls can be slow (multi-second inference on video frames);
	// 60s gives headroom without being wide-open.
	RequestTimeout time.Duration `env:"LLM_REQUEST_TIMEOUT" envDefault:"60s"`
}
