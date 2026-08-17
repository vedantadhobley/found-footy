// TwitterConfig — worker-side HTTP settings for the Playwright-Go Twitter service.
package config

import "time"

// TwitterConfig covers the HTTP client for found-footy's own twitter
// container, which runs Firefox through Playwright-Go. The client posts only
// to /search; video resolution and download use the syndication adapter.
type TwitterConfig struct {
	// BaseURL is the internal service URL. Default matches the compose
	// service name.
	BaseURL string `env:"TWITTER_SERVICE_URL" envDefault:"http://twitter:8888"`

	// SearchTimeout bounds a single /search call. Browser automation
	// can take 30-60s under load.
	SearchTimeout time.Duration `env:"TWITTER_SEARCH_TIMEOUT" envDefault:"120s"`
}
