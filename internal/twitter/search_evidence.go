// Search-page evidence collection and unavailable-state classification.
package twitter

import (
	"net/url"
	"strings"
	"sync"

	"github.com/mxschmitt/playwright-go"

	twittercontract "github.com/vedantadhobley/found-footy/internal/contract/twittersearch"
)

const (
	maxEvidenceURLLength     = 512
	maxEvidenceTitleLength   = 160
	maxEvidenceFailureLength = 160
)

// searchEvidenceCollector observes only the X search timeline request. It
// retains status and rate headers, never bodies or request/auth headers.
type searchEvidenceCollector struct {
	mu       sync.Mutex
	evidence twittercontract.SearchEvidence
}

func (c *searchEvidenceCollector) observe(page playwright.Page) {
	page.OnResponse(func(response playwright.Response) {
		if !isSearchTimelineURL(response.URL()) {
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		c.evidence.TimelineSeen = true
		c.evidence.TimelineStatus = response.Status()
		c.evidence.TimelineFailure = ""
		headers := response.Headers()
		c.evidence.RateLimitLimit = boundedEvidence(headers["x-rate-limit-limit"], maxEvidenceFailureLength)
		c.evidence.RateLimitRemain = boundedEvidence(headers["x-rate-limit-remaining"], maxEvidenceFailureLength)
		c.evidence.RateLimitReset = boundedEvidence(headers["x-rate-limit-reset"], maxEvidenceFailureLength)
	})
	page.OnRequestFailed(func(request playwright.Request) {
		if !isSearchTimelineURL(request.URL()) {
			return
		}
		failure := "request_failed"
		if err := request.Failure(); err != nil {
			failure = err.Error()
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		c.evidence.TimelineSeen = true
		c.evidence.TimelineFailure = boundedEvidence(failure, maxEvidenceFailureLength)
	})
}

func (c *searchEvidenceCollector) snapshot(page playwright.Page) twittercontract.SearchEvidence {
	pageEvidence := twittercontract.SearchEvidence{
		FinalURL: boundedPageURL(page.URL()),
	}
	if title, err := page.Title(); err == nil {
		pageEvidence.PageTitle = boundedEvidence(title, maxEvidenceTitleLength)
	}
	pageEvidence.AppShell = locatorPresent(page,
		`[data-testid='primaryColumn'], [data-testid='SideNav_AccountSwitcher_Button']`)
	pageEvidence.EmptyState = locatorPresent(page,
		`[data-testid='emptyState'], [data-testid='empty_state']`)
	pageEvidence.ErrorState = locatorPresent(page,
		`[data-testid='error-detail'], [data-testid='errorDetail']`)
	if !pageEvidence.ErrorState {
		if body, err := page.Locator("body").InnerText(); err == nil {
			body = strings.ToLower(body)
			pageEvidence.ErrorState = strings.Contains(body, "something went wrong") ||
				strings.Contains(body, "try reloading") ||
				strings.Contains(body, "rate limit exceeded")
		}
	}
	// Copy network evidence last so a timeline response that lands while the
	// DOM signals are read is retained.
	c.mu.Lock()
	evidence := c.evidence
	c.mu.Unlock()
	evidence.FinalURL = pageEvidence.FinalURL
	evidence.PageTitle = pageEvidence.PageTitle
	evidence.AppShell = pageEvidence.AppShell
	evidence.EmptyState = pageEvidence.EmptyState
	evidence.ErrorState = pageEvidence.ErrorState
	return evidence
}

func locatorPresent(page playwright.Page, selector string) bool {
	count, err := page.Locator(selector).Count()
	return err == nil && count > 0
}

func classifyMissingFeed(evidence twittercontract.SearchEvidence) twittercontract.ResultState {
	switch {
	case evidence.ErrorState,
		evidence.TimelineFailure != "",
		evidence.TimelineStatus >= 400 && evidence.TimelineStatus <= 499,
		evidence.TimelineStatus >= 500 && evidence.TimelineStatus <= 599:
		return twittercontract.ResultUpstreamError
	case evidence.EmptyState:
		return twittercontract.ResultExplicitEmpty
	default:
		return twittercontract.ResultUnknownTimeout
	}
}

func isSearchTimelineURL(raw string) bool {
	lower := strings.ToLower(raw)
	return strings.Contains(lower, "searchtimeline") ||
		strings.Contains(lower, "/search/adaptive.json")
}

func boundedPageURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err == nil {
		// The workflow already retains the query. Keeping only route parameters
		// avoids duplicating player names in durable browser evidence.
		parsed.RawQuery = ""
		parsed.Fragment = ""
		raw = parsed.String()
	}
	return boundedEvidence(raw, maxEvidenceURLLength)
}

func boundedEvidence(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}
