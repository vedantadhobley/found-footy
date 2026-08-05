// router_test.go — the #167a router foundation: health probe answers 200,
// unknown routes 404. Resource-handler tests land as the endpoints do.
package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vedantadhobley/found-footy/internal/api"
)

func TestRouter_Healthz(t *testing.T) {
	r := api.NewRouter(&api.Handlers{})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("body = %q, want ok", rec.Body.String())
	}
}

func TestRouter_UnknownRoute404(t *testing.T) {
	r := api.NewRouter(&api.Handlers{})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/does-not-exist", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown route = %d, want 404", rec.Code)
	}
}
