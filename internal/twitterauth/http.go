// http.go exposes the raw-Firefox capture service's read-only health surface.
package twitterauth

import (
	"encoding/json"
	"net/http"
	"time"
)

// Handler returns the login terminal's read-only HTTP API.
func (c *Capturer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", c.handleHealth)
	mux.HandleFunc("/status", c.handleStatus)
	return mux
}

func (c *Capturer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	status := c.Status()
	w.Header().Set("Content-Type", "application/json")
	if status.State != StateReady || !status.AuthExpiresAt.After(time.Now().UTC()) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"state":  status.State,
		"reason": status.Reason,
	})
}

func (c *Capturer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(c.Status())
}
