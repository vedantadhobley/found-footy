// HTTP handler tests verify applied-window acknowledgements without an X session.
package twitter

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
	"github.com/stretchr/testify/require"

	twittercontract "github.com/vedantadhobley/found-footy/internal/contract/twittersearch"
)

type windowHTTPBrowser struct {
	fakeBrowser
	page *windowHTTPPage
	url  string
}

// Navigate returns an isolated synthetic page instead of contacting X.
func (b *windowHTTPBrowser) Navigate(_ context.Context, url string, _ time.Duration, _ func(playwright.Page)) (playwright.Page, error) {
	b.url = url
	return b.page, nil
}

type windowHTTPPage struct {
	windowPage
	empty bool
}

// URL keeps inline auth verification on a non-login route.
func (*windowHTTPPage) URL() string { return "https://x.com/search" }

// Title supplies bounded response evidence.
func (*windowHTTPPage) Title() (string, error) { return "Synthetic search", nil }

// Close owns no external resource in this test.
func (*windowHTTPPage) Close(...playwright.PageCloseOptions) error { return nil }

// Locator implements only the app-shell/feed evidence needed by the handler.
func (p *windowHTTPPage) Locator(selector string, _ ...playwright.PageLocatorOptions) playwright.Locator {
	count := 0
	switch selector {
	case `article[data-testid='tweet']`:
		if !p.empty {
			count = 1
		}
	case `[data-testid='primaryColumn'], [data-testid='SideNav_AccountSwitcher_Button']`:
		count = 1
	case `[data-testid='emptyState'], [data-testid='empty_state']`:
		if p.empty {
			count = 1
		}
	}
	return &windowHTTPLocator{count: count}
}

type windowLocatorBase = playwright.Locator

type windowHTTPLocator struct {
	windowLocatorBase
	count int
}

// Count returns synthetic feed/app-shell presence.
func (l *windowHTTPLocator) Count() (int, error) { return l.count, nil }

// First keeps selector waits on the same stub.
func (l *windowHTTPLocator) First() playwright.Locator { return l }

// WaitFor makes the synthetic page immediately ready.
func (*windowHTTPLocator) WaitFor(...playwright.LocatorWaitForOptions) error { return nil }

// InnerText supplies no error banner or user data.
func (*windowHTTPLocator) InnerText(...playwright.LocatorInnerTextOptions) (string, error) {
	return "", nil
}

// TestSearchHandlerEchoesAppliedWindow pins rendered and explicit-empty wire
// acknowledgements and confirms that the fixed floor never enters the X URL.
func TestSearchHandlerEchoesAppliedWindow(t *testing.T) {
	for _, empty := range []bool{false, true} {
		window := &twittercontract.SearchWindow{EarliestTweetAt: windowAnchor().Add(-3 * time.Minute)}
		browser := &windowHTTPBrowser{page: &windowHTTPPage{empty: empty,
			windowPage: windowPage{KeepTimestamps: true, Batches: windowFeed(time.Minute,
				windowTweet(1, 0), windowTweet(99, -4*time.Minute)).Batches},
		}}
		service := NewService(browser, ServiceOptions{CookieFile: t.TempDir() + "/cookies.json"})
		req := SearchRequest{Query: "player team filter:videos", Window: window}
		body, err := json.Marshal(req)
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		service.handleSearch(recorder, httptest.NewRequest(http.MethodPost, "/search", bytes.NewReader(body)))
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var out SearchResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &out))
		require.NotNil(t, out.Window)
		require.True(t, window.Equal(*out.Window))
		require.Equal(t, buildSearchURL(req.Query), browser.url)
		if empty {
			require.Equal(t, twittercontract.ResultExplicitEmpty, out.ResultState)
		} else {
			require.Equal(t, twittercontract.ResultRendered, out.ResultState)
			require.Equal(t, stopAge, out.StopReason)
			require.Len(t, out.Videos, 1)
		}
	}
}
