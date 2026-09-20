// Live-watcher tests cover bounded fan-out and change-driven refresh.
package live

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeSource struct {
	mu    sync.Mutex
	token string
}

func (f *fakeSource) ChangeToken(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.token, nil
}

func (f *fakeSource) set(token string) {
	f.mu.Lock()
	f.token = token
	f.mu.Unlock()
}

// TestWatcherBroadcastsOnChange verifies a hidden database mutation reaches the UI without NATS.
func TestWatcherBroadcastsOnChange(t *testing.T) {
	source := &fakeSource{token: "a"}
	hub := NewHub()
	updates, cancelClient := hub.Subscribe()
	defer cancelClient()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go (Watcher{Source: source, Hub: hub, PollInterval: 5 * time.Millisecond, ResyncInterval: time.Second}).Run(ctx)
	time.Sleep(15 * time.Millisecond)
	source.set("b")
	select {
	case <-updates:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("database change did not broadcast")
	}
}

// TestHubCoalescesSlowClients verifies refresh production never blocks on a tab.
func TestHubCoalescesSlowClients(t *testing.T) {
	hub := NewHub()
	updates, cancel := hub.Subscribe()
	hub.Broadcast()
	hub.Broadcast()
	select {
	case <-updates:
	default:
		t.Fatal("missing coalesced refresh")
	}
	cancel()
	if hub.Count() != 0 {
		t.Fatalf("client count = %d, want 0", hub.Count())
	}
}
