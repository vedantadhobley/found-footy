// Package media provides the operator UI's GET-only Garage adapter.
package media

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/vedantadhobley/found-footy-operator-ui/internal/config"
)

// Object is one open Garage response. The caller must close Body.
type Object struct {
	Body          io.ReadCloser
	ContentLength int64
	ContentRange  string
	ContentType   string
	ETag          string
	LastModified  *time.Time
}

// Store deliberately exposes only HEAD and GET behavior.
type Store struct {
	client *awss3.Client
}

// New constructs a path-style S3 client and verifies bucket readability.
func New(ctx context.Context, cfg config.Config) (*Store, error) {
	client := awss3.NewFromConfig(aws.Config{
		Region:      cfg.S3Region,
		Credentials: credentials.NewStaticCredentialsProvider(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		// Bound connection and response-header establishment without imposing an
		// overall deadline on a browser that is streaming or seeking a long clip.
		HTTPClient: &http.Client{Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
			IdleConnTimeout:       90 * time.Second,
		}},
	}, func(options *awss3.Options) {
		options.BaseEndpoint = aws.String(cfg.S3Endpoint)
		options.UsePathStyle = cfg.S3PathStyle
	})
	if _, err := client.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String(cfg.S3Bucket)}); err != nil {
		return nil, fmt.Errorf("head Garage bucket: %w", err)
	}
	return &Store{client: client}, nil
}

// Open streams one database-resolved object and optionally honors one byte range.
func (s *Store) Open(ctx context.Context, bucket, key, byteRange string) (Object, error) {
	input := &awss3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}
	if byteRange != "" {
		input.Range = aws.String(byteRange)
	}
	out, err := s.client.GetObject(ctx, input)
	if err != nil {
		return Object{}, fmt.Errorf("get Garage object: %w", err)
	}
	object := Object{Body: out.Body, ContentLength: aws.ToInt64(out.ContentLength), LastModified: out.LastModified}
	object.ContentRange = aws.ToString(out.ContentRange)
	object.ContentType = aws.ToString(out.ContentType)
	object.ETag = aws.ToString(out.ETag)
	return object, nil
}
