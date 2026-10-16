// Accounting wire tests distinguish old producers and preserve zero-valued quota observations.
package twittersearch

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTimelineAccountingIsAdditive prevents an absent field from becoming a claimed zero-request scan.
func TestTimelineAccountingIsAdditive(t *testing.T) {
	var legacy SearchResponse
	require.NoError(t, json.Unmarshal([]byte(`{"status":"success","evidence":{}}`), &legacy))
	require.Nil(t, legacy.Evidence.Timeline)
	encoded, err := json.Marshal(legacy)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), `"timeline":`)

	zero := int64(0)
	legacy.Evidence.Timeline = &TimelineAccounting{ScanID: "fixture-scan", Requests: 1,
		Records: []TimelineRequestRecord{{Sequence: 1, ResponseMS: &zero, RateLimitRemain: &zero}}}
	encoded, err = json.Marshal(legacy)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"response_ms":0`)
	require.Contains(t, string(encoded), `"rate_limit_remaining":0`)
	var restored SearchResponse
	require.NoError(t, json.Unmarshal(encoded, &restored))
	require.Equal(t, legacy.Evidence.Timeline, restored.Evidence.Timeline)
}
