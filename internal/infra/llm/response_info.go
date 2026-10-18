// Response metadata preserves Control's public diagnostics without retaining bodies or headers.
package llm

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

// ResponseInfo contains only bounded public gateway metadata. Empty fields mean
// unknown, not zero queue time. StatusCode is available when the SDK reports an
// HTTP error; successful responses carry their request ID, not an inferred status.
type ResponseInfo struct {
	RequestID  string        `json:"request_id,omitempty"`
	StatusCode int           `json:"status_code,omitempty"`
	ErrorCode  string        `json:"error_code,omitempty"`
	RetryAfter time.Duration `json:"retry_after_ns,omitempty"`
}

// Valid rejects raw messages accidentally substituted for public identifiers.
func (i ResponseInfo) Valid() bool {
	return publicToken(i.RequestID, 128) == i.RequestID &&
		publicToken(i.ErrorCode, 64) == i.ErrorCode &&
		(i.StatusCode == 0 || i.StatusCode >= 100 && i.StatusCode <= 599) && i.RetryAfter >= 0
}

// ResponseError retains gateway evidence while errors.Is/As still reach the
// typed classification and original SDK/transport error through Err.
type ResponseError struct {
	Info ResponseInfo
	Err  error
}

// Error preserves the classified cause's diagnostic text, not arbitrary headers.
func (e *ResponseError) Error() string { return e.Err.Error() }

// Unwrap preserves retry classification independently of response metadata.
func (e *ResponseError) Unwrap() error { return e.Err }

// responseInfo reads the pinned SDK's response headers even when decoding fails.
// It performs no second request and does not inspect or copy a response body.
func responseInfo(header http.Header, err error, now time.Time) ResponseInfo {
	i := ResponseInfo{RequestID: publicToken(header.Get("X-Request-ID"), 128)}
	i.RetryAfter = parseRetryAfter(header.Get("Retry-After"), now)
	var apiErr *openai.APIError
	var requestErr *openai.RequestError
	switch {
	case errors.As(err, &apiErr):
		i.StatusCode = apiErr.HTTPStatusCode
		if code, ok := apiErr.Code.(string); ok {
			i.ErrorCode = publicToken(code, 64)
		}
	case errors.As(err, &requestErr):
		i.StatusCode = requestErr.HTTPStatusCode
	}
	return i
}

// publicToken accepts opaque bounded ASCII identifiers, never prose or URLs.
func publicToken(value string, limit int) string {
	if len(value) > limit {
		return ""
	}
	for _, c := range value {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' {
			continue
		}
		return ""
	}
	return value
}

// parseRetryAfter accepts HTTP delay-seconds or a future HTTP-date. Invalid,
// negative, expired, or duration-overflow values leave Temporal's retry policy intact.
func parseRetryAfter(value string, now time.Time) time.Duration {
	if value == "" {
		return 0
	}
	numeric := true
	for _, c := range value {
		if c < '0' || c > '9' {
			numeric = false
			break
		}
	}
	if numeric {
		seconds, err := strconv.ParseUint(value, 10, 63)
		if err == nil && seconds <= uint64((1<<63-1)/int64(time.Second)) {
			return time.Duration(seconds) * time.Second
		}
		return 0
	}
	date, err := http.ParseTime(value)
	if err != nil || !date.After(now) {
		return 0
	}
	return date.Sub(now)
}
