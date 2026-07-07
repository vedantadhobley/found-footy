// APIFootballConfig — env-driven settings for the API-Football HTTP adapter.
package config

import "time"

// APIFootballConfig covers the api-sports.io REST client used by ingest
// + monitor workflows. Auth is via the x-rapidapi-key header (single
// per-account key). Remote enforces rate limits (100/min free tier);
// we track them via response headers but don't enforce client-side.
//
// BaseURL + APIKey are not tagged required at env layer because Phase
// S1-S6 binaries don't need API-Football yet; the constructor returns a
// descriptive error when a binary that DOES need it starts without them.
type APIFootballConfig struct {
	// BaseURL is the API root. Defaults to the api-sports.io v3 host.
	BaseURL string `env:"API_FOOTBALL_BASE_URL" envDefault:"https://v3.football.api-sports.io"`

	// APIKey is the x-rapidapi-key header value.
	APIKey string `env:"API_FOOTBALL_KEY"`

	// Timeout bounds an individual HTTP request. The remote occasionally
	// takes 5-10s under load; 30s gives headroom without blocking the
	// activity forever.
	Timeout time.Duration `env:"API_FOOTBALL_TIMEOUT" envDefault:"30s"`
}
