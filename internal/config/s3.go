// S3Config — env-driven settings for the S3-compatible blob storage adapter
// (Garage in prod, MinIO in tests; API layer is identical).
package config

import "time"

// S3Config covers the S3-compatible blob storage adapter used for video
// assets. Per decisions.md 2026-07-01, prod runs Garage (Rust,
// filesystem-backed) instead of MinIO; the adapter code doesn't care —
// both speak the S3 API.
//
// Endpoint is not tagged required at the env layer; the adapter constructor
// returns a descriptive error when a consuming binary starts without it.
type S3Config struct {
	// Endpoint is the S3 API URL. Example: http://garage:3900. Tests use a
	// testcontainers-managed S3-compatible endpoint.
	Endpoint string `env:"S3_ENDPOINT"`

	// Bucket is the single logical bucket name used for all video-asset
	// keys. Path-scheme: <fixture_id>/<asset_id> (computed by the s3
	// wrapper, never stored redundantly in Postgres).
	Bucket string `env:"S3_BUCKET"`

	// Region matters to some S3-compatible servers even when they don't
	// actually region-shard. Defaults to "garage" to satisfy Garage's
	// signature validator; harmless with MinIO.
	Region string `env:"S3_REGION" envDefault:"garage"`

	// AccessKeyID + SecretAccessKey authenticate every request.
	AccessKeyID     string `env:"S3_ACCESS_KEY_ID"`
	SecretAccessKey string `env:"S3_SECRET_ACCESS_KEY"`

	// UsePathStyle addresses buckets as <endpoint>/<bucket>/<key>
	// instead of <bucket>.<endpoint>/<key>. Required for MinIO + Garage;
	// AWS S3 supports both.
	UsePathStyle bool `env:"S3_USE_PATH_STYLE" envDefault:"true"`

	// ConnectTimeout bounds the initial connect + HEAD-bucket probe done
	// by the constructor so a dead S3 endpoint can't hang startup.
	ConnectTimeout time.Duration `env:"S3_CONNECT_TIMEOUT" envDefault:"10s"`

	// PresignedURLTTL controls how long presigned GET URLs stay valid.
	// Browsers receive these to fetch video bytes directly from S3
	// bypassing the api binary; 5 minutes is plenty for a click →
	// browser fetch, short enough to bound share-URL leak damage.
	PresignedURLTTL time.Duration `env:"S3_PRESIGNED_URL_TTL" envDefault:"5m"`
}
