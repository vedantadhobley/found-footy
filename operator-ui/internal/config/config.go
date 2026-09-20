// Package config loads the operator UI's deliberately small, independent runtime contract.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config contains only the database, object-store, and HTTP settings used by
// the operator UI. It intentionally does not load the Found Footy app config.
type Config struct {
	Environment    string
	HTTPAddr       string
	PollInterval   time.Duration
	ResyncInterval time.Duration
	PGDSN          string
	PGMaxConns     int32
	S3Endpoint     string
	S3Bucket       string
	S3Region       string
	S3AccessKey    string
	S3SecretKey    string
	S3PathStyle    bool
}

// Load reads and validates the independent operator UI environment.
func Load() (Config, error) {
	c := Config{
		Environment:    envOr("OPERATOR_UI_ENV", "unknown"),
		HTTPAddr:       envOr("OPERATOR_UI_HTTP_ADDR", ":8080"),
		PollInterval:   2 * time.Second,
		ResyncInterval: 30 * time.Second,
		PGDSN:          os.Getenv("PG_DSN"),
		PGMaxConns:     4,
		S3Endpoint:     os.Getenv("S3_ENDPOINT"),
		S3Bucket:       os.Getenv("S3_BUCKET"),
		S3Region:       envOr("S3_REGION", "garage"),
		S3AccessKey:    os.Getenv("S3_ACCESS_KEY_ID"),
		S3SecretKey:    os.Getenv("S3_SECRET_ACCESS_KEY"),
		S3PathStyle:    true,
	}
	var err error
	if c.PollInterval, err = durationEnv("OPERATOR_UI_POLL_INTERVAL", c.PollInterval); err != nil {
		return Config{}, err
	}
	if c.ResyncInterval, err = durationEnv("OPERATOR_UI_RESYNC_INTERVAL", c.ResyncInterval); err != nil {
		return Config{}, err
	}
	if c.PGMaxConns, err = int32Env("OPERATOR_UI_PG_MAX_CONNS", c.PGMaxConns); err != nil {
		return Config{}, err
	}
	if raw := os.Getenv("S3_USE_PATH_STYLE"); raw != "" {
		if c.S3PathStyle, err = strconv.ParseBool(raw); err != nil {
			return Config{}, fmt.Errorf("S3_USE_PATH_STYLE: %w", err)
		}
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate rejects incomplete or unsafe runtime configuration before opening listeners.
func (c Config) Validate() error {
	required := map[string]string{
		"PG_DSN": c.PGDSN, "S3_ENDPOINT": c.S3Endpoint, "S3_BUCKET": c.S3Bucket,
		"S3_ACCESS_KEY_ID": c.S3AccessKey, "S3_SECRET_ACCESS_KEY": c.S3SecretKey,
	}
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if _, err := url.ParseRequestURI(c.S3Endpoint); err != nil {
		return fmt.Errorf("S3_ENDPOINT: %w", err)
	}
	if c.PollInterval < 250*time.Millisecond || c.PollInterval > time.Minute {
		return fmt.Errorf("OPERATOR_UI_POLL_INTERVAL must be between 250ms and 1m")
	}
	if c.ResyncInterval < c.PollInterval || c.ResyncInterval > 10*time.Minute {
		return fmt.Errorf("OPERATOR_UI_RESYNC_INTERVAL must be between poll interval and 10m")
	}
	if c.PGMaxConns < 1 || c.PGMaxConns > 16 {
		return fmt.Errorf("OPERATOR_UI_PG_MAX_CONNS must be between 1 and 16")
	}
	return nil
}

// envOr returns a fallback only when the environment value is empty.
func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// durationEnv parses an optional duration value.
func durationEnv(name string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return value, nil
}

// int32Env parses an optional positive connection limit.
func int32Env(name string, fallback int32) (int32, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return int32(value), nil
}
