// APIFootballConfig — env-driven settings for the API-Football HTTP adapter.
package config

import "time"

// APIFootballConfig covers the api-sports.io REST client used by ingest
// and the active/staging poll workflows. Auth is via the x-apisports-key header on the
// direct API-Sports endpoint. Remote enforces two rate-limit windows
// (per-minute burst + daily quota) via response headers; we track them
// but don't enforce client-side. See docs/api-football/rate-limits.md.
//
// BaseURL + APIKey are not tagged required at the env layer; the adapter
// constructor returns a descriptive error when a consuming binary starts
// without them.
type APIFootballConfig struct {
	// BaseURL is the API root. Defaults to the api-sports.io v3 host.
	BaseURL string `env:"API_FOOTBALL_BASE_URL" envDefault:"https://v3.football.api-sports.io"`

	// APIKey is the x-apisports-key header value.
	APIKey string `env:"API_FOOTBALL_KEY"`

	// Timeout bounds an individual HTTP request. The remote occasionally
	// takes 5-10s under load; 30s gives headroom without blocking the
	// activity forever.
	Timeout time.Duration `env:"API_FOOTBALL_TIMEOUT" envDefault:"30s"`

	// TrackedLeagueIDs is the CSV list of API-Football league IDs whose
	// teams we care about. Ingest fetches the current-season team roster
	// for each and unions them into `tracked_teams_cache`. Fixtures are
	// then filtered to matches where at least one team is in that set.
	//
	// Default covers the top-5 European club leagues + the current
	// World Cup. Include tournament league IDs (WC=1, Euros=4, Copa=9,
	// etc.) to catch national-team fixtures during those windows — the
	// /teams call for a tournament league returns the participating
	// national teams for that season, which is exactly what we need.
	// See decisions.md 2026-07-09 Ingest-regression-fix entry.
	TrackedLeagueIDs []int `env:"API_FOOTBALL_TRACKED_LEAGUES" envDefault:"39,140,78,135,61,1,253"`

	// TopFlightCacheHours bounds staleness of `tracked_teams_cache`.
	// Ingest's step-0 refresh checks the OLDEST row's refreshed_at
	// against this; refreshes if the delta exceeds it. Mirrors Python's
	// 24h cache (archive/src/utils/team_data.py:TOP_FLIGHT_CACHE_HOURS).
	TopFlightCacheHours int `env:"API_FOOTBALL_TOP_FLIGHT_CACHE_HOURS" envDefault:"24"`

	// FetchWindowFutureDays is the default upper bound on the by-day
	// scan when smart-lookahead runs. IngestWorkflow always fetches
	// today + tomorrow; if tomorrow is empty AND FetchFuture=true, it
	// scans day+2 through day+FetchWindowFutureDays looking for the
	// next non-empty day. Default 30 matches Python's MAX_LOOKAHEAD_DAYS
	// (archive/src/workflows/ingest_workflow.py) — long enough to bridge
	// international breaks and off-season gaps.
	FetchWindowFutureDays int `env:"API_FOOTBALL_FETCH_WINDOW_FUTURE_DAYS" envDefault:"30"`
}
