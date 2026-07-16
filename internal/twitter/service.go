// Twitter service state machine + HTTP surface. Wraps a Browser and
// exposes /health + /status. T/a scope: prove Playwright-Go + Firefox
// works in Docker. Search + auth endpoints land in T/b + T/c.
package twitter

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// State reports the current service condition. Transitions:
//
//	starting → healthy (cookies loaded, session verified)
//	starting → unauthenticated (cookies missing/expired)
//	healthy → unauthenticated (auth expired mid-run)
type State string

const (
	StateStarting        State = "starting"
	StateHealthy         State = "healthy"
	StateUnauthenticated State = "unauthenticated"
	StateFailed          State = "failed" // browser dead / unrecoverable
)

// Service is the state-machine wrapper around a *Browser. Exposes
// HTTP handlers via ServeMux registration; thread-safe via mu.
type Service struct {
	browser *Browser

	mu           sync.RWMutex
	state        State
	stateReason  string
	lastAuthCheck time.Time
	startedAt    time.Time
}

// NewService constructs a Service in StateStarting. The browser
// argument must already be initialized (NewBrowser succeeded).
func NewService(b *Browser) *Service {
	return &Service{
		browser:   b,
		state:     StateStarting,
		startedAt: time.Now().UTC(),
	}
}

// SetState transitions the service to a new state with an
// optional human-readable reason (surfaced in /status responses).
// Concurrent-safe.
func (s *Service) SetState(newState State, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = newState
	s.stateReason = reason
	if newState == StateHealthy || newState == StateUnauthenticated {
		s.lastAuthCheck = time.Now().UTC()
	}
}

// State returns the current state + reason without holding the lock
// for callers.
func (s *Service) State() (State, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state, s.stateReason
}

// RegisterHandlers installs /health and /status on mux.
//
// /health: HTTP 200 iff state == healthy. Suitable for orchestrator
// readiness probes.
//
// /status: HTTP 200 always with a JSON snapshot of the internal
// state. Used by the scaler + Discovery routing to distinguish
// unauthenticated-but-alive from truly failed.
func (s *Service) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/status", s.handleStatus)
}

func (s *Service) handleHealth(w http.ResponseWriter, r *http.Request) {
	state, reason := s.State()
	if state == StateHealthy {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "healthy",
		})
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": string(state),
		"reason": reason,
	})
}

func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"state":            string(s.state),
		"reason":           s.stateReason,
		"started_at":       s.startedAt,
		"last_auth_check":  s.lastAuthCheck,
	})
}
