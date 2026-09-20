// Package live turns authoritative PostgreSQL watermarks into browser invalidations.
package live

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// TokenSource computes a coarse watermark over operator-visible database state.
type TokenSource interface {
	ChangeToken(context.Context) (string, error)
}

// Hub fans one refresh signal out to every connected browser.
type Hub struct {
	mu      sync.Mutex
	clients map[chan struct{}]struct{}
	count   atomic.Int64
}

// NewHub creates an empty refresh fan-out.
func NewHub() *Hub { return &Hub{clients: make(map[chan struct{}]struct{})} }

// Subscribe registers one bounded client and returns its idempotent cleanup.
func (h *Hub) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.count.Add(1)
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.clients, ch)
			h.count.Add(-1)
			close(ch)
			h.mu.Unlock()
		})
	}
}

// Count reports whether the watcher needs to query PostgreSQL.
func (h *Hub) Count() int64 { return h.count.Load() }

// Broadcast coalesces refreshes for slow clients instead of blocking ingestion.
func (h *Hub) Broadcast() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		select {
		case client <- struct{}{}:
		default:
		}
	}
}

// Watcher polls only while a browser is connected and periodically forces resync.
type Watcher struct {
	Source         TokenSource
	Hub            *Hub
	PollInterval   time.Duration
	ResyncInterval time.Duration
	OnError        func(error)
}

// Run blocks until cancellation while converting token changes into refreshes.
func (w Watcher) Run(ctx context.Context) {
	poll := time.NewTicker(w.PollInterval)
	defer poll.Stop()
	resync := time.NewTicker(w.ResyncInterval)
	defer resync.Stop()
	var prior string
	for {
		select {
		case <-ctx.Done():
			return
		case <-resync.C:
			if w.Hub.Count() > 0 {
				w.Hub.Broadcast()
			}
		case <-poll.C:
			if w.Hub.Count() == 0 {
				prior = ""
				continue
			}
			token, err := w.Source.ChangeToken(ctx)
			if err != nil {
				if w.OnError != nil {
					w.OnError(err)
				}
				continue
			}
			if prior != "" && token != prior {
				w.Hub.Broadcast()
			}
			prior = token
		}
	}
}
