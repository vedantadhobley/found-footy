// HTTP tests pin the internal read contract, reclaimed-media behavior, and byte ranges.
package web

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy-operator-ui/internal/live"
	"github.com/vedantadhobley/found-footy-operator-ui/internal/media"
	"github.com/vedantadhobley/found-footy-operator-ui/internal/model"
	"github.com/vedantadhobley/found-footy-operator-ui/internal/store"
)

type fakeRepo struct {
	list   model.EventList
	detail model.EventDetail
	object model.MediaObject
	err    error
}

func (f *fakeRepo) ListEvents(context.Context, model.ListOptions) (model.EventList, error) {
	return f.list, f.err
}

func (f *fakeRepo) EventDetail(context.Context, string) (model.EventDetail, error) {
	return f.detail, f.err
}

func (f *fakeRepo) MediaObject(context.Context, string) (model.MediaObject, error) {
	return f.object, f.err
}

type fakeObjects struct {
	data         []byte
	byteRange    string
	contentRange string
	openCalled   bool
}

func (f *fakeObjects) Open(_ context.Context, _, _, byteRange string) (media.Object, error) {
	f.openCalled, f.byteRange = true, byteRange
	return media.Object{Body: io.NopCloser(bytes.NewReader(f.data)), ContentLength: int64(len(f.data)),
		ContentRange: f.contentRange, ContentType: "video/mp4"}, nil
}

func testServer(t *testing.T, repo *fakeRepo, objects *fakeObjects) *Server {
	t.Helper()
	server, err := New(repo, objects, live.NewHub(), "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return server
}

// TestListEvents returns the independent operator projection.
func TestListEvents(t *testing.T) {
	repo := &fakeRepo{list: model.EventList{Events: []model.EventSummary{{ID: uuid.NewString()}}, Total: 1, Limit: 100}}
	recorder := httptest.NewRecorder()
	testServer(t, repo, &fakeObjects{}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/events", nil))
	if recorder.Code != http.StatusOK || !bytes.Contains(recorder.Body.Bytes(), []byte(`"total":1`)) {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestEventNotFound preserves a real 404 instead of returning an empty shell.
func TestEventNotFound(t *testing.T) {
	repo := &fakeRepo{err: store.ErrNotFound}
	recorder := httptest.NewRecorder()
	path := "/api/events/" + uuid.NewString()
	testServer(t, repo, &fakeObjects{}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

// TestBrowserNormalizesDOMEventNames prevents onClick from becoming the
// case-sensitive, non-existent DOM event name "Click".
func TestBrowserNormalizesDOMEventNames(t *testing.T) {
	body, err := staticFiles.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	if !bytes.Contains(body, []byte("key.slice(2).toLowerCase()")) {
		t.Fatal("DOM event helper does not normalize event names")
	}
}

// TestMediaForwardsRange verifies browser seeking remains inside the UI proxy.
func TestMediaForwardsRange(t *testing.T) {
	assetID := uuid.NewString()
	repo := &fakeRepo{object: model.MediaObject{Bucket: "footy", Key: "fixture/asset.mp4"}}
	objects := &fakeObjects{data: []byte("video")}
	request := httptest.NewRequest(http.MethodGet, "/media/"+assetID, nil)
	request.Header.Set("Range", "bytes=0-4")
	recorder := httptest.NewRecorder()
	testServer(t, repo, objects).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || objects.byteRange != "bytes=0-4" || recorder.Body.String() != "video" {
		t.Fatalf("status=%d range=%q body=%q", recorder.Code, objects.byteRange, recorder.Body.String())
	}
}

// TestMediaReturnsPartialContent forwards Garage's resolved range response.
func TestMediaReturnsPartialContent(t *testing.T) {
	repo := &fakeRepo{object: model.MediaObject{Bucket: "footy", Key: "fixture/asset.mp4"}}
	objects := &fakeObjects{data: []byte("video"), contentRange: "bytes 0-4/10"}
	request := httptest.NewRequest(http.MethodGet, "/media/"+uuid.NewString(), nil)
	request.Header.Set("Range", "bytes=0-4")
	recorder := httptest.NewRecorder()
	testServer(t, repo, objects).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusPartialContent || recorder.Header().Get("Content-Range") != "bytes 0-4/10" {
		t.Fatalf("status=%d content-range=%q", recorder.Code, recorder.Header().Get("Content-Range"))
	}
}

// TestReclaimedMediaNeverTouchesGarage proves SQL lifecycle gates object reads.
func TestReclaimedMediaNeverTouchesGarage(t *testing.T) {
	reclaimed := time.Now()
	repo := &fakeRepo{object: model.MediaObject{ReclaimedAt: &reclaimed}}
	objects := &fakeObjects{}
	recorder := httptest.NewRecorder()
	testServer(t, repo, objects).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/media/"+uuid.NewString(), nil))
	if recorder.Code != http.StatusGone || objects.openCalled {
		t.Fatalf("status=%d open=%v, want 410 without Garage", recorder.Code, objects.openCalled)
	}
}
