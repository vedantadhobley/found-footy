// handlers.go — the read API's resource handlers + the narrow read ports they
// depend on (#167). The ports are read-only subsets satisfied by the pg repos
// and the s3 client, so handler tests use lightweight fakes rather than a real
// DB. Assembly is fixture → events → live videos; the redirect resolves a share
// through the supersede chain. Response shapes live in dto.go.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/domain/video"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/vocabulary"
)

// ─── read ports (satisfied by the pg repos + s3 client) ─────────────────────

// FixtureReader is the fixture read surface the API needs.
type FixtureReader interface {
	ListPublicWindow(ctx context.Context, completedFixtureDates int) ([]*fixture.Fixture, error)
	GetByIDs(ctx context.Context, ids []int64) ([]*fixture.Fixture, error)
	// SearchPublicFixtures returns fixtures whose competition, team, or event
	// scorer/assist names match the free-text query — the /search backing,
	// capped at limit.
	SearchPublicFixtures(ctx context.Context, q string, limit, completedFixtureDates int) ([]*fixture.Fixture, error)
}

// EventReader is the event read surface the API needs.
type EventReader interface {
	GetByIDs(ctx context.Context, ids []uuid.UUID) ([]*event.Event, error)
	ListByFixtures(ctx context.Context, fixtureIDs []int64) ([]*event.Event, error)
	// DiscoveryComplete returns the subset of eventIDs whose discovery workflow
	// has finished — the signal event.DerivePhase uses to separate `searching`
	// from `complete`. Batched to avoid an N+1 across a fixture's events.
	DiscoveryComplete(ctx context.Context, eventIDs []uuid.UUID) (map[uuid.UUID]bool, error)
}

// VideoReader is the video read surface: the live-clip list + share resolution.
type VideoReader interface {
	ListLiveForEvents(ctx context.Context, eventIDs []uuid.UUID) (map[uuid.UUID][]video.LiveClip, error)
	ResolveShare(ctx context.Context, id string) (video.ResolvedShare, error)
}

// Presigner mints a short-lived GET URL for a Garage object key.
type Presigner interface {
	PresignGet(ctx context.Context, key string) (string, error)
}

// Handlers bundles the read dependencies. Constructed once in cmd/api and
// handed to NewRouter.
type Handlers struct {
	Fixtures   FixtureReader
	Events     EventReader
	Videos     VideoReader
	Presign    Presigner
	Bucket     string        // the API's configured S3 bucket — for the presign-bucket guard
	PresignTTL time.Duration // configured lifetime of URLs minted by Presign
	// CompletedFixtureDates is shared with worker-owned media retention.
	CompletedFixtureDates int
	Log                   logging.Emitter
}

const (
	videoRedirectCacheCap    = 5 * time.Minute
	videoRedirectCacheMargin = time.Minute
)

// ─── assembly ───────────────────────────────────────────────────────────────

// discoveryComplete batches the discovery-complete lookup for a set of events
// (the phase signal). Returns the set of event IDs whose discovery has
// finished; a nil/empty input yields an empty set.
func (h *Handlers) discoveryComplete(ctx context.Context, events []*event.Event) (map[uuid.UUID]bool, error) {
	if len(events) == 0 {
		return map[uuid.UUID]bool{}, nil
	}
	ids := make([]uuid.UUID, len(events))
	for i, e := range events {
		ids[i] = e.ID
	}
	return h.Events.DiscoveryComplete(ctx, ids)
}

// assembleEvents enriches an already-batched event set with one discovery
// query and one ranked-video query. The returned map is keyed by event ID.
func (h *Handlers) assembleEvents(ctx context.Context, events []*event.Event) (map[uuid.UUID]eventDTO, error) {
	done, err := h.discoveryComplete(ctx, events)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(events))
	for i, e := range events {
		ids[i] = e.ID
	}
	clips, err := h.Videos.ListLiveForEvents(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]eventDTO, len(events))
	for _, e := range events {
		videos := make([]videoDTO, 0, len(clips[e.ID]))
		for _, clip := range clips[e.ID] {
			videos = append(videos, toVideoDTO(clip))
		}
		out[e.ID] = toEventDTO(e, videos, done[e.ID])
	}
	return out, nil
}

// assembleFixtures builds complete DTOs from four bounded reads for the whole
// request: fixtures (already loaded), events, discovery state, and videos.
func (h *Handlers) assembleFixtures(ctx context.Context, fixtures []*fixture.Fixture) ([]fixtureDTO, error) {
	fixtureIDs := make([]int64, len(fixtures))
	for i, f := range fixtures {
		fixtureIDs[i] = f.ID
	}
	events, err := h.Events.ListByFixtures(ctx, fixtureIDs)
	if err != nil {
		return nil, err
	}
	assembledEvents, err := h.assembleEvents(ctx, events)
	if err != nil {
		return nil, err
	}
	eventsByFixture := make(map[int64][]*event.Event, len(fixtures))
	for _, e := range events {
		eventsByFixture[e.FixtureID] = append(eventsByFixture[e.FixtureID], e)
	}
	out := make([]fixtureDTO, 0, len(fixtures))
	for _, f := range fixtures {
		domainEvents := eventsByFixture[f.ID]
		eventDTOs := make([]eventDTO, 0, len(domainEvents))
		for _, e := range domainEvents {
			eventDTOs = append(eventDTOs, assembledEvents[e.ID])
		}
		out = append(out, toFixtureDTO(f, eventDTOs, deriveLastActivity(f, domainEvents)))
	}
	return out, nil
}

// ─── handlers ───────────────────────────────────────────────────────────────

// maxBatchIDs caps a ?ids= batch so one request can't fan out unboundedly.
const maxBatchIDs = 200

// GetFixtures returns a flat []fixtureDTO. With ?ids=… it's the batch refetch —
// the monitor cycle's delta coalesced into ONE call (fixtures come full, so a
// single response covers new goals AND minute/status/score bumps across every
// changed match). Without ?ids= it's the full window (staging + active + recent
// completed) for the initial load. One shape either way; the frontend keys by
// id and buckets by state.
func (h *Handlers) GetFixtures(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if raw := r.URL.Query().Get("ids"); raw != "" {
		h.getFixturesByIDs(ctx, w, raw)
		return
	}
	fixtures, err := h.Fixtures.ListPublicWindow(ctx, h.CompletedFixtureDates)
	if err != nil {
		h.serverError(ctx, w, "list public fixtures", err)
		return
	}
	out, err := h.assembleFixtures(ctx, fixtures)
	if err != nil {
		h.serverError(ctx, w, "assemble public fixtures", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// getFixturesByIDs assembles just the requested fixtures. Unknown IDs are
// silently omitted; public-window aging does not delete fixture history.
func (h *Handlers) getFixturesByIDs(ctx context.Context, w http.ResponseWriter, raw string) {
	ids, err := parseInt64CSV(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid ids")
		return
	}
	fixtures, err := h.Fixtures.GetByIDs(ctx, ids)
	if err != nil {
		h.serverError(ctx, w, "batch get fixtures", err)
		return
	}
	out, err := h.assembleFixtures(ctx, fixtures)
	if err != nil {
		h.serverError(ctx, w, "assemble fixtures", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// searchLimit caps how many matched fixtures /search returns (kickoff-newest
// first). Matches the bounded-window model; deeper SQL history is not searched
// by the public route.
const searchLimit = 100

// Search is the free-text fixture search (GET /api/v1/search?q=…). It matches
// the query, case-insensitive substring, against competition (league) name,
// either team name, and any event scorer or assist name, returning the same
// []fixtureDTO shape as /fixtures (fixtures carry their events + live clips) so
// the frontend renders results with the component it already uses. Empty /
// whitespace-only q → 400.
func (h *Handlers) Search(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeError(w, http.StatusBadRequest, "q required")
		return
	}
	fx, err := h.Fixtures.SearchPublicFixtures(ctx, q, searchLimit, h.CompletedFixtureDates)
	if err != nil {
		h.serverError(ctx, w, "search fixtures", err)
		return
	}
	out, err := h.assembleFixtures(ctx, fx)
	if err != nil {
		h.serverError(ctx, w, "assemble search results", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GetEvents is the batch events endpoint (?ids=uuid,uuid): several real-time
// single-event updates coalesced into one call. Returns a flat []eventDTO;
// unknown ids are omitted. Fixture-level changes go through GetFixtures instead
// (fixtures carry their events) — this is for between-cycle event-only updates.
func (h *Handlers) GetEvents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	raw := r.URL.Query().Get("ids")
	if raw == "" {
		writeError(w, http.StatusBadRequest, "ids required")
		return
	}
	ids, err := parseUUIDCSV(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid ids")
		return
	}
	evs, err := h.Events.GetByIDs(ctx, ids)
	if err != nil {
		h.serverError(ctx, w, "batch get events", err)
		return
	}
	assembled, err := h.assembleEvents(ctx, evs)
	if err != nil {
		h.serverError(ctx, w, "assemble events", err)
		return
	}
	out := make([]eventDTO, 0, len(evs))
	for _, e := range evs {
		out = append(out, assembled[e.ID])
	}
	writeJSON(w, http.StatusOK, out)
}

// RedirectVideo resolves a share id through the supersede chain and 302s to a
// presigned Garage URL. active/superseded → 302 (URL stability: an old share
// still plays the current best clip); removed → 410; never-minted → 404.
func (h *Handlers) RedirectVideo(w http.ResponseWriter, r *http.Request) {
	shareID := chi.URLParam(r, "share_id")
	ctx := r.Context()
	rs, err := h.Videos.ResolveShare(ctx, shareID)
	if errors.Is(err, video.ErrNotFound) {
		writeError(w, http.StatusNotFound, "unknown share")
		return
	}
	if err != nil {
		h.serverError(ctx, w, "resolve share", err)
		return
	}
	switch rs.State {
	case video.ShareStateRemoved:
		writeError(w, http.StatusGone, "clip removed") // 410 — VAR/policy
		return
	case video.ShareStateActive, video.ShareStateSuperseded:
		// The presigner signs the API's bound bucket + key. Assets should always
		// carry that same bucket (worker writes with the same S3_BUCKET); log if
		// one ever diverges rather than silently sign the wrong bucket.
		if rs.Bucket != "" && rs.Bucket != h.Bucket && h.Log != nil {
			h.Log.Emit(ctx, logging.LevelWarn, vocabulary.ModuleAPI, vocabulary.ActionAPIShareForeignBucket,
				"share resolves to a foreign bucket",
				logging.String("share", shareID), logging.String("asset_bucket", rs.Bucket),
				logging.String("api_bucket", h.Bucket))
		}
		url, err := h.Presign.PresignGet(ctx, rs.Key)
		if err != nil {
			h.serverError(ctx, w, "presign", err)
			return
		}
		w.Header().Set("Cache-Control", videoRedirectCacheControl(h.PresignTTL))
		http.Redirect(w, r, url, http.StatusFound) // 302
	default:
		h.serverError(ctx, w, "share state", fmt.Errorf("unknown share state %q", rs.State))
	}
}

// videoRedirectCacheControl keeps a cached 302 strictly inside the lifetime of
// the presigned target it contains. The cap preserves the play-latency benefit;
// the margin prevents a cache hit near expiry from returning a dead URL.
func videoRedirectCacheControl(presignTTL time.Duration) string {
	cacheAge := presignTTL - videoRedirectCacheMargin
	if cacheAge <= 0 {
		return "no-store"
	}
	if cacheAge > videoRedirectCacheCap {
		cacheAge = videoRedirectCacheCap
	}
	seconds := int64(cacheAge / time.Second)
	if seconds < 1 {
		return "no-store"
	}
	return fmt.Sprintf("public, max-age=%d", seconds)
}

// ─── helpers ────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// parseInt64CSV parses a comma-separated int64 list (?ids=100,101,102), capped
// at maxBatchIDs. Whitespace around each id is tolerated.
func parseInt64CSV(s string) ([]int64, error) {
	parts := strings.Split(s, ",")
	if len(parts) > maxBatchIDs {
		return nil, fmt.Errorf("too many ids (%d > %d)", len(parts), maxBatchIDs)
	}
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// parseUUIDCSV parses a comma-separated uuid list, capped at maxBatchIDs.
func parseUUIDCSV(s string) ([]uuid.UUID, error) {
	parts := strings.Split(s, ",")
	if len(parts) > maxBatchIDs {
		return nil, fmt.Errorf("too many ids (%d > %d)", len(parts), maxBatchIDs)
	}
	out := make([]uuid.UUID, 0, len(parts))
	for _, p := range parts {
		id, err := uuid.Parse(strings.TrimSpace(p))
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// serverError logs the cause (never leaked to the client) and returns a 500.
func (h *Handlers) serverError(ctx context.Context, w http.ResponseWriter, op string, err error) {
	if h.Log != nil {
		h.Log.Emit(ctx, logging.LevelError, vocabulary.ModuleAPI, vocabulary.ActionAPIRequestFailed,
			"api handler error", logging.String("op", op), logging.Err(err))
	}
	writeError(w, http.StatusInternalServerError, "internal error")
}
