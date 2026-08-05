// router.go — the public read-API HTTP surface (#167a foundation). Chi router
// (not Huma — see decisions.md 2026-08-04) with baseline middleware, a health
// probe for Caddy's upstream check, and the versioned /api/v1 group that the
// resource handlers (fixtures / events / videos / share) mount onto as they
// land. Handlers hang off a Handlers struct so the repos are injected once.
package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Handlers bundles the read dependencies the resource handlers need. Fields
// are added as endpoints land (#167a: fixtures/events; #167b: videos/share).
type Handlers struct {
	// repos + s3 presigner injected here as the handlers are built.
}

// NewRouter builds the API mux. Health is always present; resource routes
// mount under /api/v1 as they're implemented.
func NewRouter(h *Handlers) *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	// Liveness/readiness for Caddy's upstream check on the public port
	// (distinct from the bootstrap metrics/healthz on :8080).
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	r.Route("/api/v1", func(r chi.Router) {
		// GET /fixtures/{id}         — fixture-scope refetch      (#167a)
		// GET /events/{event_id}     — event-scope refetch        (#167a)
		// GET /fixtures              — bounded/by-state window    (#167a)
		// GET /videos/{share_id}     — 302 → presigned S3 URL     (#167b)
		_ = h
	})

	return r
}
