// persist_test.go — unit tests for the consumer-queue persist activities
// with in-memory fakes for S3 + the asset/share repos. Focus: the
// promote→insert→share flow, the deterministic-UUID retry idempotency
// (double PromoteAndPersist mints exactly one asset + one share), and the
// thin bump/delete activities.
package video

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/google/uuid"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

type fakePromoter struct {
	copies  [][2]string
	deletes []string
}

func (f *fakePromoter) Copy(_ context.Context, src, dst string) error {
	f.copies = append(f.copies, [2]string{src, dst})
	return nil
}
func (f *fakePromoter) Delete(_ context.Context, key string) error {
	f.deletes = append(f.deletes, key)
	return nil
}

type fakeAssetStore struct {
	byID       map[uuid.UUID]*dvideo.Asset
	byEventMD5 map[string]uuid.UUID
}

func newFakeAssetStore() *fakeAssetStore {
	return &fakeAssetStore{byID: map[uuid.UUID]*dvideo.Asset{}, byEventMD5: map[string]uuid.UUID{}}
}
func (f *fakeAssetStore) Get(_ context.Context, id uuid.UUID) (*dvideo.Asset, error) {
	if a, ok := f.byID[id]; ok {
		return a, nil
	}
	return nil, dvideo.ErrNotFound
}
func (f *fakeAssetStore) InsertAsset(_ context.Context, a *dvideo.Asset) (bool, error) {
	key := a.EventID.String() + ":" + hex.EncodeToString(a.MD5)
	if _, exists := f.byEventMD5[key]; exists {
		return false, nil // ON CONFLICT (event_id, md5)
	}
	cp := *a
	f.byID[a.ID] = &cp
	f.byEventMD5[key] = a.ID
	return true, nil
}
func (f *fakeAssetStore) BumpPopularity(_ context.Context, id uuid.UUID) error {
	if a, ok := f.byID[id]; ok {
		a.Popularity++
		return nil
	}
	return dvideo.ErrNotFound
}
func (f *fakeAssetStore) MarkSuperseded(_ context.Context, loserID, winnerID uuid.UUID) error {
	if a, ok := f.byID[loserID]; ok {
		w := winnerID
		a.SupersededBy = &w
		return nil
	}
	return dvideo.ErrNotFound
}
func (f *fakeAssetStore) AddPopularity(_ context.Context, id uuid.UUID, n int) error {
	if n == 0 {
		return nil
	}
	if a, ok := f.byID[id]; ok {
		a.Popularity += n
		return nil
	}
	return dvideo.ErrNotFound
}

type fakeShareStore struct{ shares []*dvideo.Share }

func (f *fakeShareStore) Get(_ context.Context, id string) (*dvideo.Share, error) {
	for _, s := range f.shares {
		if s.ID == id {
			return s, nil
		}
	}
	return nil, dvideo.ErrNotFound
}
func (f *fakeShareStore) GetByEvent(_ context.Context, eventID uuid.UUID) ([]*dvideo.Share, error) {
	var out []*dvideo.Share
	for _, s := range f.shares {
		if s.EventID == eventID {
			out = append(out, s)
		}
	}
	return out, nil
}
func (f *fakeShareStore) Insert(_ context.Context, s *dvideo.Share) error {
	f.shares = append(f.shares, s)
	return nil
}
func (f *fakeShareStore) Upsert(_ context.Context, s *dvideo.Share) error {
	for i, ex := range f.shares {
		if ex.ID == s.ID {
			f.shares[i] = s
			return nil
		}
	}
	f.shares = append(f.shares, s)
	return nil
}
func (f *fakeShareStore) RebalanceRanks(_ context.Context, _ uuid.UUID) (int, error) {
	return 0, nil // ordering is covered by the pg RebalanceRanks test
}
func (f *fakeShareStore) MarkSuperseded(_ context.Context, id string) error {
	for _, s := range f.shares {
		if s.ID == id && s.State == dvideo.ShareStateActive {
			s.State = dvideo.ShareStateSuperseded
		}
	}
	return nil
}

func newPersist() (*PersistActivities, *fakePromoter, *fakeAssetStore, *fakeShareStore) {
	s3, assets, shares := &fakePromoter{}, newFakeAssetStore(), &fakeShareStore{}
	return &PersistActivities{
		S3: s3, Assets: assets, Shares: shares,
		Bucket: "found-footy", AssetsPrefix: "assets",
	}, s3, assets, shares
}

func stdPromoteInput(eventID uuid.UUID) PromoteAndPersistInput {
	return PromoteAndPersistInput{
		EventID: eventID, FixtureID: 1583467,
		StagingKey: "staging/1583467/e/t.mp4",
		MD5:        hex.EncodeToString([]byte("md5md5md5md5md5m")),
		FrameHashes: []uint64{1, 2, 4, 8}, Width: 1280, Height: 720,
		DurationMS: 6677, FileSizeBytes: 1_000_000,
		Verified: true, ExtractedMinute: intp(91),
	}
}

func intp(i int) *int { return &i }

func TestPromoteAndPersist_HappyPath(t *testing.T) {
	a, s3, assets, shares := newPersist()
	eventID := uuid.New()

	out, err := a.PromoteAndPersist(context.Background(), stdPromoteInput(eventID))
	if err != nil {
		t.Fatalf("PromoteAndPersist: %v", err)
	}
	if !out.Inserted {
		t.Error("Inserted = false, want true")
	}
	// copied staging → the deterministic assets key
	if len(s3.copies) != 1 || s3.copies[0][0] != "staging/1583467/e/t.mp4" {
		t.Fatalf("copies = %v", s3.copies)
	}
	wantKey := "assets/1583467/" + eventID.String() + "/" + out.AssetID.String() + ".mp4"
	if s3.copies[0][1] != wantKey || out.S3Key != wantKey {
		t.Errorf("dst key = %q / %q, want %q", s3.copies[0][1], out.S3Key, wantKey)
	}
	if len(assets.byID) != 1 {
		t.Errorf("assets stored = %d, want 1", len(assets.byID))
	}
	if len(shares.shares) != 1 || out.ShareID == "" {
		t.Errorf("shares = %d, shareID = %q; want 1 share + non-empty id", len(shares.shares), out.ShareID)
	}
}

func TestPromoteAndPersist_Idempotent(t *testing.T) {
	a, _, assets, shares := newPersist()
	eventID := uuid.New()
	in := stdPromoteInput(eventID)

	first, err := a.PromoteAndPersist(context.Background(), in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := a.PromoteAndPersist(context.Background(), in) // retry
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	// Same deterministic asset id; second reports not-inserted; no dupes.
	if first.AssetID != second.AssetID {
		t.Errorf("asset id not stable: %s vs %s", first.AssetID, second.AssetID)
	}
	if second.Inserted {
		t.Error("second Inserted = true, want false (ON CONFLICT)")
	}
	if first.ShareID != second.ShareID {
		t.Errorf("share id not stable: %s vs %s", first.ShareID, second.ShareID)
	}
	if len(assets.byID) != 1 {
		t.Errorf("assets = %d, want 1 (no dupe on retry)", len(assets.byID))
	}
	if len(shares.shares) != 1 {
		t.Errorf("shares = %d, want 1 (no double-mint on retry)", len(shares.shares))
	}
}

func TestBumpAndDelete(t *testing.T) {
	a, s3, assets, _ := newPersist()
	eventID := uuid.New()
	out, _ := a.PromoteAndPersist(context.Background(), stdPromoteInput(eventID))

	if err := a.BumpAssetPopularity(context.Background(), BumpAssetPopularityInput{AssetID: out.AssetID}); err != nil {
		t.Fatalf("BumpAssetPopularity: %v", err)
	}
	if got := assets.byID[out.AssetID].Popularity; got != 2 {
		t.Errorf("popularity = %d, want 2", got)
	}
	if err := a.BumpAssetPopularity(context.Background(), BumpAssetPopularityInput{AssetID: uuid.New()}); err == nil {
		t.Error("bump on missing asset should error")
	}

	if err := a.DeleteStaging(context.Background(), DeleteStagingInput{StagingKey: "staging/x.mp4"}); err != nil {
		t.Fatalf("DeleteStaging: %v", err)
	}
	if len(s3.deletes) != 1 || s3.deletes[0] != "staging/x.mp4" {
		t.Errorf("deletes = %v", s3.deletes)
	}
}
