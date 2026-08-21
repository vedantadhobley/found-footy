// twitter_auth.go defines the raw-Firefox cookie-capture process settings.
package config

import "time"

// TwitterAuthConfig configures cmd/twitter-auth. It is intentionally separate
// from TwitterServiceConfig because this process never launches Playwright or
// serves search traffic.
type TwitterAuthConfig struct {
	ListenAddr   string        `env:"TWITTER_AUTH_ADDR" envDefault:":8888"`
	CookieFile   string        `env:"TWITTER_AUTH_COOKIE_FILE" envDefault:"/config/twitter_cookies.json"`
	ProfileDir   string        `env:"TWITTER_AUTH_PROFILE_DIR" envDefault:"/data/firefox-profile"`
	PollInterval time.Duration `env:"TWITTER_AUTH_POLL_INTERVAL" envDefault:"2s"`
}
