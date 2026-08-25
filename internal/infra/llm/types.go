// types.go — domain-owned request/response types for the LLM adapter.
// Callers depend on these, NOT on sashabaranov/go-openai types. The
// adapter translates internally, so a nexus swap (different upstream
// SDK, different request shape) is a one-file change here rather than
// a per-caller migration.
package llm

import "encoding/json"

// Role is the speaker of a chat message. Matches OpenAI's role vocab
// today; nexus may add a "tool" role — bounded string keeps that
// forward-compatible.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool" // reserved for future tool-use flows
)

// ChatMessage is one turn in a chat conversation. Content is the text
// body; Images is optional attached images for vision-model calls
// (Qwen3-VL-8B, GPT-4o, etc.). A message with both Content and Images
// is a legitimate multimodal turn — the image gets sent alongside the
// text, and the model reasons over both.
type ChatMessage struct {
	Role    Role
	Content string
	Images  []ChatImage
}

// ChatImage attaches an image to a chat message. Exactly one of Data
// or URL should be set:
//
//	Data — raw bytes; adapter base64-encodes for the OpenAI wire format.
//	URL  — the model fetches directly (only supported when the endpoint
//	       has outbound network reach; llama.cpp on joi does not).
//
// MimeType matters when Data is set — image/jpeg vs image/png changes
// how the model decodes. Optional; adapter defaults to image/jpeg if
// empty.
type ChatImage struct {
	Data     []byte
	URL      string
	MimeType string
}

// ChatRequest is the domain-owned Chat call input. Nothing here depends
// on sashabaranov/go-openai — the adapter translates when it dispatches
// the actual HTTP call.
//
// Model is optional: when empty, the adapter uses the model configured
// via LLM_CHAT_MODEL (or the auto-discovered default). Explicit Model
// pins a specific request to a specific model even when the endpoint
// hosts multiple.
type ChatRequest struct {
	Messages  []ChatMessage
	Model     string
	MaxTokens *int     // nil = model default
	Stop      []string // optional stop sequences

	// ResponseFormat, when non-nil with a JSONSchema, opts this request
	// into structured output: the server constrains token sampling so the
	// reply is guaranteed-valid JSON matching the schema (no ``` fences,
	// no preamble). nil = free-form prose (the default). First caller:
	// the vision activity (V/4).
	ResponseFormat *ResponseFormat

	// ReasoningEffort is the OpenAI-compatible public reasoning control.
	// Omit it to use the selected model's Control-owned default. Supported
	// values are model-specific and published by the gateway's /v1/models
	// response.
	ReasoningEffort ReasoningEffort
}

// ReasoningEffort is a normalized public request value. The gateway owns the
// model-specific supported set and translates it to the pinned runtime.
type ReasoningEffort string

const (
	ReasoningEffortNone   ReasoningEffort = "none"
	ReasoningEffortLow    ReasoningEffort = "low"
	ReasoningEffortMedium ReasoningEffort = "medium"
	ReasoningEffortHigh   ReasoningEffort = "high"
)

// ResponseFormat opts a Chat call into structured output. Only JSONSchema
// is modeled today (the sole use case); a nil ResponseFormat means prose.
type ResponseFormat struct {
	JSONSchema *JSONSchema
}

// JSONSchema is the schema a structured-output call constrains against.
// Schema is the raw JSON Schema document; Strict requests exact adherence
// (all properties required, no extras) where the backend honors it. The
// adapter passes Schema straight through — json.RawMessage satisfies the
// SDK's json.Marshaler contract, so no re-encoding round-trip.
type JSONSchema struct {
	Name   string
	Schema json.RawMessage
	Strict bool
}

// ChatResponse is what Chat returns. Content is the assistant reply's
// text; Usage carries prompt/completion token counts (populated from
// the upstream response's usage block when present). Model records
// which model the endpoint actually served the request with — may
// differ from the request's Model if the server aliased or fell back.
type ChatResponse struct {
	Content string
	Usage   Usage
	Model   string
}

// Usage captures token accounting.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}
