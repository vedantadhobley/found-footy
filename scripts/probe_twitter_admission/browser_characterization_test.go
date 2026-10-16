// Test-only browser fakes characterize current partial-scan responses without changing production behavior.
package admissionprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
	"github.com/stretchr/testify/require"

	twittercontract "github.com/vedantadhobley/found-footy/internal/contract/twittersearch"
	"github.com/vedantadhobley/found-footy/internal/twitter"
)

// partialBrowser injects a page through the real Service's existing browser seam.
type partialBrowser struct {
	page *partialPage
}

// Done owns no browser child in this offline characterization.
func (*partialBrowser) Done() <-chan struct{} { return nil }

// VerifySession does not perform account authentication.
func (*partialBrowser) VerifySession(context.Context, time.Duration) error { return nil }

// ReplaceCookies accepts no real credentials or shared cookie file.
func (*partialBrowser) ReplaceCookies([]twitter.Cookie) error { return nil }

// GetCookies returns no account data; real backup validation remains unchanged.
func (*partialBrowser) GetCookies() ([]twitter.Cookie, error) { return nil, nil }

// Navigate installs the real response observer before simulating the initial timeline.
func (b *partialBrowser) Navigate(_ context.Context, _ string, _ time.Duration, observe func(playwright.Page)) (playwright.Page, error) {
	observe(b.page)
	request := &partialRequest{}
	b.page.request(request)
	b.page.response(&partialResponse{status: http.StatusOK, request: request})
	b.page.finished(request)
	return b.page, nil
}

// partialPage implements only the browser API exercised by the current search handler.
type partialPage struct {
	playwright.Page
	response   func(playwright.Response)
	request    func(playwright.Request)
	finished   func(playwright.Request)
	status     int
	extracted  bool
	closed     bool
	newestTime string
}

// URL remains a synthetic authenticated route, without navigating to X.
func (*partialPage) URL() string { return "https://x.com/search" }

// Title supplies bounded page evidence only.
func (*partialPage) Title() (string, error) { return "Offline partial-scan fixture", nil }

// Close records the existing per-call page lifetime.
func (p *partialPage) Close(...playwright.PageCloseOptions) error { p.closed = true; return nil }

// OnResponse captures the service's real evidence listener.
func (p *partialPage) OnResponse(fn func(playwright.Response)) { p.response = fn }

// OnRequest captures passive accounting without routing or changing the page.
func (p *partialPage) OnRequest(fn func(playwright.Request)) { p.request = fn }

// OnRequestFinished captures response-body completion independently of status.
func (p *partialPage) OnRequestFinished(fn func(playwright.Request)) { p.finished = fn }

// OnRequestFailed installs no transport failure for this response-status scenario.
func (*partialPage) OnRequestFailed(func(playwright.Request)) {}

// Locator supplies a rendered article and an otherwise healthy app shell.
func (*partialPage) Locator(selector string, _ ...playwright.PageLocatorOptions) playwright.Locator {
	count := 0
	if selector == `article[data-testid='tweet']` || strings.Contains(selector, "primaryColumn") {
		count = 1
	}
	return &partialLocator{count: count}
}

// Evaluate returns one candidate, then makes pagination return the selected status.
func (p *partialPage) Evaluate(expression string, _ ...any) (any, error) {
	if strings.Contains(expression, "window.scrollBy") {
		request := &partialRequest{}
		p.request(request)
		p.response(&partialResponse{status: p.status, request: request})
		p.finished(request)
		return nil, nil
	}
	if p.extracted {
		return []any{}, nil
	}
	p.extracted = true
	return []any{map[string]any{
		"tweet_url": "https://x.com/fixture/status/1790000000000000000",
		"text":      "synthetic candidate", "datetime": p.newestTime,
		"has_video": true, "duration_seconds": 20,
	}}, nil
}

// partialLocatorBase avoids shadowing the SDK's Locator method with a field.
type partialLocatorBase = playwright.Locator

// partialLocator keeps all synthetic selector waits immediate.
type partialLocator struct {
	partialLocatorBase
	count int
}

// Count returns only the fixture's configured DOM presence.
func (l *partialLocator) Count() (int, error) { return l.count, nil }

// First uses the same fixture locator.
func (l *partialLocator) First() playwright.Locator { return l }

// WaitFor makes deterministic local fixtures ready without a browser timer.
func (*partialLocator) WaitFor(...playwright.LocatorWaitForOptions) error { return nil }

// InnerText contains no synthetic error banner or private page body.
func (*partialLocator) InnerText(...playwright.LocatorInnerTextOptions) (string, error) {
	return "", nil
}

// partialResponse exposes status and public quota headers through the real collector.
type partialResponse struct {
	playwright.Response
	status  int
	request playwright.Request
}

// partialRequest gives the synthetic request its own identity without any network I/O.
type partialRequest struct{ playwright.Request }

// URL selects timeline-only passive observation.
func (*partialRequest) URL() string { return "https://x.com/i/api/graphql/fixture/SearchTimeline" }

// Request joins headers to the same physical request event.
func (r *partialResponse) Request() playwright.Request { return r.request }

// URL matches the current timeline recognizer, not a real network request.
func (*partialResponse) URL() string { return "https://x.com/i/api/graphql/fixture/SearchTimeline" }

// Status exposes the configured initial or pagination response.
func (r *partialResponse) Status() int { return r.status }

// Headers supplies only bounded public evidence, never credentials.
func (r *partialResponse) Headers() map[string]string {
	remaining := "49"
	if r.status == http.StatusTooManyRequests {
		remaining = "0"
	}
	return map[string]string{"x-rate-limit-limit": "50", "x-rate-limit-remaining": remaining,
		"x-rate-limit-reset": "2000000000"}
}

// TestCurrentRenderedPaginationFailure characterizes a defect, not the desired contract.
// Replace these assertions with target partial-scan assertions when production code changes.
func TestCurrentRenderedPaginationFailure(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			page := &partialPage{status: status, newestTime: time.Now().UTC().Format(time.RFC3339)}
			svc := twitter.NewService(&partialBrowser{page: page}, twitter.ServiceOptions{
				CookieFile: t.TempDir() + "/cookies.json", MaxScrolls: 3,
				ScrollJitterMin: time.Nanosecond, ScrollJitterMax: time.Nanosecond,
			})
			mux := http.NewServeMux()
			svc.RegisterHandlers(mux)
			request := twittercontract.SearchRequest{Query: "fixture filter:videos",
				Window: &twittercontract.SearchWindow{EarliestTweetAt: time.Now().UTC().Add(-3 * time.Minute)}}
			body, err := json.Marshal(request)
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body)))
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			var result twittercontract.SearchResponse
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
			require.Equal(t, status, result.Evidence.TimelineStatus)
			require.NotNil(t, result.Evidence.Timeline)
			require.Equal(t, 2, result.Evidence.Timeline.Requests)
			require.Equal(t, 2, result.Evidence.Timeline.Responses)
			require.Equal(t, 2, result.Evidence.Timeline.Finished)
			require.Len(t, result.Evidence.Timeline.Records, 2)
			require.Equal(t, status, result.Evidence.Timeline.Records[1].Status)
			require.Equal(t, twittercontract.ResultRendered, result.ResultState)
			require.True(t, result.ResultState.Usable())
			require.Len(t, result.Videos, 1)
			require.True(t, page.closed)
			t.Logf("current_result=%s usable=%t timeline_status=%d candidates=%d page_closed=%t",
				result.ResultState, result.ResultState.Usable(), result.Evidence.TimelineStatus, len(result.Videos), page.closed)
		})
	}
}
