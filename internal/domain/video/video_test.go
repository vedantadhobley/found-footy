// Tests for video Asset + Share types, share ID generation, and
// ranking comparison.
package video_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/domain/video"
)

// helpers ---------------------------------------------------------

func makeAsset(popularity int, fileSize int64) *video.Asset {
	a := video.NewAsset(
		uuid.New(),
		5000,
		"found-footy", "5000/asset123.mp4",
		[]byte{0xab, 0xcd, 0x12, 0x34, 0xef, 0x56, 0x78, 0x90}, // 8-byte dHash
		[]byte("md5md5md5md5md5m"),                            // 16 bytes
		1920, 1080, 45_000,
		fileSize,
		time.Date(2026, 7, 8, 15, 30, 0, 0, time.UTC),
	)
	a.Popularity = popularity
	return a
}

// ShareState ------------------------------------------------------

func TestShareState_Valid(t *testing.T) {
	if !video.ShareStateActive.Valid() {
		t.Error("active must be Valid")
	}
	if !video.ShareStateRemoved.Valid() {
		t.Error("removed must be Valid")
	}
	if video.ShareState("bogus").Valid() {
		t.Error("bogus must not be Valid")
	}
}

// RemovalReason ---------------------------------------------------

func TestRemovalReason_Valid(t *testing.T) {
	for _, r := range []video.RemovalReason{video.RemovalVAR, video.RemovalPolicy, video.RemovalAssetGone} {
		if !r.Valid() {
			t.Errorf("%q must be Valid", r)
		}
	}
	if video.RemovalReason("").Valid() {
		t.Error("empty must not be Valid")
	}
}

// Asset construction ---------------------------------------------

func TestNewAsset_PopulatesDerivedFields(t *testing.T) {
	a := makeAsset(1, 15_000_000)
	if a.ID.String() == "" {
		t.Error("ID is empty")
	}
	if a.Popularity != 1 {
		t.Errorf("Popularity = %d, want 1", a.Popularity)
	}
	// perceptual_hash_prefix should be first 2 bytes: 0xabcd = 43981
	if a.PerceptualHashPrefix != 0xabcd {
		t.Errorf("PerceptualHashPrefix = 0x%x, want 0xabcd", a.PerceptualHashPrefix)
	}
	// aspect ratio 1920/1080 ≈ 1.7777...
	want := float32(1920) / float32(1080)
	if a.AspectRatio != want {
		t.Errorf("AspectRatio = %f, want %f", a.AspectRatio, want)
	}
}

func TestBumpPopularity(t *testing.T) {
	a := makeAsset(3, 15_000_000)
	a.BumpPopularity()
	if a.Popularity != 4 {
		t.Errorf("Popularity = %d, want 4", a.Popularity)
	}
}

func TestSupersededBySet(t *testing.T) {
	a := makeAsset(1, 15_000_000)
	if a.SupersededBy != nil {
		t.Error("new asset must have SupersededBy=nil")
	}
	successor := uuid.New()
	a.SupersededBySet(successor)
	if a.SupersededBy == nil || *a.SupersededBy != successor {
		t.Errorf("SupersededBy = %v, want %v", a.SupersededBy, successor)
	}
}

// Share construction ---------------------------------------------

func TestNewShare_HappyPath(t *testing.T) {
	at := time.Date(2026, 7, 8, 15, 30, 0, 0, time.UTC)
	extracted := 23
	s, err := video.NewShare(uuid.New(), uuid.New(), true, &extracted, 1, at)
	if err != nil {
		t.Fatalf("NewShare: %v", err)
	}
	if !strings.HasPrefix(s.ID, "s_") {
		t.Errorf("share ID = %q, want prefix s_", s.ID)
	}
	if len(s.ID) != len("s_")+12 {
		t.Errorf("share ID length = %d, want %d", len(s.ID), len("s_")+12)
	}
	if s.State != video.ShareStateActive {
		t.Errorf("State = %q, want active", s.State)
	}
	if s.Rank != 1 {
		t.Errorf("Rank = %d, want 1", s.Rank)
	}
	if err := s.ValidateInvariants(); err != nil {
		t.Errorf("invariants: %v", err)
	}
}

func TestNewShare_RejectsZeroRank(t *testing.T) {
	_, err := video.NewShare(uuid.New(), uuid.New(), false, nil, 0, time.Now())
	if err == nil {
		t.Error("expected error for rank=0")
	}
}

func TestNewShare_IDsAreUnique(t *testing.T) {
	ids := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		s, err := video.NewShare(uuid.New(), uuid.New(), false, nil, 1, time.Now())
		if err != nil {
			t.Fatalf("NewShare: %v", err)
		}
		if _, seen := ids[s.ID]; seen {
			t.Errorf("duplicate share ID %q generated", s.ID)
		}
		ids[s.ID] = struct{}{}
	}
}

// Share.Remove ---------------------------------------------------

func TestShare_Remove_HappyPath(t *testing.T) {
	s, _ := video.NewShare(uuid.New(), uuid.New(), true, nil, 1, time.Now())
	at := time.Date(2026, 7, 8, 15, 45, 0, 0, time.UTC)
	if err := s.Remove(video.RemovalVAR, at); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if s.State != video.ShareStateRemoved {
		t.Errorf("State = %q, want removed", s.State)
	}
	if s.RemovedReason == nil || *s.RemovedReason != video.RemovalVAR {
		t.Errorf("RemovedReason = %v, want var", s.RemovedReason)
	}
	if err := s.ValidateInvariants(); err != nil {
		t.Errorf("invariants after Remove: %v", err)
	}
}

func TestShare_Remove_Idempotent(t *testing.T) {
	s, _ := video.NewShare(uuid.New(), uuid.New(), true, nil, 1, time.Now())
	if err := s.Remove(video.RemovalVAR, time.Now()); err != nil {
		t.Fatalf("first Remove: %v", err)
	}
	if err := s.Remove(video.RemovalVAR, time.Now()); err != nil {
		t.Errorf("second Remove same reason: %v", err)
	}
}

func TestShare_Remove_DifferentReasonErrors(t *testing.T) {
	s, _ := video.NewShare(uuid.New(), uuid.New(), true, nil, 1, time.Now())
	if err := s.Remove(video.RemovalVAR, time.Now()); err != nil {
		t.Fatalf("first Remove: %v", err)
	}
	if err := s.Remove(video.RemovalPolicy, time.Now()); err == nil {
		t.Error("expected error remove-with-different-reason")
	}
}

func TestShare_Remove_InvalidReasonErrors(t *testing.T) {
	s, _ := video.NewShare(uuid.New(), uuid.New(), true, nil, 1, time.Now())
	if err := s.Remove(video.RemovalReason("typo"), time.Now()); err == nil {
		t.Error("expected error for invalid reason")
	}
}

// CompareShares --------------------------------------------------

func TestCompareShares_VerifiedBeatsUnverified(t *testing.T) {
	aAsset := makeAsset(1, 10_000_000)
	bAsset := makeAsset(1, 10_000_000)
	aVerified, _ := video.NewShare(aAsset.ID, uuid.New(), true, nil, 1, time.Now())
	bUnver, _ := video.NewShare(bAsset.ID, uuid.New(), false, nil, 2, time.Now())

	if video.CompareShares(aVerified, bUnver, aAsset, bAsset) != -1 {
		t.Error("verified must rank better than unverified")
	}
	if video.CompareShares(bUnver, aVerified, bAsset, aAsset) != +1 {
		t.Error("unverified must rank worse than verified")
	}
}

func TestCompareShares_PopularityBreaksTies(t *testing.T) {
	// Both verified, different popularity.
	aAsset := makeAsset(5, 10_000_000)
	bAsset := makeAsset(2, 10_000_000)
	a, _ := video.NewShare(aAsset.ID, uuid.New(), true, nil, 1, time.Now())
	b, _ := video.NewShare(bAsset.ID, uuid.New(), true, nil, 2, time.Now())

	if video.CompareShares(a, b, aAsset, bAsset) != -1 {
		t.Error("higher popularity must rank better")
	}
}

func TestCompareShares_FileSizeBreaksTies(t *testing.T) {
	// Both verified, same popularity, different file_size.
	aAsset := makeAsset(1, 20_000_000)
	bAsset := makeAsset(1, 10_000_000)
	a, _ := video.NewShare(aAsset.ID, uuid.New(), true, nil, 1, time.Now())
	b, _ := video.NewShare(bAsset.ID, uuid.New(), true, nil, 2, time.Now())

	if video.CompareShares(a, b, aAsset, bAsset) != -1 {
		t.Error("larger file_size must rank better")
	}
}

func TestCompareShares_CreatedAtTiebreak(t *testing.T) {
	// Fully-identical scoring; older CreatedAt wins.
	aAsset := makeAsset(1, 10_000_000)
	bAsset := makeAsset(1, 10_000_000)
	earlier := time.Date(2026, 7, 8, 15, 30, 0, 0, time.UTC)
	later := earlier.Add(10 * time.Minute)
	a, _ := video.NewShare(aAsset.ID, uuid.New(), true, nil, 1, earlier)
	b, _ := video.NewShare(bAsset.ID, uuid.New(), true, nil, 2, later)

	if video.CompareShares(a, b, aAsset, bAsset) != -1 {
		t.Error("older CreatedAt must rank better on full tie")
	}
	if video.CompareShares(a, a, aAsset, aAsset) != 0 {
		t.Error("identical shares must compare as equal")
	}
}

// ValidateInvariants ---------------------------------------------

func TestShare_ValidateInvariants_CatchesActiveWithReason(t *testing.T) {
	s, _ := video.NewShare(uuid.New(), uuid.New(), true, nil, 1, time.Now())
	reason := video.RemovalVAR
	s.RemovedReason = &reason // set without going through Remove()
	if err := s.ValidateInvariants(); err == nil {
		t.Error("expected error: active with RemovedReason set")
	}
}

func TestShare_ValidateInvariants_CatchesRemovedWithoutTimestamp(t *testing.T) {
	s, _ := video.NewShare(uuid.New(), uuid.New(), true, nil, 1, time.Now())
	s.State = video.ShareStateRemoved
	if err := s.ValidateInvariants(); err == nil {
		t.Error("expected error: removed without RemovedReason/At")
	}
}
