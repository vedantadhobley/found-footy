// API-Football adapter Action enum values + init-time registration.
package vocabulary

// API-Football adapter actions. Call-level events labeled generically
// (call/call_failed); the endpoint label on the metric captures which
// route was hit without exploding action cardinality.
const (
	ActionAPIFootballConnected      Action = "apifootball_connected"       // /status probe succeeded at startup — matches pg/nats/s3/llm _connected pattern
	ActionAPIFootballCall           Action = "apifootball_call"            // HTTP call returned 2xx
	ActionAPIFootballFailed         Action = "apifootball_failed"          // HTTP call errored or returned non-2xx
	ActionAPIFootballRateLimited    Action = "apifootball_rate_limited"    // remote returned 429 or reported 0 remaining
	ActionAPIFootballQuotaObserved  Action = "apifootball_quota_observed"  // periodic snapshot of remaining daily quota
)

func init() {
	registerActions(
		ActionAPIFootballConnected,
		ActionAPIFootballCall,
		ActionAPIFootballFailed,
		ActionAPIFootballRateLimited,
		ActionAPIFootballQuotaObserved,
	)
}
