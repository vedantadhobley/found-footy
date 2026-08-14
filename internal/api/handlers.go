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
	Get(ctx context.Context, id int64) (*fixture.Fixture, error)
	ListByState(ctx context.Context, state fixture.State) ([]*fixture.Fixture, error)
}

// EventReader is the event read surface the API needs.
type EventReader interface {
	Get(ctx context.Context, id uuid.UUID) (*event.Event, error)
	ListByFixture(ctx context.Context, fixtureID int64) ([]*event.Event, error)
	// DiscoveryComplete returns the subset of eventIDs whose discovery workflow
	// has finished — the signal event.DerivePhase uses to separate `searching`
	// from `complete`. Batched to avoid an N+1 across a fixture's events.
	DiscoveryComplete(ctx context.Context, eventIDs []uuid.UUID) (map[uuid.UUID]bool, error)
}

// VideoReader is the video read surface: the live-clip list + share resolution.
type VideoReader interface {
	ListLiveForEvent(ctx context.Context, eventID uuid.UUID) ([]video.LiveClip, error)
	ResolveShare(ctx context.Context, id string) (video.ResolvedShare, error)
}

// Presigner mints a short-lived GET URL for a Garage object key.
type Presigner interface {
	PresignGet(ctx context.Context, key string) (string, error)
}

// Handlers bundles the read dependencies. Constructed once in cmd/api and
// handed to NewRouter.
type Handlers struct {
	Fixtures FixtureReader
	Events   EventReader
	Videos   VideoReader
	Presign  Presigner
	Bucket   string // the API's configured S3 bucket — for the presign-bucket guard
	Log      logging.Emitter
}

// ─── assembly ───────────────────────────────────────────────────────────────

// eventVideos loads an event's live clips and maps them to DTOs.
func (h *Handlers) eventVideos(ctx context.Context, eventID uuid.UUID) ([]videoDTO, error) {
	clips, err := h.Videos.ListLiveForEvent(ctx, eventID)
	if err != nil {
		return nil, err
	}
	out := make([]videoDTO, 0, len(clips))
	for _, c := range clips {
		out = append(out, toVideoDTO(c))
	}
	return out, nil
}

// fixtureToDTO assembles a fixture with its non-removed events + each event's
// live videos. NOTE: this is N+1 across an event's videos — fine within the
// bounded window; a single JOIN batch is a later optimization if a busy day
// gets heavy.
func (h *Handlers) fixtureToDTO(ctx context.Context, f *fixture.Fixture) (fixtureDTO, error) {
	events, err := h.Events.ListByFixture(ctx, f.ID)
	if err != nil {
		return fixtureDTO{}, err
	}
	done, err := h.discoveryComplete(ctx, events)
	if err != nil {
		return fixtureDTO{}, err
	}
	dtos := make([]eventDTO, 0, len(events))
	for _, e := range events {
		vids, err := h.eventVideos(ctx, e.ID)
		if err != nil {
			return fixtureDTO{}, err
		}
		dtos = append(dtos, toEventDTO(e, vids, done[e.ID]))
	}
	return toFixtureDTO(f, dtos), nil
}

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
	// Full window. Staging is pre-kickoff (no events) → skip its event/video loads.
	out := []fixtureDTO{}
	for _, s := range []struct {
		state      fixture.State
		withEvents bool
	}{
		{fixture.StateStaging, false},
		{fixture.StateActive, true},
		{fixture.StateCompleted, true},
	} {
		part, err := h.loadState(ctx, s.state, s.withEvents)
		if err != nil {
			h.serverError(ctx, w, "list "+string(s.state), err)
			return
		}
		out = append(out, part...)
	}
	writeJSON(w, http.StatusOK, out)
}

// getFixturesByIDs assembles just the requested fixtures. Unknown ids are
// silently omitted (a fixture may have aged out of retention since the hint).
func (h *Handlers) getFixturesByIDs(ctx context.Context, w http.ResponseWriter, raw string) {
	ids, err := parseInt64CSV(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid ids")
		return
	}
	out := make([]fixtureDTO, 0, len(ids))
	for _, id := range ids {
		f, err := h.Fixtures.Get(ctx, id)
		if errors.Is(err, fixture.ErrNotFound) {
			continue
		}
		if err != nil {
			h.serverError(ctx, w, "batch get fixture", err)
			return
		}
		d, err := h.fixtureToDTO(ctx, f)
		if err != nil {
			h.serverError(ctx, w, "assemble fixture", err)
			return
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, out)
}

// loadState lists + assembles one state bucket. withEvents=false shortcuts
// staging (no events yet) to skip the per-fixture event/video loads.
func (h *Handlers) loadState(ctx context.Context, state fixture.State, withEvents bool) ([]fixtureDTO, error) {
	fx, err := h.Fixtures.ListByState(ctx, state)
	if err != nil {
		return nil, err
	}
	out := make([]fixtureDTO, 0, len(fx))
	for _, f := range fx {
		if !withEvents {
			out = append(out, toFixtureDTO(f, nil))
			continue
		}
		d, err := h.fixtureToDTO(ctx, f)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// GetFixture serves one fixture (with events + videos) — the fixture-scope refetch.
func (h *Handlers) GetFixture(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid fixture id")
		return
	}
	ctx := r.Context()
	f, err := h.Fixtures.Get(ctx, id)
	if errors.Is(err, fixture.ErrNotFound) {
		writeError(w, http.StatusNotFound, "fixture not found")
		return
	}
	if err != nil {
		h.serverError(ctx, w, "get fixture", err)
		return
	}
	d, err := h.fixtureToDTO(ctx, f)
	if err != nil {
		h.serverError(ctx, w, "assemble fixture", err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// GetEvent serves one event (with videos) — the event-scope refetch.
func (h *Handlers) GetEvent(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "event_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid event id")
		return
	}
	ctx := r.Context()
	e, err := h.Events.Get(ctx, id)
	if errors.Is(err, event.ErrNotFound) {
		writeError(w, http.StatusNotFound, "event not found")
		return
	}
	if err != nil {
		h.serverError(ctx, w, "get event", err)
		return
	}
	vids, err := h.eventVideos(ctx, e.ID)
	if err != nil {
		h.serverError(ctx, w, "event videos", err)
		return
	}
	done, err := h.discoveryComplete(ctx, []*event.Event{e})
	if err != nil {
		h.serverError(ctx, w, "event phase", err)
		return
	}
	writeJSON(w, http.StatusOK, toEventDTO(e, vids, done[e.ID]))
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
	evs := make([]*event.Event, 0, len(ids))
	for _, id := range ids {
		e, err := h.Events.Get(ctx, id)
		if errors.Is(err, event.ErrNotFound) {
			continue
		}
		if err != nil {
			h.serverError(ctx, w, "batch get event", err)
			return
		}
		evs = append(evs, e)
	}
	done, err := h.discoveryComplete(ctx, evs)
	if err != nil {
		h.serverError(ctx, w, "batch event phase", err)
		return
	}
	out := make([]eventDTO, 0, len(evs))
	for _, e := range evs {
		vids, err := h.eventVideos(ctx, e.ID)
		if err != nil {
			h.serverError(ctx, w, "event videos", err)
			return
		}
		out = append(out, toEventDTO(e, vids, done[e.ID]))
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
		// Cache the 302 for 5 min so playback doesn't re-resolve every seek
		// (decisions.md 2026-07-02 play-latency fix). Presign TTL must exceed this.
		w.Header().Set("Cache-Control", "public, max-age=300")
		http.Redirect(w, r, url, http.StatusFound) // 302
	default:
		h.serverError(ctx, w, "share state", fmt.Errorf("unknown share state %q", rs.State))
	}
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
