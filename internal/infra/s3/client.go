// Package s3 is the S3-compatible blob storage adapter. Per
// decisions.md 2026-07-01, prod runs Garage (Rust, filesystem-backed);
// tests run MinIO via testcontainers; both speak the same API and this
// package doesn't distinguish.
//
// Phase S4.1: client wrapper with PutObject / GetObject / HeadObject /
// DeleteObject / PresignGetObject, each folding metric + log emission
// around the underlying aws-sdk-go-v2 call.
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// Client wraps *s3.Client (embedded, so every AWS SDK method is
// directly callable) with lifecycle log emissions + per-operation
// metric instrumentation on the five methods found-footy actually
// uses: Put, Get, Head, Delete, Presign. Same shape as pg.Pool /
// nats.Conn.
type Client struct {
	*awss3.Client
	presign *awss3.PresignClient
	ins     *Instruments
	bucket  string
	ttl     time.Duration
}

// New builds an S3 client from cfg, verifies the bucket is reachable
// via HeadBucket, and returns a Client bound to that single bucket.
// Bounded by cfg.ConnectTimeout so a dead endpoint can't hang binary
// startup.
//
// The caller registers cleanup with bootstrap.Deps.RegisterCloser
// ("s3", ...). S3 client itself doesn't need explicit Close (no
// persistent connection); the closer is a no-op for symmetry with
// the pg/nats adapters.
func New(ctx context.Context, cfg config.S3Config, ins *Instruments) (*Client, error) {
	if ins == nil {
		return nil, fmt.Errorf("s3.New: Instruments is required (call RegisterMetrics first)")
	}
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("s3.New: S3_ENDPOINT not set")
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("s3.New: S3_BUCKET not set")
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		),
	)
	if err != nil {
		ins.emitEvent(ctx, logging.LevelError, vocabulary.ActionS3ConnectFailed,
			"aws config load failed", logging.Err(err))
		return nil, fmt.Errorf("s3.New: load aws config: %w", err)
	}

	client := awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
		o.BaseEndpoint = &cfg.Endpoint
		o.UsePathStyle = cfg.UsePathStyle
	})

	// Probe the bucket. HeadBucket returns 200 when reachable + authorized.
	// Bounded by ConnectTimeout so a dead endpoint fails startup fast.
	probeCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if _, err := client.HeadBucket(probeCtx, &awss3.HeadBucketInput{Bucket: &cfg.Bucket}); err != nil {
		ins.emitEvent(ctx, logging.LevelError, vocabulary.ActionS3ConnectFailed,
			"bucket head probe failed",
			logging.String("bucket", cfg.Bucket),
			logging.String("endpoint", cfg.Endpoint),
			logging.Err(err),
		)
		return nil, fmt.Errorf("s3.New: head bucket %q: %w", cfg.Bucket, err)
	}

	ins.emitEvent(ctx, logging.LevelInfo, vocabulary.ActionS3Connected,
		"s3 client ready",
		logging.String("bucket", cfg.Bucket),
		logging.String("endpoint", cfg.Endpoint),
		logging.String("region", cfg.Region),
	)

	return &Client{
		Client:  client,
		presign: awss3.NewPresignClient(client),
		ins:     ins,
		bucket:  cfg.Bucket,
		ttl:     cfg.PresignedURLTTL,
	}, nil
}

// Bucket returns the bucket this client is bound to. Callers that need
// it in a URL or log line use this instead of re-reading the config.
func (c *Client) Bucket() string { return c.bucket }

// Upload PUTs body under key. contentType may be empty; when set, it's
// carried into the object metadata so browsers get the right MIME on
// direct-from-S3 fetches via presigned URL. Records upload count +
// duration + byte total; emits ActionS3Upload on success or
// ActionS3UploadFailed on error.
func (c *Client) Upload(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	start := time.Now()
	input := &awss3.PutObjectInput{
		Bucket:        &c.bucket,
		Key:           &key,
		Body:          body,
		ContentLength: &size,
	}
	if contentType != "" {
		input.ContentType = &contentType
	}

	_, err := c.Client.PutObject(ctx, input)
	elapsed := time.Since(start)
	c.ins.operationLatency.WithLabelValues("upload").Observe(elapsed.Seconds())

	if err != nil {
		c.ins.operations.WithLabelValues("upload", "failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionS3UploadFailed,
			"s3 upload failed",
			logging.String("key", key),
			logging.Int64("size_bytes", size),
			logging.Int64("elapsed_ms", elapsed.Milliseconds()),
			logging.Err(err),
		)
		return err
	}
	c.ins.operations.WithLabelValues("upload", "success").Inc()
	c.ins.bytesTransferred.WithLabelValues("upload").Add(float64(size))
	c.ins.emitEvent(ctx, logging.LevelDebug, vocabulary.ActionS3Upload,
		"s3 upload ok",
		logging.String("key", key),
		logging.Int64("size_bytes", size),
		logging.Int64("elapsed_ms", elapsed.Milliseconds()),
	)
	return nil
}

// Download GETs the object at key and returns its body reader. Caller
// is responsible for closing the reader. Emits ActionS3Download on
// success or ActionS3DownloadFailed on error. Byte transfer accounting
// happens on close since we don't know upfront how many bytes will
// actually be read.
func (c *Client) Download(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	start := time.Now()
	out, err := c.Client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: &c.bucket,
		Key:    &key,
	})
	elapsed := time.Since(start)
	c.ins.operationLatency.WithLabelValues("download").Observe(elapsed.Seconds())

	if err != nil {
		c.ins.operations.WithLabelValues("download", "failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionS3DownloadFailed,
			"s3 download failed",
			logging.String("key", key),
			logging.Int64("elapsed_ms", elapsed.Milliseconds()),
			logging.Err(err),
		)
		return nil, 0, err
	}
	var size int64
	if out.ContentLength != nil {
		size = *out.ContentLength
	}
	c.ins.operations.WithLabelValues("download", "success").Inc()
	c.ins.bytesTransferred.WithLabelValues("download").Add(float64(size))
	c.ins.emitEvent(ctx, logging.LevelDebug, vocabulary.ActionS3Download,
		"s3 download ok",
		logging.String("key", key),
		logging.Int64("size_bytes", size),
		logging.Int64("elapsed_ms", elapsed.Milliseconds()),
	)
	return out.Body, size, nil
}

// Head checks whether key exists. Returns (true, nil) if present,
// (false, nil) if missing (404), (false, err) on transport / auth
// errors. Missing objects emit ActionS3HeadMissing (distinct from
// ActionS3HeadFailed) because a legitimate 404 is a lookup outcome,
// not a failure.
func (c *Client) Head(ctx context.Context, key string) (bool, error) {
	start := time.Now()
	_, err := c.Client.HeadObject(ctx, &awss3.HeadObjectInput{
		Bucket: &c.bucket,
		Key:    &key,
	})
	elapsed := time.Since(start)
	c.ins.operationLatency.WithLabelValues("head").Observe(elapsed.Seconds())

	if err == nil {
		c.ins.operations.WithLabelValues("head", "success").Inc()
		c.ins.emitEvent(ctx, logging.LevelDebug, vocabulary.ActionS3Head,
			"s3 head ok",
			logging.String("key", key),
			logging.Int64("elapsed_ms", elapsed.Milliseconds()),
		)
		return true, nil
	}
	if isNotFound(err) {
		c.ins.operations.WithLabelValues("head", "missing").Inc()
		c.ins.emitEvent(ctx, logging.LevelDebug, vocabulary.ActionS3HeadMissing,
			"s3 head missing",
			logging.String("key", key),
			logging.Int64("elapsed_ms", elapsed.Milliseconds()),
		)
		return false, nil
	}
	c.ins.operations.WithLabelValues("head", "failure").Inc()
	c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionS3HeadFailed,
		"s3 head failed",
		logging.String("key", key),
		logging.Int64("elapsed_ms", elapsed.Milliseconds()),
		logging.Err(err),
	)
	return false, err
}

// Delete removes key. A missing key is treated as success (S3-standard
// idempotent DELETE semantic).
func (c *Client) Delete(ctx context.Context, key string) error {
	start := time.Now()
	_, err := c.Client.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: &c.bucket,
		Key:    &key,
	})
	elapsed := time.Since(start)
	c.ins.operationLatency.WithLabelValues("delete").Observe(elapsed.Seconds())

	if err != nil {
		c.ins.operations.WithLabelValues("delete", "failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionS3DeleteFailed,
			"s3 delete failed",
			logging.String("key", key),
			logging.Int64("elapsed_ms", elapsed.Milliseconds()),
			logging.Err(err),
		)
		return err
	}
	c.ins.operations.WithLabelValues("delete", "success").Inc()
	c.ins.emitEvent(ctx, logging.LevelDebug, vocabulary.ActionS3Delete,
		"s3 delete ok",
		logging.String("key", key),
		logging.Int64("elapsed_ms", elapsed.Milliseconds()),
	)
	return nil
}

// PresignGet returns a signed URL that grants temporary anonymous
// read access to the object at key. Browsers fetch video bytes via
// this URL directly from S3, bypassing the api binary. TTL is
// cfg.PresignedURLTTL.
func (c *Client) PresignGet(ctx context.Context, key string) (string, error) {
	req, err := c.presign.PresignGetObject(ctx,
		&awss3.GetObjectInput{Bucket: &c.bucket, Key: &key},
		awss3.WithPresignExpires(c.ttl),
	)
	if err != nil {
		c.ins.operations.WithLabelValues("presign", "failure").Inc()
		c.ins.emitEvent(ctx, logging.LevelWarn, vocabulary.ActionS3PresignFailed,
			"s3 presign failed",
			logging.String("key", key),
			logging.Err(err),
		)
		return "", err
	}
	c.ins.operations.WithLabelValues("presign", "success").Inc()
	c.ins.emitEvent(ctx, logging.LevelDebug, vocabulary.ActionS3Presign,
		"s3 presign ok",
		logging.String("key", key),
		logging.String("ttl", c.ttl.String()),
	)
	return req.URL, nil
}

// isNotFound returns true when err is the S3 404 "NoSuchKey" (or the
// wrapper HTTP 404). aws-sdk-go-v2 surfaces this in two possible
// shapes depending on the operation; check both.
func isNotFound(err error) bool {
	var nsk *s3types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var respErr *smithyhttp.ResponseError
	if errors.As(err, &respErr) && respErr.HTTPStatusCode() == 404 {
		return true
	}
	return false
}
