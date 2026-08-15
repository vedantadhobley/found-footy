// FirefoxFleetConfig — env-driven settings for the per-event Firefox
// fleet (#160). The worker provisions one twitter/Firefox container per
// active event via the Docker API (socket mounted into the worker); each
// is named deterministically from the event ID, reached by container-name
// DNS on the shared network, and released on event completion / decay.
package config

import "time"

// FirefoxFleetConfig configures internal/infra/firefoxfleet.
type FirefoxFleetConfig struct {
	// Enabled gates the whole per-event-instance path. When false the
	// worker falls back to the single shared twitter service at
	// TwitterConfig.BaseURL (pre-#160 behavior) — so the fleet can ship
	// dark and flip on once verified.
	Enabled bool `env:"FIREFOXFLEET_ENABLED" envDefault:"false"`

	// Image is the twitter/Firefox image provisioned per event. Must be
	// pre-built (docker compose build twitter). Env-specific tag.
	Image string `env:"FIREFOXFLEET_IMAGE" envDefault:"found-footy-dev-twitter:latest"`

	// Network is the docker network instances attach to so the worker
	// reaches them by container-name DNS. MUST be a network the worker is
	// also on. This is the *actual* Docker network name. Both compose files
	// set an explicit `name:` on their network (docker-compose.dev.yml →
	// `found-footy-dev`), matching Python prod's convention, so the name is
	// clean rather than Compose's `<project>_<key>` default. Override per-env
	// via the env var (prod sets `found-footy-prod`).
	Network string `env:"FIREFOXFLEET_NETWORK" envDefault:"found-footy-dev"`

	// CookieHostPath is the HOST path to the shared cookie file, bind-
	// mounted read-write into each instance at /config/twitter_cookies.json.
	// It is a host path (not a worker-container path) because the Docker
	// daemon resolves binds against the host filesystem. All instances
	// share it; atomic writes + fingerprint dedupe + filesystem-mtime
	// coordination (internal/twitter/cookies_backup.go, decisions.md
	// 2026-07-21) make concurrent writers safe, so per-event instances
	// keep the fleet's cookies fresh during active periods.
	CookieHostPath string `env:"FIREFOXFLEET_COOKIE_HOST_PATH" envDefault:"/home/vedanta/.config/found-footy/twitter_cookies.json"`

	// InstanceMemLimit caps each instance's memory (bytes). Firefox stays
	// under ~2 GiB with the cache-bounding prefs; per-instance lifetime =
	// event lifetime (~15 min) so it never bloats like the old always-on
	// container did.
	InstanceMemLimit int64 `env:"FIREFOXFLEET_INSTANCE_MEM_BYTES" envDefault:"2147483648"` // 2 GiB

	// MaxInstances bounds concurrent instances. The binding constraint is
	// not host memory (16 × 2 GiB is fine on this node) but Twitter's
	// tolerance for concurrent sessions on the single shared account —
	// Python tested ~8; >8 is beyond that range, so watch for auth
	// flapping if this is raised. Provision blocks-and-waits at cap.
	MaxInstances int `env:"FIREFOXFLEET_MAX_INSTANCES" envDefault:"16"`

	// HealthTimeout bounds the post-start wait for /health before
	// Provision returns the instance address. Covers container start +
	// browser launch + cookie load + auth-verify (~30s typical).
	HealthTimeout time.Duration `env:"FIREFOXFLEET_HEALTH_TIMEOUT" envDefault:"45s"`
}
