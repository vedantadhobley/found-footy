// Passive accounting tests pin request identity, bounded retention, concurrency and unchanged scan decisions.
package twitter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
	"github.com/stretchr/testify/require"

	twittercontract "github.com/vedantadhobley/found-footy/internal/contract/twittersearch"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

const accountingTimelineURL = "https://x.com/i/api/graphql/fixture/SearchTimeline?variables=private-cursor"

// accountingRequest exposes only initializer-backed methods accounting is allowed to read.
type accountingRequest struct {
	playwright.Request
	url string
}

// URL supplies endpoint recognition without exposing its value in retained accounting.
func (r *accountingRequest) URL() string { return r.url }

// Failure intentionally contains sensitive text that detailed accounting must never copy.
func (*accountingRequest) Failure() error { return errors.New("private-failure-text") }

// accountingResponse contains no body/header-fetch methods; unexpected SDK I/O panics in tests.
type accountingResponse struct {
	playwright.Response
	request *accountingRequest
	status  int
	headers map[string]string
}

// URL selects only timeline evidence.
func (r *accountingResponse) URL() string { return r.request.URL() }

// Request preserves physical identity even when different requests share one URL.
func (r *accountingResponse) Request() playwright.Request { return r.request }

// Status supplies the already-observed HTTP status.
func (r *accountingResponse) Status() int { return r.status }

// Headers supplies already-observed headers without a driver call.
func (r *accountingResponse) Headers() map[string]string { return r.headers }

// accountingPage captures passive callbacks without supporting routing, scrolling or remote calls.
type accountingPage struct {
	playwright.Page
	request  func(playwright.Request)
	response func(playwright.Response)
	finished func(playwright.Request)
	failed   func(playwright.Request)
}

// OnRequest captures existing dispatch notifications only.
func (p *accountingPage) OnRequest(fn func(playwright.Request)) { p.request = fn }

// OnResponse captures existing header notifications only.
func (p *accountingPage) OnResponse(fn func(playwright.Response)) { p.response = fn }

// OnRequestFinished captures body-complete notifications only.
func (p *accountingPage) OnRequestFinished(fn func(playwright.Request)) { p.finished = fn }

// OnRequestFailed captures transport failures, not HTTP rejection status.
func (p *accountingPage) OnRequestFailed(fn func(playwright.Request)) { p.failed = fn }

// TestTimelineAccountingJoinsByIdentity separates identical URLs and out-of-order responses.
func TestTimelineAccountingJoinsByIdentity(t *testing.T) {
	c := newTimelineAccounting()
	first := &accountingRequest{url: accountingTimelineURL}
	second := &accountingRequest{url: accountingTimelineURL}
	c.request(first)
	c.request(second)
	c.response(&accountingResponse{request: second, status: http.StatusTooManyRequests,
		headers: map[string]string{"x-rate-limit-limit": "50", "x-rate-limit-remaining": "0", "x-rate-limit-reset": "2000000000"}})
	c.end(second, false) // HTTP 429 is a completed response, not a transport failure.
	c.response(&accountingResponse{request: first, status: http.StatusOK,
		headers: map[string]string{"x-rate-limit-remaining": "49"}})
	c.end(first, true) // Headers can arrive before a response-body failure.
	out := c.snapshot()
	require.NotEmpty(t, out.ScanID)
	require.False(t, out.StartedAt.IsZero())
	require.False(t, out.ObservedAt.Before(out.StartedAt))
	require.Equal(t, 2, out.Requests)
	require.Equal(t, 2, out.Responses)
	require.Equal(t, 1, out.Finished)
	require.Equal(t, 1, out.Failed)
	require.Equal(t, 1, out.RateLimited)
	require.Zero(t, out.UnmatchedEvents)
	require.Len(t, out.Records, 2)
	require.Equal(t, 1, out.Records[0].Sequence)
	require.Equal(t, 200, out.Records[0].Status)
	require.True(t, out.Records[0].Failed)
	require.Equal(t, int64(49), *out.Records[0].RateLimitRemain)
	require.Equal(t, 429, out.Records[1].Status)
	require.Equal(t, int64(0), *out.Records[1].RateLimitRemain)
	require.Equal(t, int64(50), *out.Records[1].RateLimitLimit)
	require.GreaterOrEqual(t, *out.Records[0].EndedMS, *out.Records[0].ResponseMS)

	// Snapshot values must not change when the collector receives later events.
	third := &accountingRequest{url: accountingTimelineURL}
	c.request(third)
	require.Equal(t, 2, out.Requests)
	require.Len(t, out.Records, 2)
	require.Equal(t, 3, c.snapshot().Requests)
}

// TestTimelineAccountingRetainsBoundedDetail keeps exact event totals beyond the record limit.
func TestTimelineAccountingRetainsBoundedDetail(t *testing.T) {
	c := newTimelineAccounting()
	for i := 0; i < twittercontract.MaxTimelineRecords+9; i++ {
		request := &accountingRequest{url: accountingTimelineURL}
		c.request(request)
		c.response(&accountingResponse{request: request, status: http.StatusServiceUnavailable})
		c.end(request, false)
	}
	out := c.snapshot()
	require.Equal(t, twittercontract.MaxTimelineRecords+9, out.Requests)
	require.Equal(t, out.Requests, out.Responses)
	require.Equal(t, out.Requests, out.Finished)
	require.Equal(t, out.Requests, out.ServerErrors)
	require.Equal(t, 9, out.DroppedRecords)
	require.Equal(t, 18, out.UnmatchedEvents)
	require.Len(t, out.Records, twittercontract.MaxTimelineRecords)
	require.Len(t, c.indices, twittercontract.MaxTimelineRecords)
}

// TestTimelineAccountingObserverIsPassive excludes other endpoints without calling routing or request I/O.
func TestTimelineAccountingObserverIsPassive(t *testing.T) {
	page := &accountingPage{}
	collector := &searchEvidenceCollector{accounting: newTimelineAccounting()}
	collector.observe(page)
	unrelated := &accountingRequest{url: "https://x.com/home"}
	page.request(unrelated)
	page.response(&accountingResponse{request: unrelated, status: 200})
	page.finished(unrelated)
	page.failed(unrelated)
	require.Zero(t, collector.accounting.snapshot().Requests)
	require.Zero(t, collector.accounting.snapshot().Responses)
	require.Zero(t, collector.accounting.snapshot().Failed)

	request := &accountingRequest{url: accountingTimelineURL}
	page.request(request)
	page.response(&accountingResponse{request: request, status: 429,
		headers: map[string]string{"authorization": "private-token", "set-cookie": "private-cookie",
			"x-rate-limit-remaining": "0", "x-rate-limit-limit": strings.Repeat("9", 1000)}})
	page.failed(request)
	out := collector.networkSnapshot()
	require.Equal(t, 1, out.Timeline.Requests)
	require.Equal(t, 1, out.Timeline.Failed)
	require.Nil(t, out.Timeline.Records[0].RateLimitLimit)
	encoded, err := json.Marshal(out.Timeline)
	require.NoError(t, err)
	for _, private := range []string{"private-cursor", "private-token", "private-cookie", "private-failure-text", "https://"} {
		require.NotContains(t, string(encoded), private)
	}
}

// TestTimelineAccountingUnfinishedAndMissingDispatch never invents a completed or sent request.
func TestTimelineAccountingUnfinishedAndMissingDispatch(t *testing.T) {
	c := newTimelineAccounting()
	pending := &accountingRequest{url: accountingTimelineURL}
	c.request(pending)
	snapshot := c.snapshot()
	require.Nil(t, snapshot.Records[0].ResponseMS)
	require.Nil(t, snapshot.Records[0].EndedMS)
	orphan := &accountingRequest{url: accountingTimelineURL}
	c.response(&accountingResponse{request: orphan, status: 200})
	c.end(orphan, true)
	out := c.snapshot()
	require.Equal(t, 1, out.Requests)
	require.Equal(t, 1, out.Responses)
	require.Equal(t, 1, out.Failed)
	require.Equal(t, 2, out.UnmatchedEvents)
	require.Len(t, out.Records, 1)
	require.Nil(t, snapshot.Records[0].EndedMS)
}

// TestTimelineAccountingConcurrentSnapshots exercises callback/snapshot overlap under the race detector.
func TestTimelineAccountingConcurrentSnapshots(t *testing.T) {
	c := newTimelineAccounting()
	var done sync.WaitGroup
	for i := 0; i < 40; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			request := &accountingRequest{url: accountingTimelineURL}
			c.request(request)
			c.response(&accountingResponse{request: request, status: 200})
			c.end(request, false)
		}()
	}
	for i := 0; i < 40; i++ {
		_, err := json.Marshal(c.snapshot())
		require.NoError(t, err)
	}
	done.Wait()
	out := c.snapshot()
	require.Equal(t, 40, out.Requests)
	require.Equal(t, 40, out.Responses)
	require.Equal(t, 40, out.Finished)
	require.Zero(t, out.UnmatchedEvents)
}

// TestQuotaIntegerPreservesUnknown distinguishes absent/malformed evidence from a real zero allowance.
func TestQuotaIntegerPreservesUnknown(t *testing.T) {
	for _, raw := range []string{"", "missing", "-1", "9223372036854775808", "1.5"} {
		require.Nil(t, quotaInteger(raw), raw)
	}
	require.Equal(t, int64(0), *quotaInteger("0"))
	require.Equal(t, int64(50), *quotaInteger(" 50 "))
}

// accountingNavigationBrowser fails after dispatch so the failure path must retain observed work.
type accountingNavigationBrowser struct {
	fakeBrowser
	page accountingPage
}

// Navigate installs listeners before simulating dispatch, transport failure and failed page load.
func (b *accountingNavigationBrowser) Navigate(_ context.Context, _ string, _ time.Duration, observe func(playwright.Page)) (playwright.Page, error) {
	observe(&b.page)
	request := &accountingRequest{url: accountingTimelineURL}
	b.page.request(request)
	b.page.failed(request)
	return nil, errors.New("synthetic navigation failed")
}

// TestSearchAccountingSurvivesNavigationFailure retains diagnostics even without a response page or live caller.
func TestSearchAccountingSurvivesNavigationFailure(t *testing.T) {
	var summaries []*twittercontract.TimelineAccounting
	svc := NewService(&accountingNavigationBrowser{}, ServiceOptions{
		CookieFile: t.TempDir() + "/cookies.json",
		AuditEmit: func(action string, fields map[string]any) {
			if action == string(vocabulary.ActionTwitterTimelineMeasured) {
				summaries = append(summaries, fields["timeline"].(*twittercontract.TimelineAccounting))
			}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	recorder := httptest.NewRecorder()
	svc.handleSearch(recorder, httptest.NewRequest(http.MethodPost, "/search",
		strings.NewReader(`{"query":"synthetic goal"}`)).WithContext(ctx))
	require.Equal(t, http.StatusBadGateway, recorder.Code)
	var out SearchErrorBody
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &out))
	require.Equal(t, twittercontract.ResultUnknownTimeout, out.ResultState)
	require.Equal(t, "navigation_failed", out.Evidence.TimelineFailure)
	require.NotNil(t, out.Evidence.Timeline)
	require.Equal(t, 1, out.Evidence.Timeline.Requests)
	require.Equal(t, 1, out.Evidence.Timeline.Failed)
	require.Len(t, summaries, 1)
	require.Equal(t, out.Evidence.Timeline.ScanID, summaries[0].ScanID)
	require.Equal(t, out.Evidence.Timeline.Requests, summaries[0].Requests)
}

// BenchmarkTimelineAccounting measures bounded Go-only bookkeeping, not browser or network latency.
func BenchmarkTimelineAccounting(b *testing.B) {
	requests := make([]*accountingRequest, 8)
	responses := make([]*accountingResponse, len(requests))
	for i := range requests {
		requests[i] = &accountingRequest{url: accountingTimelineURL}
		responses[i] = &accountingResponse{request: requests[i], status: 200,
			headers: map[string]string{"x-rate-limit-limit": "50", "x-rate-limit-remaining": "40"}}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := newTimelineAccounting()
		for j, request := range requests {
			c.request(request)
			c.response(responses[j])
			c.end(request, false)
		}
		_ = c.snapshot()
	}
}
