// Package heartbeat is a time-based Temporal activity keepalive: a background
// ticker emits RecordHeartbeat on a fixed interval, so a long-but-progressing
// activity — a queue wait on a semaphore, a slow opaque call — is not killed
// at its HeartbeatTimeout. The per-op timeouts + StartToClose stay the real
// bounds; this only proves liveness, and it stops the instant the worker dies
// so Temporal re-dispatches fast.
//
// Convention (the rule whose absence caused #184 and the 2026-08-13 audit
// P0-1/P0-2): every activity that sets HeartbeatTimeout MUST call Keepalive at
// entry. A time-based ticker (not progress-tied) is correct here because these
// activities have no partial progress a heartbeat could reflect, and their
// no-progress waits (semaphore queue, Garage fetch) are exactly where the
// premature kills happened.
package heartbeat

import (
	"context"
	"sync"
	"time"

	"go.temporal.io/sdk/activity"
)

// Interval is the default heartbeat cadence. It MUST stay below the smallest
// HeartbeatTimeout in use (currently 30s on SearchTweets) with comfortable
// margin.
const Interval = 10 * time.Second

// Keepalive starts a background heartbeat every `interval` (<=0 uses Interval)
// until the returned stop func is called or ctx is cancelled. Idiom: call at
// activity entry and defer the stop —
//
//	defer heartbeat.Keepalive(ctx, heartbeat.Interval)()
//
// No-op outside an activity context: unit tests invoke activities directly
// with context.Background(), where RecordHeartbeat panics, so activity.IsActivity
// gates the whole thing.
func Keepalive(ctx context.Context, interval time.Duration) (stop func()) {
	if interval <= 0 {
		interval = Interval
	}
	if !activity.IsActivity(ctx) {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				activity.RecordHeartbeat(ctx)
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}
