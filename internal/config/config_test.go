// Tests for per-binary environment parsing and semantic validation.
package config

import (
	"strings"
	"testing"
)

func TestLoadForAPIDefaultsAndOverrides(t *testing.T) {
	setValidAPIEnvironment(t)
	t.Setenv("LOG_LEVEL", "DEBUG")
	t.Setenv("LOG_FORMAT", "text")
	t.Setenv("METRICS_ADDR", ":9090")

	cfg, err := LoadFor(BinaryAPI)
	if err != nil {
		t.Fatalf("LoadFor(api): %v", err)
	}
	if cfg.Observability.LogLevel != "DEBUG" {
		t.Errorf("LogLevel = %q, want DEBUG", cfg.Observability.LogLevel)
	}
	if cfg.Observability.LogFormat != "text" {
		t.Errorf("LogFormat = %q, want text", cfg.Observability.LogFormat)
	}
	if cfg.Observability.MetricsAddr != ":9090" {
		t.Errorf("MetricsAddr = %q, want :9090", cfg.Observability.MetricsAddr)
	}
	if cfg.API.ListenAddr != ":8081" {
		t.Errorf("API ListenAddr default = %q, want :8081", cfg.API.ListenAddr)
	}
}

func TestLoadForDoesNotParseAnotherBinaryEnvironment(t *testing.T) {
	setValidAPIEnvironment(t)
	t.Setenv("DISCOVERY_MAX_ATTEMPTS", "not-an-integer")

	if _, err := LoadFor(BinaryAPI); err != nil {
		t.Fatalf("API rejected worker-only discovery environment: %v", err)
	}
	if _, err := LoadFor(BinaryWorker); err == nil {
		t.Fatal("worker accepted malformed DISCOVERY_MAX_ATTEMPTS")
	}
}

func TestLoadForAPIDoesNotRequireOrParseNATS(t *testing.T) {
	setValidAPIEnvironment(t)
	t.Setenv("NATS_MAX_RECONNECTS", "not-an-integer")

	if _, err := LoadFor(BinaryAPI); err != nil {
		t.Fatalf("API rejected unused NATS environment: %v", err)
	}

	setValidWorkerEnvironment(t)
	if _, err := LoadFor(BinaryWorker); err == nil {
		t.Fatal("worker accepted malformed NATS_MAX_RECONNECTS")
	}
}

func TestLoadForWorkerRejectsSemanticErrors(t *testing.T) {
	cases := []struct {
		name   string
		values map[string]string
		want   string
	}{
		{name: "zero attempts", values: map[string]string{"DISCOVERY_MAX_ATTEMPTS": "0"}, want: "DISCOVERY_MAX_ATTEMPTS"},
		{name: "attempt exceeds schema", values: map[string]string{"DISCOVERY_MAX_ATTEMPTS": "21"}, want: "between 1 and 20"},
		{name: "zero unavailable budget", values: map[string]string{"DISCOVERY_MAX_UNAVAILABLE_ATTEMPTS": "0"}, want: "DISCOVERY_MAX_UNAVAILABLE_ATTEMPTS"},
		{name: "pool floor exceeds cap", values: map[string]string{"PG_MIN_CONNS": "11"}, want: "PG_MIN_CONNS must be <= PG_MAX_CONNS"},
		{name: "enabled fleet has no capacity", values: map[string]string{"FIREFOXFLEET_ENABLED": "true", "FIREFOXFLEET_MAX_INSTANCES": "0"}, want: "FIREFOXFLEET_MAX_INSTANCES"},
		{name: "dedup misses consume window", values: map[string]string{"DEDUP_MAX_GAP_FRAMES": "30"}, want: "DEDUP_MAX_GAP_FRAMES must be < DEDUP_MIN_RUN_FRAMES"},
		{name: "hard filter bounds inverted", values: map[string]string{"HARDFILTER_MIN_DURATION_SECS": "91"}, want: "HARDFILTER_MAX_DURATION_SECS"},
		{name: "ffmpeg has no slots", values: map[string]string{"FFMPEG_MAX_CONCURRENT": "0"}, want: "FFMPEG_MAX_CONCURRENT"},
		{name: "search outlives activity", values: map[string]string{"TWITTER_SEARCH_TIMEOUT": "121s"}, want: "TWITTER_SEARCH_TIMEOUT must be <= DISCOVERY_QUERY_TIMEOUT"},
		{name: "invalid event token", values: map[string]string{"EVENT_ENV": "Prod!"}, want: "EVENT_ENV"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setValidWorkerEnvironment(t)
			for key, value := range tc.values {
				t.Setenv(key, value)
			}
			_, err := LoadFor(BinaryWorker)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestLoadForWorkerUsesLiveCalibratedAspectDefault(t *testing.T) {
	setValidWorkerEnvironment(t)

	cfg, err := LoadFor(BinaryWorker)
	if err != nil {
		t.Fatalf("LoadFor(worker): %v", err)
	}
	if got := cfg.Video.HardFilter.MinAspectRatio; got != 1.73 {
		t.Errorf("MinAspectRatio default = %v, want 1.73", got)
	}
	if got := cfg.Discovery.MaxUnavailableAttempts; got != 15 {
		t.Errorf("MaxUnavailableAttempts default = %d, want 15", got)
	}
}

func TestLoadForAPIRejectsSharedListenAddress(t *testing.T) {
	setValidAPIEnvironment(t)
	t.Setenv("API_LISTEN_ADDR", ":8080")
	_, err := LoadFor(BinaryAPI)
	if err == nil || !strings.Contains(err.Error(), "API_LISTEN_ADDR must differ") {
		t.Fatalf("error = %v, want listen-address collision", err)
	}
}

func TestLoadForTwitterUsesStrictBooleans(t *testing.T) {
	t.Setenv("TWITTER_HEADLESS", "truthy")
	if _, err := LoadFor(BinaryTwitter); err == nil {
		t.Fatal("Twitter accepted a malformed boolean")
	}
}

func TestLoadForTwitterAuthRejectsRelativePaths(t *testing.T) {
	t.Setenv("TWITTER_AUTH_PROFILE_DIR", "relative")
	_, err := LoadFor(BinaryTwitterAuth)
	if err == nil || !strings.Contains(err.Error(), "TWITTER_AUTH_PROFILE_DIR must be an absolute path") {
		t.Fatalf("error = %v, want absolute profile path", err)
	}
}

func TestLoadForRejectsUnknownBinary(t *testing.T) {
	if _, err := LoadFor(Binary("typo")); err == nil {
		t.Fatal("LoadFor accepted an unknown binary")
	}
}

func setValidAPIEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("PG_DSN", "postgres://user:pass@postgres:5432/found_footy")
	t.Setenv("S3_ENDPOINT", "http://garage:3900")
	t.Setenv("S3_BUCKET", "found-footy")
	t.Setenv("S3_ACCESS_KEY_ID", "test-access")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test-secret")
}

func setValidWorkerEnvironment(t *testing.T) {
	t.Helper()
	setValidAPIEnvironment(t)
	t.Setenv("NATS_URL", "nats://nats:4222")
	t.Setenv("NATS_CLIENT_NAME", "found-footy-test-worker")
	t.Setenv("TEMPORAL_HOSTPORT", "temporal:7233")
	t.Setenv("LLM_ENDPOINT_URL", "http://joi.luv")
	t.Setenv("API_FOOTBALL_KEY", "test-key")
}
