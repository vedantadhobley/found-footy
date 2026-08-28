// validation.go — per-binary semantic and cross-field configuration checks.
// Parsing proves types; these checks prove the resulting values can satisfy the
// runtime and persistence contracts that consume them.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxRecordedSearchAttempt = 20

var eventEnvironmentPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidateFor checks only the sections consumed by binary. It returns every
// detected problem in declaration order so one startup attempt gives the
// operator a complete, secret-free repair list.
func (c *Config) ValidateFor(binary Binary) error {
	if c == nil {
		return errors.New("configuration is nil")
	}

	v := &validator{}
	switch binary {
	case BinaryWorker:
		validateObservability(v, c.Observability)
		validatePostgres(v, c.Postgres)
		validateNATS(v, c.NATS)
		validateS3(v, c.S3)
		validateTemporal(v, c.Temporal)
		validateLLM(v, c.LLM)
		validateAPIFootball(v, c.APIFootball)
		validateSyndication(v, c.Syndication)
		validateTwitterClient(v, c.Twitter)
		validateFirefoxFleet(v, c.FirefoxFleet)
		validateFFmpeg(v, c.FFmpeg)
		validateWorkflows(v, c.Workflows)
		validateDiscovery(v, c.Discovery)
		validateDedup(v, c.Dedup)
		validateVideo(v, c.Video)
		validateVision(v, c.Vision)
		validateEvent(v, c.Event)
		v.check(c.Twitter.SearchTimeout <= c.Discovery.QueryTimeout,
			"TWITTER_SEARCH_TIMEOUT must be <= DISCOVERY_QUERY_TIMEOUT")
	case BinaryAPI:
		validateObservability(v, c.Observability)
		validatePostgres(v, c.Postgres)
		validateS3(v, c.S3)
		validateAPI(v, c.API)
		v.check(c.API.ListenAddr != c.Observability.MetricsAddr,
			"API_LISTEN_ADDR must differ from METRICS_ADDR")
	case BinaryTwitter:
		validateTwitterService(v, c.TwitterService)
	case BinaryTwitterAuth:
		validateTwitterAuth(v, c.TwitterAuth)
	default:
		return fmt.Errorf("unknown binary %q", binary)
	}
	return v.err()
}

type validator struct {
	errs []error
}

func (v *validator) check(ok bool, message string) {
	if !ok {
		v.errs = append(v.errs, errors.New(message))
	}
}

func (v *validator) err() error {
	return errors.Join(v.errs...)
}

func validateObservability(v *validator, cfg ObservabilityConfig) {
	switch strings.ToUpper(cfg.LogLevel) {
	case "DEBUG", "INFO", "WARN", "ERROR":
	default:
		v.check(false, "LOG_LEVEL must be one of DEBUG, INFO, WARN, or ERROR")
	}
	v.check(cfg.LogFormat == "json" || cfg.LogFormat == "text",
		"LOG_FORMAT must be json or text")
	validateListenAddress(v, "METRICS_ADDR", cfg.MetricsAddr)
}

func validatePostgres(v *validator, cfg PGConfig) {
	required(v, "PG_DSN", cfg.DSN)
	v.check(cfg.MaxConns > 0, "PG_MAX_CONNS must be > 0")
	v.check(cfg.MinConns >= 0, "PG_MIN_CONNS must be >= 0")
	v.check(cfg.MinConns <= cfg.MaxConns, "PG_MIN_CONNS must be <= PG_MAX_CONNS")
	positiveDuration(v, "PG_CONNECT_TIMEOUT", cfg.ConnectTimeout)
}

func validateNATS(v *validator, cfg NATSConfig) {
	required(v, "NATS_URL", cfg.URL)
	for _, raw := range strings.Split(cfg.URL, ",") {
		if strings.TrimSpace(raw) != "" {
			validateURLSchemes(v, "NATS_URL", raw, "nats", "tls", "ws", "wss")
		}
	}
	required(v, "NATS_CLIENT_NAME", cfg.ClientName)
	positiveDuration(v, "NATS_CONNECT_TIMEOUT", cfg.ConnectTimeout)
	positiveDuration(v, "NATS_RECONNECT_WAIT", cfg.ReconnectWait)
	v.check(cfg.MaxReconnects >= -1, "NATS_MAX_RECONNECTS must be >= -1")
}

func validateS3(v *validator, cfg S3Config) {
	validateHTTPURL(v, "S3_ENDPOINT", cfg.Endpoint)
	required(v, "S3_BUCKET", cfg.Bucket)
	required(v, "S3_REGION", cfg.Region)
	required(v, "S3_ACCESS_KEY_ID", cfg.AccessKeyID)
	required(v, "S3_SECRET_ACCESS_KEY", cfg.SecretAccessKey)
	positiveDuration(v, "S3_CONNECT_TIMEOUT", cfg.ConnectTimeout)
	positiveDuration(v, "S3_PRESIGNED_URL_TTL", cfg.PresignedURLTTL)
}

func validateTemporal(v *validator, cfg TemporalConfig) {
	required(v, "TEMPORAL_HOSTPORT", cfg.HostPort)
	required(v, "TEMPORAL_NAMESPACE", cfg.Namespace)
	required(v, "TEMPORAL_TASK_QUEUE", cfg.TaskQueue)
	positiveDuration(v, "TEMPORAL_CONNECT_TIMEOUT", cfg.ConnectTimeout)
	positiveDuration(v, "TEMPORAL_WORKER_SHUTDOWN_TIMEOUT", cfg.WorkerShutdownTimeout)
	v.check(cfg.MaxConcurrentActivities > 0,
		"TEMPORAL_MAX_CONCURRENT_ACTIVITIES must be > 0")
	v.check(cfg.MaxConcurrentWorkflowTasks > 0,
		"TEMPORAL_MAX_CONCURRENT_WORKFLOW_TASKS must be > 0")
}

func validateLLM(v *validator, cfg LLMConfig) {
	validateHTTPURL(v, "LLM_ENDPOINT_URL", cfg.Endpoint)
	required(v, "LLM_API_VERSION_PATH", cfg.APIVersionPath)
	required(v, "LLM_API_KEY", cfg.APIKey)
	v.check(cfg.ChatConcurrencyCap > 0, "LLM_CHAT_CONCURRENCY_CAP must be > 0")
	positiveDuration(v, "LLM_CONNECT_TIMEOUT", cfg.ConnectTimeout)
	positiveDuration(v, "LLM_REQUEST_TIMEOUT", cfg.RequestTimeout)
}

func validateAPIFootball(v *validator, cfg APIFootballConfig) {
	validateHTTPURL(v, "API_FOOTBALL_BASE_URL", cfg.BaseURL)
	required(v, "API_FOOTBALL_KEY", cfg.APIKey)
	positiveDuration(v, "API_FOOTBALL_TIMEOUT", cfg.Timeout)
	v.check(len(cfg.TrackedLeagueIDs) > 0,
		"API_FOOTBALL_TRACKED_LEAGUES must contain at least one league ID")
	seen := make(map[int]struct{}, len(cfg.TrackedLeagueIDs))
	for _, id := range cfg.TrackedLeagueIDs {
		v.check(id > 0, "API_FOOTBALL_TRACKED_LEAGUES IDs must be > 0")
		if _, exists := seen[id]; exists {
			v.check(false, "API_FOOTBALL_TRACKED_LEAGUES must not contain duplicates")
		}
		seen[id] = struct{}{}
	}
	v.check(cfg.TopFlightCacheHours > 0,
		"API_FOOTBALL_TOP_FLIGHT_CACHE_HOURS must be > 0")
	v.check(cfg.FetchWindowFutureDays > 0,
		"API_FOOTBALL_FETCH_WINDOW_FUTURE_DAYS must be > 0")
}

func validateSyndication(v *validator, cfg SyndicationConfig) {
	validateHTTPURL(v, "SYNDICATION_BASE_URL", cfg.BaseURL)
	required(v, "SYNDICATION_USER_AGENT", cfg.UserAgent)
	positiveDuration(v, "SYNDICATION_TIMEOUT", cfg.Timeout)
}

func validateTwitterClient(v *validator, cfg TwitterConfig) {
	validateHTTPURL(v, "TWITTER_SERVICE_URL", cfg.BaseURL)
	positiveDuration(v, "TWITTER_SEARCH_TIMEOUT", cfg.SearchTimeout)
}

func validateFirefoxFleet(v *validator, cfg FirefoxFleetConfig) {
	if !cfg.Enabled {
		return
	}
	required(v, "FIREFOXFLEET_IMAGE", cfg.Image)
	required(v, "FIREFOXFLEET_NETWORK", cfg.Network)
	v.check(filepath.IsAbs(cfg.CookieHostPath),
		"FIREFOXFLEET_COOKIE_HOST_PATH must be an absolute host path")
	v.check(cfg.InstanceMemLimit > 0, "FIREFOXFLEET_INSTANCE_MEM_BYTES must be > 0")
	v.check(cfg.MaxInstances > 0, "FIREFOXFLEET_MAX_INSTANCES must be > 0")
}

func validateFFmpeg(v *validator, cfg FFmpegConfig) {
	required(v, "FFMPEG_PATH", cfg.FFmpegPath)
	required(v, "FFPROBE_PATH", cfg.FFprobePath)
	positiveDuration(v, "FFMPEG_TIMEOUT", cfg.Timeout)
	positiveDuration(v, "FFMPEG_DENSE_TIMEOUT", cfg.DenseTimeout)
	v.check(cfg.MaxProcesses > 0, "FFMPEG_MAX_CONCURRENT must be > 0")
	v.check(cfg.ThreadsPerProc >= 0, "FFMPEG_THREADS_PER_PROC must be >= 0")
	v.check(cfg.FrameQuality >= 2 && cfg.FrameQuality <= 31,
		"FFMPEG_FRAME_QUALITY must be between 2 and 31")
}

func validateWorkflows(v *validator, cfg WorkflowsConfig) {
	positiveDuration(v, "WORKFLOWS_ACTIVE_FIXTURE_POLL_INTERVAL", cfg.ActiveFixturePollInterval)
	positiveDuration(v, "WORKFLOWS_TERMINAL_GRACE_PERIOD", cfg.TerminalGracePeriod)
	required(v, "WORKFLOWS_STAGING_POLL_CRON", cfg.StagingPollCron)
	required(v, "WORKFLOWS_TWITTER_MAINTENANCE_CRON", cfg.TwitterMaintenanceCron)
	v.check(cfg.ActivationWindow >= 0, "WORKFLOWS_ACTIVATION_WINDOW must be >= 0")
	v.check(cfg.RetentionDays >= 0, "WORKFLOWS_RETENTION_DAYS must be >= 0")
}

func validateDiscovery(v *validator, cfg DiscoveryConfig) {
	v.check(cfg.MaxAttempts >= 1 && cfg.MaxAttempts <= maxRecordedSearchAttempt,
		fmt.Sprintf("DISCOVERY_MAX_ATTEMPTS must be between 1 and %d", maxRecordedSearchAttempt))
	v.check(cfg.MaxUnavailableAttempts >= 1 && cfg.MaxUnavailableAttempts <= maxRecordedSearchAttempt,
		fmt.Sprintf("DISCOVERY_MAX_UNAVAILABLE_ATTEMPTS must be between 1 and %d", maxRecordedSearchAttempt))
	positiveDuration(v, "DISCOVERY_ATTEMPT_SPACING", cfg.AttemptSpacing)
	v.check(cfg.MaxAgeMinutes > 0, "DISCOVERY_MAX_AGE_MINUTES must be > 0")
	positiveDuration(v, "DISCOVERY_QUERY_TIMEOUT", cfg.QueryTimeout)
}

func validateDedup(v *validator, cfg DedupConfig) {
	v.check(cfg.FrameIntervalSecs > 0, "DEDUP_FRAME_INTERVAL_SECS must be > 0")
	v.check(cfg.MaxHamming >= 1 && cfg.MaxHamming <= 64,
		"DEDUP_MAX_HAMMING must be between 1 and 64")
	v.check(cfg.MinRunFrames > 0, "DEDUP_MIN_RUN_FRAMES must be > 0")
	v.check(cfg.MaxGapFrames > 0, "DEDUP_MAX_GAP_FRAMES must be > 0")
	v.check(cfg.MaxGapFrames < cfg.MinRunFrames,
		"DEDUP_MAX_GAP_FRAMES must be < DEDUP_MIN_RUN_FRAMES")
	v.check(cfg.LongMaxHamming >= 1 && cfg.LongMaxHamming <= 64,
		"DEDUP_LONG_MAX_HAMMING must be between 1 and 64")
	v.check(cfg.LongMinRunFrames >= cfg.MinRunFrames,
		"DEDUP_LONG_MIN_RUN_FRAMES must be >= DEDUP_MIN_RUN_FRAMES")
	v.check(cfg.LongMaxGapFrames > 0, "DEDUP_LONG_MAX_GAP_FRAMES must be > 0")
	v.check(cfg.LongMaxGapFrames < cfg.LongMinRunFrames,
		"DEDUP_LONG_MAX_GAP_FRAMES must be < DEDUP_LONG_MIN_RUN_FRAMES")
}

func validateVideo(v *validator, cfg VideoConfig) {
	v.check(filepath.IsAbs(cfg.ScratchDir), "VIDEO_SCRATCH_DIR must be an absolute path")
	validateObjectPrefix(v, "VIDEO_STAGING_PREFIX", cfg.StagingPrefix)
	validateObjectPrefix(v, "VIDEO_ASSETS_PREFIX", cfg.AssetsPrefix)
	v.check(cfg.StagingPrefix != cfg.AssetsPrefix,
		"VIDEO_STAGING_PREFIX must differ from VIDEO_ASSETS_PREFIX")
	v.check(cfg.HardFilter.MinDurationSecs > 0,
		"HARDFILTER_MIN_DURATION_SECS must be > 0")
	v.check(cfg.HardFilter.MaxDurationSecs >= cfg.HardFilter.MinDurationSecs,
		"HARDFILTER_MAX_DURATION_SECS must be >= HARDFILTER_MIN_DURATION_SECS")
	v.check(cfg.HardFilter.MinAspectRatio > 0, "HARDFILTER_MIN_ASPECT must be > 0")
	v.check(cfg.HardFilter.MaxAspectRatio >= cfg.HardFilter.MinAspectRatio,
		"HARDFILTER_MAX_ASPECT must be >= HARDFILTER_MIN_ASPECT")
	v.check(cfg.HardFilter.MinShortEdge > 0, "HARDFILTER_MIN_SHORT_EDGE must be > 0")
	v.check(cfg.HardFilter.MinFrameRate > 0, "HARDFILTER_MIN_FRAMERATE must be > 0")
}

func validateVision(v *validator, cfg VisionConfig) {
	v.check(cfg.ToleranceMinutes >= 0, "VISION_TOLERANCE_MINUTES must be >= 0")
	v.check(cfg.FrameQuality >= 2 && cfg.FrameQuality <= 31,
		"VISION_FRAME_QUALITY must be between 2 and 31")
}

func validateEvent(v *validator, cfg EventConfig) {
	v.check(eventEnvironmentPattern.MatchString(cfg.Environment),
		"EVENT_ENV must be a lowercase environment token")
}

func validateAPI(v *validator, cfg APIConfig) {
	validateListenAddress(v, "API_LISTEN_ADDR", cfg.ListenAddr)
	positiveDuration(v, "API_READ_TIMEOUT", cfg.ReadTimeout)
	positiveDuration(v, "API_WRITE_TIMEOUT", cfg.WriteTimeout)
}

func validateTwitterService(v *validator, cfg TwitterServiceConfig) {
	validateListenAddress(v, "TWITTER_SERVICE_ADDR", cfg.ListenAddr)
	v.check(filepath.IsAbs(cfg.CookieFile), "TWITTER_COOKIE_FILE must be an absolute path")
	v.check(filepath.IsAbs(cfg.ProfileDir), "TWITTER_PROFILE_DIR must be an absolute path")
	if cfg.VNCURL != "" {
		validateHTTPURL(v, "TWITTER_VNC_URL", cfg.VNCURL)
	}
}

func validateTwitterAuth(v *validator, cfg TwitterAuthConfig) {
	validateListenAddress(v, "TWITTER_AUTH_ADDR", cfg.ListenAddr)
	v.check(filepath.IsAbs(cfg.CookieFile), "TWITTER_AUTH_COOKIE_FILE must be an absolute path")
	v.check(filepath.IsAbs(cfg.ProfileDir), "TWITTER_AUTH_PROFILE_DIR must be an absolute path")
	positiveDuration(v, "TWITTER_AUTH_POLL_INTERVAL", cfg.PollInterval)
}

func required(v *validator, name, value string) {
	v.check(strings.TrimSpace(value) != "", name+" must not be empty")
}

func positiveDuration(v *validator, name string, value time.Duration) {
	v.check(value > 0, name+" must be > 0")
}

func validateHTTPURL(v *validator, name, value string) {
	validateURLSchemes(v, name, value, "http", "https")
}

func validateURLSchemes(v *validator, name, value string, schemes ...string) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" {
		v.check(false, name+" must be an absolute URL")
		return
	}
	for _, scheme := range schemes {
		if parsed.Scheme == scheme {
			return
		}
	}
	v.check(false, fmt.Sprintf("%s must use one of: %s", name, strings.Join(schemes, ", ")))
}

func validateListenAddress(v *validator, name, value string) {
	_, portText, err := net.SplitHostPort(value)
	if err != nil {
		v.check(false, name+" must be a host:port listen address")
		return
	}
	port, err := strconv.Atoi(portText)
	v.check(err == nil && port >= 0 && port <= 65535,
		name+" must contain a numeric port between 0 and 65535")
}

func validateObjectPrefix(v *validator, name, value string) {
	clean := path.Clean(value)
	v.check(value != "" && clean != "." && clean != ".." &&
		!strings.HasPrefix(value, "/") && !strings.HasPrefix(clean, "../"),
		name+" must be a non-empty relative object prefix")
}
