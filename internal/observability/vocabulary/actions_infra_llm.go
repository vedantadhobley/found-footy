// LLM-adapter Action enum values + init-time registration.
package vocabulary

// LLM-adapter actions. Cover the connection lifecycle + per-call
// outcomes for chat + embedding operations. Call-level actions emit at
// DEBUG on success; WARN on failure. Concurrency-cap contention is not
// an emission (visible via the found_footy_llm_concurrent_calls gauge).
const (
	ActionLLMConnected       Action = "llm_connected"        // /v1/models probe succeeded
	ActionLLMConnectFailed   Action = "llm_connect_failed"   // client build or /v1/models probe failed
	ActionLLMClosed          Action = "llm_closed"           // Close() invoked — distinct from _connected so dashboards can distinguish
	ActionLLMChatCall        Action = "llm_chat_call"        // chat completion succeeded
	ActionLLMChatFailed      Action = "llm_chat_failed"      // chat completion returned an error
	ActionLLMEmbedCall       Action = "llm_embed_call"       // embedding call succeeded
	ActionLLMEmbedFailed     Action = "llm_embed_failed"     // embedding call returned an error
	ActionLLMModelDiscovered Action = "llm_model_discovered" // startup discovered a chat model via /v1/models
)

func init() {
	registerActions(
		ActionLLMConnected,
		ActionLLMConnectFailed,
		ActionLLMClosed,
		ActionLLMChatCall,
		ActionLLMChatFailed,
		ActionLLMEmbedCall,
		ActionLLMEmbedFailed,
		ActionLLMModelDiscovered,
	)
}
