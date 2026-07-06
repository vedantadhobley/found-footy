// Package llm is the config-swappable LLM endpoint client (joi today,
// nexus later — per decisions.md 2026-07-01). Per-worker-process
// sync.Semaphore around chat completion calls to respect joi's
// parallel cap. See §9 llm adapter.
package llm
