// Package llm is the OpenAI-compatible client for Control-managed model
// gateways. A per-worker-process semaphore bounds local chat fan-out while
// Control independently enforces shared admission. See §9 llm adapter.
package llm
