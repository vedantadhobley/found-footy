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

// NewRouter builds the API mux. Health is always present; the /api/v1 resource
// routes (see handlers.go) mount under the versioned group.
func NewRouter(h *Handlers) *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)

	// Liveness/readiness for Caddy's upstream check on the public port
	// (distinct from the bootstrap metrics/healthz on :8080).
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/fixtures", h.GetFixtures)            // window (no params) OR ?ids= batch refetch
		r.Get("/search", h.Search)                   // free-text: competition / team / scorer / assist names
		r.Get("/events", h.GetEvents)                // batch: ?ids=uuid,uuid
		r.Get("/videos/{share_id}", h.RedirectVideo) // 302 → presigned Garage URL
	})

	return r
}
