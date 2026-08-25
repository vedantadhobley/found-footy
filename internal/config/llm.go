// LLMConfig defines the Found Footy side of Control's OpenAI-compatible model
// gateway contract.
package config

import "time"

// LLMConfig covers the OpenAI-compatible LLM endpoint the worker binary
// hits for vision validation. The application selects a Control gateway and
// public model ID; Control owns backend placement and runtime translation.
//
// LoadFor validates Endpoint before the worker opens external connections.
// Constructor checks remain as defense in depth for direct use.
type LLMConfig struct {
	// Endpoint is the OpenAI-compatible base URL. Production uses
	// http://control-joi.luv. The full model + chat URLs are composed as
	// Endpoint + APIVersionPath + "/models" or "/chat/completions".
	Endpoint string `env:"LLM_ENDPOINT_URL"`

	// APIVersionPath is the URL segment between Endpoint and the OpenAI
	// route names. Control and OpenAI-compatible gateways use "/v1" by
	// default; an explicit empty path remains supported.
	APIVersionPath string `env:"LLM_API_VERSION_PATH" envDefault:"/v1"`

	// APIKey is passed as Authorization: Bearer <key>. The internal Control
	// gateway currently accepts "not-required"; populate a real credential
	// when the selected gateway requires one.
	APIKey string `env:"LLM_API_KEY" envDefault:"not-required"`

	// ChatModel is the model ID the chat completion path targets. Empty
	// = discover from /v1/models at startup (picks the first entry).
	// Explicit value pins us to a specific model even when the server
	// hosts multiple.
	ChatModel string `env:"LLM_CHAT_MODEL" envDefault:""`

	// ChatConcurrencyCap bounds the number of in-flight chat calls this
	// process may have open at once. It bounds this application's local burst
	// and retry surface; Control independently owns shared model admission.
	// Production divides the accepted four-call Gemma budget across its two
	// worker replicas.
	ChatConcurrencyCap int `env:"LLM_CHAT_CONCURRENCY_CAP" envDefault:"4"`

	// ConnectTimeout bounds the initial /v1/models probe done by the
	// constructor.
	ConnectTimeout time.Duration `env:"LLM_CONNECT_TIMEOUT" envDefault:"10s"`

	// RequestTimeout bounds an individual chat completion request.
	// Vision calls can be slow (multi-second inference on video frames);
	// 60s gives headroom without being wide-open.
	RequestTimeout time.Duration `env:"LLM_REQUEST_TIMEOUT" envDefault:"60s"`
}
