// twitter_service.go — process configuration for the Playwright-backed
// Twitter search HTTP service.
package config

// TwitterServiceConfig configures cmd/twitter itself. TwitterConfig in
// twitter.go configures the worker-side HTTP client that calls this service.
type TwitterServiceConfig struct {
	ListenAddr string `env:"TWITTER_SERVICE_ADDR" envDefault:":8888"`
	CookieFile string `env:"TWITTER_COOKIE_FILE" envDefault:"/config/twitter_cookies.json"`
	ProfileDir string `env:"TWITTER_PROFILE_DIR" envDefault:"/data/firefox-profile"`
	Headless   bool   `env:"TWITTER_HEADLESS" envDefault:"true"`

	// VNCURL and VNCStartCommand are optional operator instructions returned
	// when authentication expires. Per-event headless instances leave both
	// empty; the static service receives them from Compose.
	VNCURL          string `env:"TWITTER_VNC_URL" envDefault:""`
	VNCStartCommand string `env:"TWITTER_VNC_START_CMD" envDefault:""`
}
