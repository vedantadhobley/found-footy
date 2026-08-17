// persist_test.go — unit tests for the consumer-queue persist activities
// with in-memory fakes for S3 + the asset/share repos. Focus: the
// promote→insert→share→rank→cleanup flow, deterministic-UUID retry
// idempotency (double PromoteAndPersist mints exactly one asset + one share),
// failure repair across the durable tail, and the thin bump/delete activities.
package video

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

type fakePromoter struct {
	copies         [][2]string
	deletes        []string
	copyErr        error
	deleteFailures int
}

func (f *fakePromoter) Copy(_ context.Context, src, dst string) error {
	f.copies = append(f.copies, [2]string{src, dst})
	return f.copyErr
}
func (f *fakePromoter) Delete(_ context.Context, key string) error {
	f.deletes = append(f.deletes, key)
	if f.deleteFailures > 0 {
		f.deleteFailures--
		return errors.New("injected delete failure")
	}
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
func (f *fakeAssetStore) AddPopularity(_ context.Context, id uuid.UUID, n int) error {
	if n < 1 {
		n = 1
	}
	if a, ok := f.byID[id]; ok {
		a.Popularity += n
		return nil
	}
	return dvideo.ErrNotFound
}
func (f *fakeAssetStore) Supersede(_ context.Context, loserID, winnerID uuid.UUID) error {
	if loserID == winnerID {
		return nil
	}
	loser, ok := f.byID[loserID]
	if !ok || loser.SupersededBy != nil {
		return nil // missing or already superseded — idempotent no-op
	}
	w := winnerID
	loser.SupersededBy = &w
	if winner, ok := f.byID[winnerID]; ok {
		winner.Popularity += loser.Popularity
	}
	return nil
}
func (f *fakeAssetStore) ListObjectKeysByEvent(_ context.Context, eventID uuid.UUID) ([]dvideo.ObjectRef, error) {
	var out []dvideo.ObjectRef
	for _, a := range f.byID {
		if a.EventID == eventID {
			out = append(out, dvideo.ObjectRef{Bucket: a.S3Bucket, Key: a.S3Key})
		}
	}
	return out, nil
}

type fakeShareStore struct {
	shares            []*dvideo.Share
	rebalanceCalls    int
	rebalanceFailures int
}

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
	f.rebalanceCalls++
	if f.rebalanceFailures > 0 {
		f.rebalanceFailures--
		return 0, errors.New("injected rebalance failure")
	}
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
func (f *fakeShareStore) RemoveByEvent(_ context.Context, eventID uuid.UUID, reason dvideo.RemovalReason) error {
	for _, s := range f.shares {
		if s.EventID == eventID && s.State != dvideo.ShareStateRemoved {
			s.State = dvideo.ShareStateRemoved
			r := reason
			s.RemovedReason = &r
			now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
			s.RemovedAt = &now
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
		StagingKey:  "staging/1583467/e/t.mp4",
		MD5:         hex.EncodeToString([]byte("md5md5md5md5md5m")),
		FrameHashes: []uint64{1, 2, 4, 8}, Width: 1280, Height: 720,
		DurationMS: 6677, FileSizeBytes: 1_000_000,
		Verified: true, ExtractedMinute: intp(91),
	}
}

func intp(i int) *int { return &i }

func TestPromoteAndPersist_HappyPath(t *testing.T) {
	a, s3, assets, shares := newPersist()
	eventID := uuid.New()
	in := stdPromoteInput(eventID)

	out, err := a.PromoteAndPersist(context.Background(), in)
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
	if !out.Minted {
		t.Error("Minted = false, want true (workflow still owes the dirty signal)")
	}
	if shares.rebalanceCalls != 1 {
		t.Errorf("rebalance calls = %d, want 1", shares.rebalanceCalls)
	}
	if len(s3.deletes) != 1 || s3.deletes[0] != in.StagingKey {
		t.Errorf("staging deletes = %v, want [%s]", s3.deletes, in.StagingKey)
	}
}

func TestPromoteAndPersist_Idempotent(t *testing.T) {
	a, s3, assets, shares := newPersist()
	eventID := uuid.New()
	in := stdPromoteInput(eventID)

	first, err := a.PromoteAndPersist(context.Background(), in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	// The first completion deleted staging. If a retry attempts Copy again,
	// model the missing source as a hard failure.
	s3.copyErr = errors.New("staging source is gone")
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
	if len(s3.copies) != 1 {
		t.Errorf("copies = %v, want one (retry must trust durable asset)", s3.copies)
	}
	if len(s3.deletes) != 2 {
		t.Errorf("staging deletes = %v, want two idempotent attempts", s3.deletes)
	}
	if shares.rebalanceCalls != 2 {
		t.Errorf("rebalance calls = %d, want 2 (existing share still repairs ranks)", shares.rebalanceCalls)
	}
	if !first.Minted || !second.Minted {
		t.Errorf("Minted = %v/%v, want true/true so final activity success announces", first.Minted, second.Minted)
	}
}

func TestPromoteAndPersist_RejectsMismatchedDeterministicAsset(t *testing.T) {
	a, s3, assets, shares := newPersist()
	eventID := uuid.New()
	in := stdPromoteInput(eventID)
	assetID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(eventID.String()+":"+in.MD5))
	assets.byID[assetID] = &dvideo.Asset{
		ID: assetID, EventID: eventID, FixtureID: in.FixtureID,
		S3Bucket: a.Bucket, S3Key: "assets/wrong.mp4",
		MD5: []byte("md5md5md5md5md5m"),
	}

	if _, err := a.PromoteAndPersist(context.Background(), in); err == nil {
		t.Fatal("mismatched deterministic asset should fail closed")
	}
	if len(s3.copies) != 0 || len(s3.deletes) != 0 || len(shares.shares) != 0 {
		t.Errorf("side effects after identity mismatch: copies=%v deletes=%v shares=%d",
			s3.copies, s3.deletes, len(shares.shares))
	}
}

// A delete may succeed remotely while its response is lost. The retry must
// not need the now-missing staging source: the deterministic asset row proves
// destination bytes were copied before persistence.
func TestPromoteAndPersist_DeleteFailureRetrySkipsCopy(t *testing.T) {
	a, s3, assets, shares := newPersist()
	eventID := uuid.New()
	in := stdPromoteInput(eventID)
	s3.deleteFailures = 1

	if _, err := a.PromoteAndPersist(context.Background(), in); err == nil {
		t.Fatal("first attempt should fail at staging delete")
	}
	if len(assets.byID) != 1 || len(shares.shares) != 1 {
		t.Fatalf("durable progress = %d assets/%d shares, want 1/1", len(assets.byID), len(shares.shares))
	}
	if shares.rebalanceCalls != 1 {
		t.Fatalf("rebalance calls = %d, want 1 before delete failure", shares.rebalanceCalls)
	}

	s3.copyErr = errors.New("staging source is gone")
	out, err := a.PromoteAndPersist(context.Background(), in)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if out.Inserted {
		t.Error("retry Inserted = true, want false")
	}
	if !out.Minted {
		t.Error("retry Minted = false, want true")
	}
	if len(s3.copies) != 1 {
		t.Errorf("copies = %v, want one (no retry copy after uncertain delete)", s3.copies)
	}
	if len(s3.deletes) != 2 {
		t.Errorf("deletes = %v, want failed attempt + retry", s3.deletes)
	}
	if shares.rebalanceCalls != 2 {
		t.Errorf("rebalance calls = %d, want 2", shares.rebalanceCalls)
	}
}

// FF-023: once Insert has committed, a transient rank failure leaves a share
// for the retry to find. Finding it must not short-circuit rank repair or
// staging cleanup, and it must not mint a second share.
func TestPromoteAndPersist_RebalanceFailureRetryRepairs(t *testing.T) {
	a, s3, _, shares := newPersist()
	eventID := uuid.New()
	in := stdPromoteInput(eventID)
	shares.rebalanceFailures = 1

	if _, err := a.PromoteAndPersist(context.Background(), in); err == nil {
		t.Fatal("first attempt should fail at rank rebalance")
	}
	if len(shares.shares) != 1 {
		t.Fatalf("shares after failed rebalance = %d, want 1", len(shares.shares))
	}
	if len(s3.deletes) != 0 {
		t.Fatalf("staging deleted before durable tail completed: %v", s3.deletes)
	}

	s3.copyErr = errors.New("retry must not copy")
	out, err := a.PromoteAndPersist(context.Background(), in)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(shares.shares) != 1 {
		t.Errorf("shares after retry = %d, want 1", len(shares.shares))
	}
	if shares.rebalanceCalls != 2 {
		t.Errorf("rebalance calls = %d, want failed call + repair", shares.rebalanceCalls)
	}
	if len(s3.copies) != 1 || len(s3.deletes) != 1 {
		t.Errorf("S3 operations copies=%v deletes=%v, want one copy + one cleanup", s3.copies, s3.deletes)
	}
	if !out.Minted {
		t.Error("retry Minted = false, want final success to announce durable share")
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
	// Count>1 — a pending clip that absorbed gate md5-dups transfers them (#180).
	if err := a.BumpAssetPopularity(context.Background(), BumpAssetPopularityInput{AssetID: out.AssetID, Count: 3}); err != nil {
		t.Fatalf("BumpAssetPopularity Count=3: %v", err)
	}
	if got := assets.byID[out.AssetID].Popularity; got != 5 {
		t.Errorf("popularity after +3 = %d, want 5", got)
	}
	if err := a.BumpAssetPopularity(context.Background(), BumpAssetPopularityInput{AssetID: uuid.New()}); err == nil {
		t.Error("bump on missing asset should error")
	}

	s3.deletes = nil // isolate this activity from PromoteAndPersist's cleanup
	if err := a.DeleteStaging(context.Background(), DeleteStagingInput{StagingKey: "staging/x.mp4"}); err != nil {
		t.Fatalf("DeleteStaging: %v", err)
	}
	if len(s3.deletes) != 1 || s3.deletes[0] != "staging/x.mp4" {
		t.Errorf("deletes = %v", s3.deletes)
	}
}

// TestSupersedeAssets — the consolidate activity (#171): each loser gets
// superseded_by=winner + popularity merged, its active share flips to
// 'superseded', and its Garage bytes are reclaimed; the winner is untouched.
// A retry must not double-merge popularity.
func TestSupersedeAssets(t *testing.T) {
	a, s3, assets, shares := newPersist()
	ctx := context.Background()
	eventID := uuid.New()
	winnerID, loserID := uuid.New(), uuid.New()

	assets.byID[winnerID] = &dvideo.Asset{ID: winnerID, EventID: eventID, Popularity: 2, S3Key: "assets/w.mp4"}
	assets.byID[loserID] = &dvideo.Asset{ID: loserID, EventID: eventID, Popularity: 3, S3Key: "assets/l.mp4"}
	winShare := &dvideo.Share{ID: "s_win", AssetID: winnerID, EventID: eventID, State: dvideo.ShareStateActive, Rank: 1}
	loseShare := &dvideo.Share{ID: "s_lose", AssetID: loserID, EventID: eventID, State: dvideo.ShareStateActive, Rank: 2}
	shares.shares = []*dvideo.Share{winShare, loseShare}

	in := SupersedeAssetsInput{EventID: eventID, WinnerAssetID: winnerID, LoserAssetIDs: []uuid.UUID{loserID}}
	if err := a.SupersedeAssets(ctx, in); err != nil {
		t.Fatalf("SupersedeAssets: %v", err)
	}

	if sb := assets.byID[loserID].SupersededBy; sb == nil || *sb != winnerID {
		t.Errorf("loser.SupersededBy = %v, want %v", sb, winnerID)
	}
	if got := assets.byID[winnerID].Popularity; got != 5 { // 2 + merged 3
		t.Errorf("winner.Popularity = %d, want 5", got)
	}
	if assets.byID[winnerID].SupersededBy != nil {
		t.Error("winner must stay live")
	}
	if loseShare.State != dvideo.ShareStateSuperseded {
		t.Errorf("loser share = %q, want superseded", loseShare.State)
	}
	if winShare.State != dvideo.ShareStateActive {
		t.Errorf("winner share = %q, want active (untouched)", winShare.State)
	}
	if len(s3.deletes) != 1 || s3.deletes[0] != "assets/l.mp4" {
		t.Errorf("byte reclaim = %v, want [assets/l.mp4] (loser only)", s3.deletes)
	}

	// Retry: idempotent — popularity must not double-merge.
	if err := a.SupersedeAssets(ctx, in); err != nil {
		t.Fatalf("SupersedeAssets retry: %v", err)
	}
	if got := assets.byID[winnerID].Popularity; got != 5 {
		t.Errorf("winner.Popularity after retry = %d, want 5 (no double-merge)", got)
	}
}

// TestDestroyEvent — the VAR teardown activity (#172): every share of the event
// (active + superseded) → 'removed' with reason var, every asset object
// reclaimed; a share from ANOTHER event is untouched; retry is a no-op.
func TestDestroyEvent(t *testing.T) {
	a, s3, assets, shares := newPersist()
	ctx := context.Background()
	eventID, otherID := uuid.New(), uuid.New()

	liveID, supID := uuid.New(), uuid.New()
	assets.byID[liveID] = &dvideo.Asset{ID: liveID, EventID: eventID, S3Key: "assets/live.mp4"}
	sup := &dvideo.Asset{ID: supID, EventID: eventID, S3Key: "assets/sup.mp4"}
	w := liveID
	sup.SupersededBy = &w
	assets.byID[supID] = sup

	liveShare := &dvideo.Share{ID: "s_live", AssetID: liveID, EventID: eventID, State: dvideo.ShareStateActive, Rank: 1}
	supShare := &dvideo.Share{ID: "s_sup", AssetID: supID, EventID: eventID, State: dvideo.ShareStateSuperseded, Rank: 2}
	otherShare := &dvideo.Share{ID: "s_other", AssetID: uuid.New(), EventID: otherID, State: dvideo.ShareStateActive, Rank: 1}
	shares.shares = []*dvideo.Share{liveShare, supShare, otherShare}

	if err := a.DestroyEvent(ctx, DestroyEventInput{EventID: eventID}); err != nil {
		t.Fatalf("DestroyEvent: %v", err)
	}

	if liveShare.State != dvideo.ShareStateRemoved || supShare.State != dvideo.ShareStateRemoved {
		t.Errorf("shares not removed: live=%q sup=%q", liveShare.State, supShare.State)
	}
	if liveShare.RemovedReason == nil || *liveShare.RemovedReason != dvideo.RemovalVAR {
		t.Errorf("live share reason = %v, want var", liveShare.RemovedReason)
	}
	if otherShare.State != dvideo.ShareStateActive {
		t.Errorf("other-event share clobbered to %q", otherShare.State)
	}
	if len(s3.deletes) != 2 {
		t.Errorf("byte reclaim = %v, want 2 (live + sup)", s3.deletes)
	}

	// Idempotent: a retry leaves the already-removed shares as-is.
	if err := a.DestroyEvent(ctx, DestroyEventInput{EventID: eventID}); err != nil {
		t.Fatalf("DestroyEvent retry: %v", err)
	}
	if liveShare.State != dvideo.ShareStateRemoved {
		t.Errorf("retry mutated a removed share")
	}
}
