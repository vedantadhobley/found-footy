// Metadata parser tests pin bounded identifiers and HTTP Retry-After semantics.
package llm

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestParseRetryAfter keeps invalid/expired guidance from inventing a delay.
func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 17, 18, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"", 0}, {"0", 0}, {"7", 7 * time.Second},
		{now.Add(12 * time.Second).Format(http.TimeFormat), 12 * time.Second},
		{now.Add(-time.Second).Format(http.TimeFormat), 0},
		{"-1", 0}, {"+1", 0}, {"1.5", 0}, {"later", 0},
		{"9223372036854775807", 0}, {strings.Repeat("9", 100), 0},
	} {
		t.Run(tc.value, func(t *testing.T) {
			require.Equal(t, tc.want, parseRetryAfter(tc.value, now))
		})
	}
}

// TestResponseInfoValid prevents raw bodies/URLs and malformed status values in durable details.
func TestResponseInfoValid(t *testing.T) {
	for _, i := range []ResponseInfo{
		{}, {RequestID: "req_abc-123.4", ErrorCode: "capacity_exceeded", StatusCode: 429, RetryAfter: time.Second},
	} {
		require.True(t, i.Valid())
	}
	for _, i := range []ResponseInfo{
		{RequestID: strings.Repeat("x", 129)}, {RequestID: "raw response body"},
		{ErrorCode: "https://private.example"}, {ErrorCode: strings.Repeat("x", 65)},
		{StatusCode: 99}, {StatusCode: 600}, {RetryAfter: -time.Second},
	} {
		require.False(t, i.Valid())
	}
}
