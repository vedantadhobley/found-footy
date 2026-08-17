// handlers_test.go — read-API handler tests with in-memory fakes for the read
// ports (no DB, no S3). Covers assembly (fixture → events → live videos), DTO
// mapping, and the /videos/{share_id} redirect contract (302/410/404). Internal
// (package api) so it can assert on the unexported DTO shapes. Log is nil — the
// handlers guard it; these tests assert status codes + bodies, not log output.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/domain/video"
)

// ─── fakes ──────────────────────────────────────────────────────────────────

type fakeFixtures struct {
	byID    map[int64]*fixture.Fixture
	byState map[fixture.State][]*fixture.Fixture
}

func (f *fakeFixtures) Get(_ context.Context, id int64) (*fixture.Fixture, error) {
	if fx, ok := f.byID[id]; ok {
		return fx, nil
	}
	return nil, fixture.ErrNotFound
}
func (f *fakeFixtures) ListByState(_ context.Context, s fixture.State) ([]*fixture.Fixture, error) {
	return f.byState[s], nil
}
func (f *fakeFixtures) SearchFixtures(_ context.Context, q string, _ int) ([]*fixture.Fixture, error) {
	// A lightweight league/team substring match — enough to exercise the
	// handler's wire-through + assembly. The full 4-arm SQL (incl. scorer/
	// assist) is covered by TestFixtureRepo_SearchFixtures.
	var out []*fixture.Fixture
	ql := strings.ToLower(q)
	for _, fx := range f.byID {
		if strings.Contains(strings.ToLower(fx.League.Name), ql) ||
			strings.Contains(strings.ToLower(fx.Home.Name), ql) ||
			strings.Contains(strings.ToLower(fx.Away.Name), ql) {
			out = append(out, fx)
		}
	}
	return out, nil
}

type fakeEvents struct {
	byID          map[uuid.UUID]*event.Event
	byFixture     map[int64][]*event.Event
	discoveryDone map[uuid.UUID]bool // event IDs whose discovery has completed
}

func (f *fakeEvents) Get(_ context.Context, id uuid.UUID) (*event.Event, error) {
	if e, ok := f.byID[id]; ok {
		return e, nil
	}
	return nil, event.ErrNotFound
}
func (f *fakeEvents) ListByFixture(_ context.Context, fixtureID int64) ([]*event.Event, error) {
	return f.byFixture[fixtureID], nil
}
func (f *fakeEvents) DiscoveryComplete(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if f.discoveryDone[id] {
			out[id] = true
		}
	}
	return out, nil
}

type fakeVideos struct {
	byEvent map[uuid.UUID][]video.LiveClip
	resolve map[string]video.ResolvedShare
}

func (f *fakeVideos) ListLiveForEvent(_ context.Context, eventID uuid.UUID) ([]video.LiveClip, error) {
	return f.byEvent[eventID], nil
}
func (f *fakeVideos) ResolveShare(_ context.Context, id string) (video.ResolvedShare, error) {
	if rs, ok := f.resolve[id]; ok {
		return rs, nil
	}
	return video.ResolvedShare{}, video.ErrNotFound
}

type fakePresign struct{ url string }

func (f *fakePresign) PresignGet(_ context.Context, _ string) (string, error) { return f.url, nil }

func ip(i int) *int       { return &i }
func sp(s string) *string { return &s }

// scaffold builds one fixture (id 100, active) with one goal event carrying one
// live clip, wired into all three fakes.
func scaffold() (*Handlers, int64, uuid.UUID) {
	fxID := int64(100)
	evID := uuid.New()
	fx := &fixture.Fixture{
		ID: fxID, State: fixture.StateActive,
		Kickoff:    time.Date(2026, 8, 14, 19, 0, 0, 0, time.UTC),
		Home:       fixture.Team{ID: 1, Name: "Alpha"},
		Away:       fixture.Team{ID: 2, Name: "Beta"},
		League:     fixture.League{ID: 140, Name: "La Liga", Season: 2026},
		APIStatus:  fixture.APIStatus{Short: "2H", Long: "Second Half"},
		APIElapsed: ip(67), HomeScore: ip(2), AwayScore: ip(1),
	}
	ev := &event.Event{
		ID: evID, FixtureID: fxID, Type: event.Type("goal"), Minute: 23,
		Team:   event.Team{ID: 1, Name: "Alpha"},
		Player: event.Player{ID: ip(9), Name: sp("Scorer")},
	}
	clip := video.LiveClip{ShareID: "s_abc123", Rank: 1, Verified: true,
		ExtractedMinute: ip(23), Popularity: 3, Width: 1920, Height: 1080, DurationMS: 8000}

	h := &Handlers{
		Fixtures: &fakeFixtures{
			byID:    map[int64]*fixture.Fixture{fxID: fx},
			byState: map[fixture.State][]*fixture.Fixture{fixture.StateActive: {fx}},
		},
		Events: &fakeEvents{
			byID:      map[uuid.UUID]*event.Event{evID: ev},
			byFixture: map[int64][]*event.Event{fxID: {ev}},
		},
		Videos: &fakeVideos{
			byEvent: map[uuid.UUID][]video.LiveClip{evID: {clip}},
			resolve: map[string]video.ResolvedShare{},
		},
		Presign:    &fakePresign{url: "https://garage.example/presigned"},
		Bucket:     "found-footy",
		PresignTTL: 5 * time.Minute,
	}
	return h, fxID, evID
}

func get(h *Handlers, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	NewRouter(h).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// ─── tests ──────────────────────────────────────────────────────────────────

func TestGetFixtures_Window(t *testing.T) {
	h, _, _ := scaffold()
	rec := get(h, "/api/v1/fixtures")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var list []fixtureDTO // flat — the frontend buckets by state
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 1 || list[0].State != "active" || len(list[0].Events) != 1 {
		t.Fatalf("window = %+v, want one active fixture carrying its event", list)
	}
}

func TestGetFixtures_Batch(t *testing.T) {
	h, fxID, _ := scaffold()
	// ?ids= returns just the requested fixtures (flat), skipping unknown ids —
	// the per-cycle batch refetch. Fixtures come full (events carried).
	rec := get(h, fmt.Sprintf("/api/v1/fixtures?ids=%d,999999", fxID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var list []fixtureDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 1 || list[0].ID != fxID || len(list[0].Events) != 1 {
		t.Fatalf("batch = %+v, want just fixture %d with its event", list, fxID)
	}
	if rec := get(h, "/api/v1/fixtures?ids=100,notanumber"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad batch id = %d, want 400", rec.Code)
	}
}

func TestSearch(t *testing.T) {
	h, fxID, _ := scaffold() // fixture 100: La Liga · Alpha vs Beta · one goal event w/ clip

	// competition match → the fixture assembled with its event + live clip
	rec := get(h, "/api/v1/search?q=la+liga")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var list []fixtureDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 1 || list[0].ID != fxID || len(list[0].Events) != 1 || len(list[0].Events[0].Videos) != 1 {
		t.Fatalf("search = %+v, want fixture %d assembled with its event+clip", list, fxID)
	}

	// team match, case-insensitive
	if rec := get(h, "/api/v1/search?q=ALPHA"); rec.Code != http.StatusOK {
		t.Errorf("team match status = %d, want 200", rec.Code)
	}

	// no match → 200 + empty array (not 404)
	rec = get(h, "/api/v1/search?q=zzznope")
	var empty []fixtureDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &empty)
	if rec.Code != http.StatusOK || len(empty) != 0 {
		t.Errorf("no-match = %d / %+v, want 200 / empty", rec.Code, empty)
	}

	// empty/whitespace q → 400
	if rec := get(h, "/api/v1/search?q="); rec.Code != http.StatusBadRequest {
		t.Errorf("empty q = %d, want 400", rec.Code)
	}
}

func TestGetEvents_Batch(t *testing.T) {
	h, _, evID := scaffold()
	rec := get(h, "/api/v1/events?ids="+evID.String()+","+uuid.NewString())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var list []eventDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list) != 1 || list[0].ID != evID.String() || len(list[0].Videos) != 1 {
		t.Fatalf("batch events = %+v, want just event %s with its clip", list, evID)
	}
	if rec := get(h, "/api/v1/events"); rec.Code != http.StatusBadRequest {
		t.Errorf("events without ids = %d, want 400", rec.Code)
	}
}

func TestRedirectVideo(t *testing.T) {
	h, _, _ := scaffold()
	fv := h.Videos.(*fakeVideos)
	fv.resolve["s_active"] = video.ResolvedShare{State: video.ShareStateActive, Bucket: "found-footy", Key: "a.mp4"}
	fv.resolve["s_superseded"] = video.ResolvedShare{State: video.ShareStateSuperseded, Bucket: "found-footy", Key: "b.mp4"}
	fv.resolve["s_removed"] = video.ResolvedShare{State: video.ShareStateRemoved}

	// Active → 302 to the presigned URL, with one minute held back from the
	// five-minute signature lifetime.
	rec := get(h, "/api/v1/videos/s_active")
	if rec.Code != http.StatusFound {
		t.Fatalf("active status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://garage.example/presigned" {
		t.Errorf("Location = %q", loc)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=240" {
		t.Errorf("Cache-Control = %q, want public, max-age=240", cc)
	}
	// Superseded still 302s (URL stability — resolves through the chain).
	if rec := get(h, "/api/v1/videos/s_superseded"); rec.Code != http.StatusFound {
		t.Errorf("superseded status = %d, want 302", rec.Code)
	}
	// Removed → 410 Gone; never-minted → 404.
	if rec := get(h, "/api/v1/videos/s_removed"); rec.Code != http.StatusGone {
		t.Errorf("removed status = %d, want 410", rec.Code)
	}
	if rec := get(h, "/api/v1/videos/s_nope"); rec.Code != http.StatusNotFound {
		t.Errorf("missing status = %d, want 404", rec.Code)
	}
}

func TestVideoRedirectCacheControl(t *testing.T) {
	for _, tc := range []struct {
		name       string
		presignTTL time.Duration
		want       string
	}{
		{name: "default keeps one minute", presignTTL: 5 * time.Minute, want: "public, max-age=240"},
		{name: "long lifetime caps at five minutes", presignTTL: 10 * time.Minute, want: "public, max-age=300"},
		{name: "margin consumes short lifetime", presignTTL: 30 * time.Second, want: "no-store"},
		{name: "subsecond remainder is not cached", presignTTL: time.Minute + 500*time.Millisecond, want: "no-store"},
		{name: "unset lifetime cannot be cached", presignTTL: 0, want: "no-store"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := videoRedirectCacheControl(tc.presignTTL); got != tc.want {
				t.Fatalf("cache control = %q, want %q", got, tc.want)
			}
		})
	}
}
