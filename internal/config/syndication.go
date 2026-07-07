// SyndicationConfig — env-driven settings for the Twitter syndication (guestpass) adapter.
package config

import "time"

// SyndicationConfig covers Twitter's guestpass syndication endpoint —
// public URLs used to fetch tweet content without authentication. The
// browser-automation twitter/ service falls back to this for
// low-cost lookups.
type SyndicationConfig struct {
	// BaseURL is the syndication host. Sometimes changes; env-overridable.
	BaseURL string `env:"SYNDICATION_BASE_URL" envDefault:"https://cdn.syndication.twimg.com"`

	// UserAgent — a real-browser UA reduces bot-detection false
	// positives. Rotate periodically when the site starts blocking.
	UserAgent string `env:"SYNDICATION_USER_AGENT" envDefault:"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36"`

	// Timeout bounds an individual syndication call.
	Timeout time.Duration `env:"SYNDICATION_TIMEOUT" envDefault:"15s"`
}
