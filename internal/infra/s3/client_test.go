// Integration + unit tests for the S3 client wrapper (MinIO in tests,
// Garage in prod; API is identical).
package s3_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"

	"github.com/vedantadhobley/found-footy/internal/config"
	"github.com/vedantadhobley/found-footy/internal/infra/s3"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// minio:RELEASE.2025-04-08T15-41-24Z — recent, small, S3-compatible.
// Same wire protocol as Garage; adapter code doesn't distinguish.
const minioImage = "minio/minio:RELEASE.2025-04-08T15-41-24Z"
const testBucket = "found-footy-test"
const testUser = "minioadmin"
const testPass = "minioadmin"

type testFixture struct {
	reg *metrics.Registry
	log *logging.TestEmitter
	ins *s3.Instruments
}

func newTestFixture() *testFixture {
	reg := metrics.New()
	log := &logging.TestEmitter{}
	ins := s3.RegisterMetrics(reg, log)
	return &testFixture{reg: reg, log: log, ins: ins}
}

// runTestMinIO spins up an ephemeral MinIO server + creates testBucket
// so s3.New's HeadBucket probe finds it. Returns the endpoint URL.
func runTestMinIO(ctx context.Context, t *testing.T) string {
	t.Helper()

	mc, err := tcminio.Run(ctx, minioImage,
		tcminio.WithUsername(testUser),
		tcminio.WithPassword(testPass),
	)
	if err != nil {
		t.Fatalf("start minio container: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(mc); err != nil {
			t.Logf("terminate container: %v", err)
		}
	})

	endpoint, err := mc.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	url := "http://" + endpoint

	// Pre-create the bucket via the raw AWS SDK. s3.New's HeadBucket
	// probe requires it to exist.
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(testUser, testPass, ""),
		),
	)
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	raw := awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
		o.BaseEndpoint = &url
		o.UsePathStyle = true
	})
	if _, err := raw.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: strPtr(testBucket)}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	return url
}

func strPtr(s string) *string { return &s }

func scrapeMetrics(t *testing.T, reg *metrics.Registry) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	reg.Handler().ServeHTTP(w, req)
	body, _ := io.ReadAll(w.Result().Body)
	return string(body)
}

func newTestClient(t *testing.T, ctx context.Context, endpoint string, fx *testFixture) *s3.Client {
	t.Helper()
	c, err := s3.New(ctx, config.S3Config{
		Endpoint:        endpoint,
		Bucket:          testBucket,
		Region:          "us-east-1",
		AccessKeyID:     testUser,
		SecretAccessKey: testPass,
		UsePathStyle:    true,
		ConnectTimeout:  10 * time.Second,
		PresignedURLTTL: 5 * time.Minute,
	}, fx.ins)
	if err != nil {
		t.Fatalf("s3.New: %v", err)
	}
	return c
}

// TestClient_UploadHeadDownloadDelete walks the full object lifecycle:
// upload a small payload, head confirms exists, download returns the
// same bytes, delete removes it, subsequent head returns missing.
func TestClient_UploadHeadDownloadDelete(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	endpoint := runTestMinIO(ctx, t)
	fx := newTestFixture()
	c := newTestClient(t, ctx, endpoint, fx)

	if !fx.log.HasAction(vocabulary.ModuleInfraS3, vocabulary.ActionS3Connected) {
		t.Errorf("expected ActionS3Connected; captured=%+v", fx.log.Snapshot())
	}

	const key = "test/hello.mp4"
	payload := []byte("hello, footy")

	if err := c.Upload(ctx, key, bytes.NewReader(payload), int64(len(payload)), "video/mp4"); err != nil {
		t.Fatalf("upload: %v", err)
	}

	exists, err := c.Head(ctx, key)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if !exists {
		t.Error("head returned exists=false after upload")
	}

	body, size, err := c.Download(ctx, key)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer func() { _ = body.Close() }()
	got, _ := io.ReadAll(body)
	if !bytes.Equal(got, payload) {
		t.Errorf("download body = %q, want %q", got, payload)
	}
	if size != int64(len(payload)) {
		t.Errorf("download size = %d, want %d", size, len(payload))
	}

	if err := c.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}

	exists, err = c.Head(ctx, key)
	if err != nil {
		t.Fatalf("head after delete: %v", err)
	}
	if exists {
		t.Error("head returned exists=true after delete")
	}
	if !fx.log.HasAction(vocabulary.ModuleInfraS3, vocabulary.ActionS3HeadMissing) {
		t.Errorf("expected ActionS3HeadMissing after delete; captured=%+v", fx.log.Snapshot())
	}
}

// TestClient_Copy verifies the server-side staging→assets promote: the dest
// exists with identical bytes and the source is left intact.
func TestClient_Copy(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	endpoint := runTestMinIO(ctx, t)
	fx := newTestFixture()
	c := newTestClient(t, ctx, endpoint, fx)

	const src = "staging/9100/evt/tweet.mp4"
	const dst = "assets/9100/evt/asset-uuid.mp4"
	payload := []byte("goal clip bytes")
	if err := c.Upload(ctx, src, bytes.NewReader(payload), int64(len(payload)), "video/mp4"); err != nil {
		t.Fatalf("upload src: %v", err)
	}

	if err := c.Copy(ctx, src, dst); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if !fx.log.HasAction(vocabulary.ModuleInfraS3, vocabulary.ActionS3Copy) {
		t.Errorf("expected ActionS3Copy; captured=%+v", fx.log.Snapshot())
	}

	// Dest has the bytes.
	body, size, err := c.Download(ctx, dst)
	if err != nil {
		t.Fatalf("download dst: %v", err)
	}
	defer func() { _ = body.Close() }()
	got, _ := io.ReadAll(body)
	if !bytes.Equal(got, payload) || size != int64(len(payload)) {
		t.Errorf("dst body/size = %q/%d, want %q/%d", got, size, payload, len(payload))
	}

	// Source is untouched (copy, not move).
	if exists, _ := c.Head(ctx, src); !exists {
		t.Error("source should still exist after copy")
	}
}

// TestClient_MetricsCoverage verifies every operation contributes to
// its expected counter + histogram + byte-transfer series.
func TestClient_MetricsCoverage(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	endpoint := runTestMinIO(ctx, t)
	fx := newTestFixture()
	c := newTestClient(t, ctx, endpoint, fx)

	payload := []byte("metric coverage payload")
	_ = c.Upload(ctx, "k1", bytes.NewReader(payload), int64(len(payload)), "")
	_, _ = c.Head(ctx, "k1")
	body, _, _ := c.Download(ctx, "k1")
	if body != nil {
		_, _ = io.Copy(io.Discard, body)
		_ = body.Close()
	}
	_, _ = c.PresignGet(ctx, "k1")
	_, _ = c.Head(ctx, "does-not-exist")
	_ = c.Delete(ctx, "k1")

	scrape := scrapeMetrics(t, fx.reg)
	wantContains := []string{
		`found_footy_s3_operations_total{op="upload",outcome="success"} 1`,
		`found_footy_s3_operations_total{op="head",outcome="success"} 1`,
		`found_footy_s3_operations_total{op="head",outcome="missing"} 1`,
		`found_footy_s3_operations_total{op="download",outcome="success"} 1`,
		`found_footy_s3_operations_total{op="presign",outcome="success"} 1`,
		`found_footy_s3_operations_total{op="delete",outcome="success"} 1`,
		`found_footy_s3_operation_duration_seconds_count{op="upload"} 1`,
		`found_footy_s3_bytes_transferred_total{direction="upload"}`,
		`found_footy_s3_bytes_transferred_total{direction="download"}`,
	}
	for _, want := range wantContains {
		if !strings.Contains(scrape, want) {
			t.Errorf("scrape missing %q; got:\n%s", want, scrape)
		}
	}
}

// TestPresignGet_ReturnsUsableURL confirms the presigned URL actually
// works against the underlying MinIO — a browser fetching this URL
// should receive the object bytes without needing credentials.
func TestPresignGet_ReturnsUsableURL(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	endpoint := runTestMinIO(ctx, t)
	fx := newTestFixture()
	c := newTestClient(t, ctx, endpoint, fx)

	payload := []byte("presigned-url body")
	if err := c.Upload(ctx, "presign/target", bytes.NewReader(payload), int64(len(payload)), "video/mp4"); err != nil {
		t.Fatalf("upload: %v", err)
	}

	url, err := c.PresignGet(ctx, "presign/target")
	if err != nil {
		t.Fatalf("presign: %v", err)
	}

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("http.Get(presigned url): %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("presigned GET status = %d, want 200", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, payload) {
		t.Errorf("presigned body = %q, want %q", got, payload)
	}
}

// TestNew_NilInstruments_Errors — same fast-fail guard as pg + nats.
func TestNew_NilInstruments_Errors(t *testing.T) {
	_, err := s3.New(context.Background(),
		config.S3Config{Endpoint: "http://x:9000", Bucket: "b"}, nil)
	if err == nil {
		t.Fatal("expected error for nil Instruments, got nil")
	}
}

// TestNew_MissingEndpoint_Errors — no endpoint = no client.
func TestNew_MissingEndpoint_Errors(t *testing.T) {
	fx := newTestFixture()
	_, err := s3.New(context.Background(),
		config.S3Config{Endpoint: "", Bucket: "b"}, fx.ins)
	if err == nil {
		t.Fatal("expected error for empty S3_ENDPOINT, got nil")
	}
}

// TestNew_MissingBucket_Errors — no bucket = no client (Bucket is
// required to scope operations).
func TestNew_MissingBucket_Errors(t *testing.T) {
	fx := newTestFixture()
	_, err := s3.New(context.Background(),
		config.S3Config{Endpoint: "http://x:9000", Bucket: ""}, fx.ins)
	if err == nil {
		t.Fatal("expected error for empty S3_BUCKET, got nil")
	}
}

// TestNew_UnreachableHost_ErrorsQuickly bounds startup delay via
// ConnectTimeout — same guard as pg + nats.
func TestNew_UnreachableHost_ErrorsQuickly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration-ish test skipped in -short mode")
	}

	fx := newTestFixture()
	start := time.Now()
	_, err := s3.New(context.Background(), config.S3Config{
		Endpoint:        "http://192.0.2.1:9000",
		Bucket:          testBucket,
		Region:          "us-east-1",
		AccessKeyID:     "x",
		SecretAccessKey: "x",
		UsePathStyle:    true,
		ConnectTimeout:  2 * time.Second,
	}, fx.ins)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error for unreachable host, got nil")
	}
	if elapsed > 8*time.Second {
		t.Errorf("New took %v, want ≤ 8s (timeout was 2s; SDK adds retries)", elapsed)
	}
	if !fx.log.HasAction(vocabulary.ModuleInfraS3, vocabulary.ActionS3ConnectFailed) {
		t.Errorf("expected ActionS3ConnectFailed; captured=%+v", fx.log.Snapshot())
	}
}
