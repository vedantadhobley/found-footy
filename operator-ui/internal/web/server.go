// Package web serves the removable operator UI, its read API, SSE, and media proxy.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy-operator-ui/internal/live"
	"github.com/vedantadhobley/found-footy-operator-ui/internal/media"
	"github.com/vedantadhobley/found-footy-operator-ui/internal/model"
	"github.com/vedantadhobley/found-footy-operator-ui/internal/store"
)

//go:embed static/*
var staticFiles embed.FS

// Repository is the UI's read-only projection boundary.
type Repository interface {
	ListFixtures(context.Context, model.ListOptions) (model.FixtureList, error)
	ListEvents(context.Context, model.ListOptions) (model.EventList, error)
	EventDetail(context.Context, string) (model.EventDetail, error)
	MediaObject(context.Context, string) (model.MediaObject, error)
}

// ObjectReader is the UI's GET-only Garage boundary.
type ObjectReader interface {
	Open(context.Context, string, string, string) (media.Object, error)
}

// Server owns HTTP routing without depending on the Found Footy public API.
type Server struct {
	repo        Repository
	objects     ObjectReader
	hub         *live.Hub
	environment string
	log         *slog.Logger
	handler     http.Handler
}

// New builds the complete operator HTTP surface.
func New(repo Repository, objects ObjectReader, hub *live.Hub, environment string, log *slog.Logger) (*Server, error) {
	assets, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, fmt.Errorf("open embedded assets: %w", err)
	}
	s := &Server{repo: repo, objects: objects, hub: hub, environment: environment, log: log}
	mux := http.NewServeMux()
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", cacheAssets(http.FileServer(http.FS(assets)))))
	mux.HandleFunc("GET /", s.index)
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/fixtures", s.listFixtures)
	mux.HandleFunc("GET /api/events", s.listEvents)
	mux.HandleFunc("GET /api/events/{event_id}", s.eventDetail)
	mux.HandleFunc("GET /stream", s.stream)
	mux.HandleFunc("GET /media/{asset_id}", s.media)
	mux.HandleFunc("HEAD /media/{asset_id}", s.media)
	s.handler = securityHeaders(requestLog(log, mux))
	return s, nil
}

// listFixtures returns bounded fixture summaries with complete nested event
// membership. Filters choose fixtures; they never remove sibling events.
func (s *Server) listFixtures(w http.ResponseWriter, r *http.Request) {
	limit := boundedInt(r.URL.Query().Get("limit"), 50, 1, 100)
	offset := boundedInt(r.URL.Query().Get("offset"), 0, 0, 100_000)
	eventType := r.URL.Query().Get("type")
	switch eventType {
	case "", "goal", "card", "missed penalty":
	default:
		writeError(w, http.StatusBadRequest, "invalid event type")
		return
	}
	result, err := s.repo.ListFixtures(r.Context(), model.ListOptions{
		Query: strings.TrimSpace(r.URL.Query().Get("q")), EventType: eventType,
		OnlyWithAssets: r.URL.Query().Get("with_assets") == "true", Limit: limit, Offset: offset,
	})
	if err != nil {
		s.log.Error("list fixtures failed", "error", err)
		writeError(w, http.StatusInternalServerError, "database query failed")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// index serves the single embedded application document.
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	body, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "embedded UI unavailable", http.StatusInternalServerError)
		return
	}
	body = []byte(strings.ReplaceAll(string(body), "{{ENVIRONMENT}}", s.environment))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// health reports process readiness; startup already proved DB and Garage access.
func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}

// listEvents returns bounded event summaries.
func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	limit := boundedInt(r.URL.Query().Get("limit"), 100, 1, 250)
	offset := boundedInt(r.URL.Query().Get("offset"), 0, 0, 100_000)
	eventType := r.URL.Query().Get("type")
	switch eventType {
	case "", "goal", "card", "missed penalty":
	default:
		writeError(w, http.StatusBadRequest, "invalid event type")
		return
	}
	result, err := s.repo.ListEvents(r.Context(), model.ListOptions{
		Query: strings.TrimSpace(r.URL.Query().Get("q")), EventType: eventType,
		OnlyWithAssets: r.URL.Query().Get("with_assets") == "true", Limit: limit, Offset: offset,
	})
	if err != nil {
		s.log.Error("list events failed", "error", err)
		writeError(w, http.StatusInternalServerError, "database query failed")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// eventDetail returns all retained evidence for one event.
func (s *Server) eventDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("event_id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid event id")
		return
	}
	detail, err := s.repo.EventDetail(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "event not found")
		return
	}
	if err != nil {
		s.log.Error("event detail failed", "event_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "database query failed")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// stream pushes refresh invalidations; the browser always refetches authoritative state.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	updates, unsubscribe := s.hub.Subscribe()
	defer unsubscribe()
	_, _ = io.WriteString(w, "event: refresh\ndata: initial\n\n")
	flusher.Flush()
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case _, open := <-updates:
			if !open {
				return
			}
			_, _ = io.WriteString(w, "event: refresh\ndata: changed\n\n")
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = io.WriteString(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

// media proxies a database-resolved Garage object and supports browser byte ranges.
func (s *Server) media(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("asset_id")
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, http.StatusBadRequest, "invalid asset id")
		return
	}
	objectRef, err := s.repo.MediaObject(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "asset not found")
		return
	}
	if err != nil {
		s.log.Error("media lookup failed", "asset_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "database query failed")
		return
	}
	if objectRef.ReclaimedAt != nil {
		writeError(w, http.StatusGone, "media reclaimed")
		return
	}
	byteRange := r.Header.Get("Range")
	if byteRange != "" && (!strings.HasPrefix(byteRange, "bytes=") || strings.Contains(byteRange, ",")) {
		writeError(w, http.StatusRequestedRangeNotSatisfiable, "unsupported range")
		return
	}
	object, err := s.objects.Open(r.Context(), objectRef.Bucket, objectRef.Key, byteRange)
	if err != nil {
		s.log.Error("Garage read failed", "asset_id", id, "error", err)
		writeError(w, http.StatusBadGateway, "media unavailable")
		return
	}
	defer func() { _ = object.Body.Close() }()
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "private, max-age=300")
	w.Header().Set("Content-Disposition", "inline")
	if object.ContentType != "" {
		w.Header().Set("Content-Type", object.ContentType)
	} else {
		w.Header().Set("Content-Type", "video/mp4")
	}
	if object.ETag != "" {
		w.Header().Set("ETag", object.ETag)
	}
	if object.LastModified != nil {
		w.Header().Set("Last-Modified", object.LastModified.UTC().Format(http.TimeFormat))
	}
	if object.ContentRange != "" {
		w.Header().Set("Content-Range", object.ContentRange)
	}
	if object.ContentLength > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(object.ContentLength, 10))
	}
	if object.ContentRange != "" {
		w.WriteHeader(http.StatusPartialContent)
	}
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, object.Body); err != nil && !errors.Is(err, context.Canceled) {
		s.log.Debug("media stream ended", "asset_id", id, "error", err)
	}
}

// boundedInt parses a clamped nonnegative query parameter.
func boundedInt(raw string, fallback, minimum, maximum int) int {
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

// writeJSON emits one JSON response without HTML fallbacks.
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// writeError emits a stable error envelope.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// cacheAssets marks content-addressed embedded assets as privately reusable.
func cacheAssets(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, max-age=300")
		next.ServeHTTP(w, r)
	})
}

// securityHeaders applies the internal UI's browser boundary.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; media-src 'self'; style-src 'self'; script-src 'self'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// requestLog records only completed requests; it never logs query contents.
func requestLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		log.Debug("request", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(started).Milliseconds())
	})
}
