// errors.go — typed sentinel errors for the LLM adapter. Callers gate
// retry policy on errors.Is(err, ErrRateLimited) etc.; adapter maps
// upstream error shapes (HTTP status codes, error body classes) onto
// these sentinels via classifyError.
package llm

import (
	"errors"
	"net/http"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

// Typed sentinel errors. Grouping them in one file keeps the retry
// classification decisions visible in one place.
var (
	// ErrCapExceeded — the endpoint is at its concurrency cap.
	// llama.cpp on joi returns 503 when max_parallel is exceeded (per
	// AGENTS.md joi-side notes). Retry with backoff.
	ErrCapExceeded = errors.New("llm: concurrency cap exceeded")

	// ErrRateLimited — the endpoint imposed a per-account/per-key
	// rate limit. HTTP 429. Retry with (longer) backoff, respect any
	// Retry-After header when present.
	ErrRateLimited = errors.New("llm: rate limited")

	// ErrUnavailable — the endpoint is transiently unreachable / down.
	// 500, 502, 504, network transport errors. Retry.
	ErrUnavailable = errors.New("llm: server unavailable")

	// ErrInvalidJSON — response was 2xx but the body wasn't parseable.
	// Do NOT retry (deterministic failure).
	ErrInvalidJSON = errors.New("llm: invalid JSON in response")

	// ErrModelNotFound — the endpoint doesn't have the requested model
	// loaded. HTTP 404 on chat completions. Do NOT retry.
	ErrModelNotFound = errors.New("llm: model not found")

	// ErrInvalidRequest — the request itself is malformed (bad params,
	// unsupported content-type). HTTP 400. Do NOT retry.
	ErrInvalidRequest = errors.New("llm: invalid request")

	// ErrAuthFailed — the API key was rejected. HTTP 401/403. Do NOT
	// retry; the config is wrong.
	ErrAuthFailed = errors.New("llm: authentication failed")
)

// classifyError maps an upstream error to one of the typed sentinels.
// Returns the original error unwrapped when no sentinel matches — so
// callers see the raw message for debugging but errors.Is still works
// on the classified cases.
//
// Currently handles openai.APIError (from sashabaranov/go-openai) and
// generic transport errors. When nexus lands with a potentially
// different error shape, extend this function; the sentinel surface
// stays stable.
func classifyError(err error) error {
	if err == nil {
		return nil
	}

	// sashabaranov/go-openai errors carry the HTTP status code.
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.HTTPStatusCode {
		case http.StatusTooManyRequests: // 429
			return joinErr(err, ErrRateLimited)
		case http.StatusServiceUnavailable: // 503 — llama.cpp cap-exceeded case
			return joinErr(err, ErrCapExceeded)
		case http.StatusInternalServerError, http.StatusBadGateway, http.StatusGatewayTimeout: // 500/502/504
			return joinErr(err, ErrUnavailable)
		case http.StatusNotFound: // 404 (chat-completion returns this for missing model)
			return joinErr(err, ErrModelNotFound)
		case http.StatusBadRequest: // 400
			return joinErr(err, ErrInvalidRequest)
		case http.StatusUnauthorized, http.StatusForbidden: // 401/403
			return joinErr(err, ErrAuthFailed)
		}
	}

	// Some upstream errors don't hit APIError (transport/DNS) — sniff
	// common cases from the message.
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "no such host"),
		strings.Contains(msg, "timeout"):
		return joinErr(err, ErrUnavailable)
	}
	return err
}

// joinErr wraps `sentinel` with the original error's message so both
// remain accessible: errors.Is(returned, sentinel) is true; the
// returned err.Error() carries the upstream detail. Uses fmt.Errorf's
// %w chaining (via errors.Join in the multi-target form).
func joinErr(original, sentinel error) error {
	return errors.Join(sentinel, original)
}
