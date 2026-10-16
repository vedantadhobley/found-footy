// Timeline accounting observes existing Playwright events without routing, per-request I/O or waits.
package twitter

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mxschmitt/playwright-go"

	twittercontract "github.com/vedantadhobley/found-footy/internal/contract/twittersearch"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// timelineAccountingCollector retains at most MaxTimelineRecords SDK request
// references. Totals continue beyond the detail cap; unmatched events make
// truncation or missing request observations explicit rather than inventing IDs.
type timelineAccountingCollector struct {
	mu      sync.Mutex
	started time.Time
	totals  twittercontract.TimelineAccounting
	indices map[playwright.Request]int
}

// newTimelineAccounting creates a scan identity independent of query and account.
func newTimelineAccounting() *timelineAccountingCollector {
	started := time.Now()
	return &timelineAccountingCollector{
		started: started,
		totals:  twittercontract.TimelineAccounting{ScanID: uuid.NewString(), StartedAt: started.UTC()},
		indices: make(map[playwright.Request]int),
	}
}

// request observes the existing dispatch event, not proof that X received or charged it.
func (c *timelineAccountingCollector) request(request playwright.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.indices[request]; exists {
		return
	}
	c.totals.Requests++
	if len(c.totals.Records) >= twittercontract.MaxTimelineRecords {
		c.totals.DroppedRecords++
		return
	}
	c.indices[request] = len(c.totals.Records)
	c.totals.Records = append(c.totals.Records, twittercontract.TimelineRequestRecord{
		Sequence: c.totals.Requests, StartedMS: time.Since(c.started).Milliseconds(),
	})
}

// response reads only initializer-backed SDK values; it never requests a body or extra headers.
func (c *timelineAccountingCollector) response(response playwright.Response) {
	request := response.Request()
	status, headers := response.Status(), response.Headers()
	c.mu.Lock()
	defer c.mu.Unlock()
	index, matched := c.indices[request]
	if matched && c.totals.Records[index].ResponseMS != nil {
		return
	}
	c.totals.Responses++
	if status == 429 {
		c.totals.RateLimited++
	}
	if status >= 500 && status <= 599 {
		c.totals.ServerErrors++
	}
	if !matched {
		c.totals.UnmatchedEvents++
		return
	}
	ms := time.Since(c.started).Milliseconds()
	record := &c.totals.Records[index]
	record.ResponseMS, record.Status = &ms, status
	record.RateLimitLimit = quotaInteger(headers["x-rate-limit-limit"])
	record.RateLimitRemain = quotaInteger(headers["x-rate-limit-remaining"])
	record.RateLimitReset = quotaInteger(headers["x-rate-limit-reset"])
}

// end distinguishes response-body completion from transport failure, including failure after headers.
func (c *timelineAccountingCollector) end(request playwright.Request, failed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	index, matched := c.indices[request]
	if matched && c.totals.Records[index].EndedMS != nil {
		return
	}
	if failed {
		c.totals.Failed++
	} else {
		c.totals.Finished++
	}
	if !matched {
		c.totals.UnmatchedEvents++
		return
	}
	ms := time.Since(c.started).Milliseconds()
	record := &c.totals.Records[index]
	record.EndedMS, record.Failed = &ms, failed
}

// snapshot copies bounded state without waiting for outstanding requests or changing search behavior.
func (c *timelineAccountingCollector) snapshot() *twittercontract.TimelineAccounting {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.totals
	out.ObservedAt = time.Now().UTC()
	out.Records = append([]twittercontract.TimelineRequestRecord(nil), c.totals.Records...)
	return &out
}

// quotaInteger preserves missing, malformed, negative and overflowing header values as unknown.
func quotaInteger(raw string) *int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 0 {
		return nil
	}
	return &value
}

// emitTimelineAccounting keeps diagnostics when the caller disconnects. The browser summary
// is authoritative for this snapshot; client/history copies share ScanID and must not be added again.
func (s *Service) emitTimelineAccounting(c *timelineAccountingCollector) {
	if s.auditEmit != nil {
		s.auditEmit(string(vocabulary.ActionTwitterTimelineMeasured), map[string]any{
			"module": vocabulary.ModuleInfraTwitter, "timeline": c.snapshot(),
		})
	}
}
