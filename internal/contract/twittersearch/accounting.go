// Passive timeline accounting is additive evidence, never an admission or search-completion decision.
package twittersearch

import "time"

// MaxTimelineRecords bounds retained request detail independently of observed event totals.
const MaxTimelineRecords = 64

// TimelineAccounting describes one browser scan at a point in time. ScanID
// joins browser logs, HTTP evidence and workflow history; it is not a metric
// label or an account identifier. Counts are browser events, not proven X
// quota debits. A response can subsequently fail while receiving its body.
type TimelineAccounting struct {
	ScanID          string                  `json:"scan_id"`
	StartedAt       time.Time               `json:"started_at"`
	ObservedAt      time.Time               `json:"observed_at"`
	Requests        int                     `json:"requests"`
	Responses       int                     `json:"responses"`
	Finished        int                     `json:"finished"`
	Failed          int                     `json:"failed"`
	RateLimited     int                     `json:"rate_limited"`
	ServerErrors    int                     `json:"server_errors"`
	DroppedRecords  int                     `json:"dropped_records"`
	UnmatchedEvents int                     `json:"unmatched_events"`
	Records         []TimelineRequestRecord `json:"records,omitempty"`
}

// TimelineRequestRecord joins events by request identity, never URL: multiple
// physical requests can use the same URL. Offsets use a local monotonic clock
// from StartedAt. Nil offsets mean that event was not observed at snapshot,
// not a timeout. Quota values are parsed non-negative integers; missing or
// invalid values remain nil. No URL, body, cursor or credential is retained.
type TimelineRequestRecord struct {
	Sequence        int    `json:"sequence"`
	StartedMS       int64  `json:"started_ms"`
	ResponseMS      *int64 `json:"response_ms,omitempty"`
	EndedMS         *int64 `json:"ended_ms,omitempty"`
	Status          int    `json:"status,omitempty"`
	Failed          bool   `json:"failed,omitempty"`
	RateLimitLimit  *int64 `json:"rate_limit_limit,omitempty"`
	RateLimitRemain *int64 `json:"rate_limit_remaining,omitempty"`
	RateLimitReset  *int64 `json:"rate_limit_reset,omitempty"`
}
