// TwitterConfig — env-driven settings for the internal Twitter (Firefox+Selenium) service.
package config

import "time"

// TwitterConfig covers the HTTP client for found-footy's own
// twitter/ container, which runs Firefox+Selenium browser automation
// for search + video download. The client just POSTs to /search and
// /download; the container does the actual scraping.
type TwitterConfig struct {
	// BaseURL is the internal service URL. Default matches the compose
	// service name.
	BaseURL string `env:"TWITTER_SERVICE_URL" envDefault:"http://twitter:8888"`

	// SearchTimeout bounds a single /search call. Browser automation
	// can take 30-60s under load.
	SearchTimeout time.Duration `env:"TWITTER_SEARCH_TIMEOUT" envDefault:"120s"`

	// DownloadTimeout bounds a single /download call. Video downloads
	// via yt-dlp or similar can be slow.
	DownloadTimeout time.Duration `env:"TWITTER_DOWNLOAD_TIMEOUT" envDefault:"180s"`
}
