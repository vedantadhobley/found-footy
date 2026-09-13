// Selection regressions pin direct support, alias routing and evidence boundaries.
package video

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	dvision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// selectionFixture is A -> C -> B, where A/B do not match and the bridge matches both.
func selectionFixture() (SelectionSnapshot, SelectionPolicy, []uuid.UUID) {
	eventID := uuid.New()
	s := SelectionSnapshot{EventID: eventID, FixtureID: 1}
	var ids []uuid.UUID
	for i, hashes := range [][]uint64{{0, 0, 0}, {0, 0, 0, ^uint64(0), ^uint64(0), ^uint64(0)}, {^uint64(0), ^uint64(0), ^uint64(0)}} {
		a := NewAsset(eventID, 1, "test", fmt.Sprint(i), []byte(strings.Repeat(string(rune('a'+i)), 16)), "test-v1", hashes, 1280, 720, 10000, 1000, time.Unix(int64(i), 0))
		share, _ := NewShare(a.ID, eventID, true, selectionMinute(22), i+1, time.Unix(int64(i), 0))
		s.Nodes = append(s.Nodes, SelectionNode{Asset: a, Share: share})
		ids = append(ids, a.ID)
	}
	s.Nodes[0].Asset.SupersededBySet(ids[1])
	s.Nodes[0].Share.State = ShareStateSuperseded
	s.Nodes[1].Asset.SupersededBySet(ids[2])
	s.Nodes[1].Share.State = ShareStateSuperseded
	s.Nodes[2].Asset.Popularity = 14
	for i, count := range []int{2, 5, 7} {
		for range count {
			s.Sources = append(s.Sources, SelectionSource{ID: uuid.New(), ObservedAssetID: ids[i], CreditedAssetID: ids[2]})
		}
	}
	return s, SelectionPolicy{MaxHamming: 0, MinRun: 3, MaxGaps: 0}, ids
}

// TestSelectionRestoresWithDirectSupport pins the motivating 2/5/7 source example.
func TestSelectionRestoresWithDirectSupport(t *testing.T) {
	s, p, ids := selectionFixture()
	before, err := s.Fingerprint()
	require.NoError(t, err)
	plan, err := PlanSelection(s, p, ids)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{ids[0]}, plan.Restored)
	require.Equal(t, []SelectionOwner{{ids[0], ids[0], false}, {ids[1], ids[2], true}, {ids[2], ids[2], false}}, plan.Owners)
	counts := map[uuid.UUID]int{}
	for _, selected := range plan.Selected {
		counts[selected.AssetID] = selected.Popularity
	}
	require.Equal(t, map[uuid.UUID]int{ids[0]: 7, ids[2]: 12}, counts)
	after, err := s.Fingerprint()
	require.NoError(t, err)
	require.Equal(t, before, after, "planner must not mutate input")
	slices.Reverse(s.Nodes)
	slices.Reverse(s.Sources)
	shuffled, err := PlanSelection(s, p, ids)
	require.NoError(t, err)
	require.Equal(t, plan, shuffled)
	after, err = s.Fingerprint()
	require.NoError(t, err)
	require.Equal(t, before, after)
}

// TestSelectionUsesOwnAcceptance allows first-loss restoration only with its own proof.
func TestSelectionUsesOwnAcceptance(t *testing.T) {
	s, p, ids := selectionFixture()
	s.Nodes[0].Share = nil
	plan, err := PlanSelection(s, p, ids)
	require.NoError(t, err)
	require.Empty(t, plan.Restored)
	require.Contains(t, plan.Ineligible, ids[0])
	clock := "22:12"
	frames := []dvision.FrameObservation{{Soccer: true, Clock: &clock}, {Soccer: true}, {Soccer: true}}
	s.Nodes[0].Validation = &dvision.Evidence{ID: uuid.New(), Version: 1, EvaluatedAt: time.Now().UTC(), Evaluator: dvision.EvaluatorVersion,
		EventID: s.EventID, FixtureID: 1, MD5: strings.Repeat("61", 16), PromptSHA256: strings.Repeat("a", 64), SchemaSHA256: strings.Repeat("b", 64),
		Expected: dvision.Expected{Elapsed: 23}, ToleranceMinutes: 1, FramePositions: []float64{1, 2, 3}, Frames: frames, Evaluation: dvision.Evaluate(frames, dvision.Expected{Elapsed: 23}, 1)}
	plan, err = PlanSelection(s, p, ids)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{ids[0]}, plan.Restored)
	for _, selected := range plan.Selected {
		if selected.AssetID == ids[0] {
			require.Equal(t, 22, *selected.ExtractedMinute)
		}
	}
}

// TestSelectionExcludesIneligibleAndFailsIncompleteInputs separates unknown history from safe restoration.
func TestSelectionExcludesIneligibleAndFailsIncompleteInputs(t *testing.T) {
	for name, mutate := range map[string]func(*SelectionSnapshot, []uuid.UUID){
		"removed share": func(s *SelectionSnapshot, _ []uuid.UUID) { s.Nodes[0].Share.State = ShareStateRemoved },
		"reclaimed": func(s *SelectionSnapshot, _ []uuid.UUID) {
			now := time.Now()
			s.Nodes[0].Asset.ObjectReclaimedAt = &now
		},
		"unknown": func(s *SelectionSnapshot, _ []uuid.UUID) { s.Nodes[0].Share = nil },
	} {
		t.Run(name, func(t *testing.T) {
			s, p, ids := selectionFixture()
			mutate(&s, ids)
			plan, err := PlanSelection(s, p, ids)
			require.NoError(t, err)
			require.Empty(t, plan.Restored)
		})
	}
	t.Run("unprepared hidden", func(t *testing.T) {
		s, p, ids := selectionFixture()
		plan, err := PlanSelection(s, p, ids[1:])
		require.NoError(t, err)
		require.Empty(t, plan.Restored)
	})
	for name, mutate := range map[string]func(*SelectionSnapshot){
		"missing observation": func(s *SelectionSnapshot) { s.Sources[0].ObservedAssetID = uuid.Nil },
		"missing credit":      func(s *SelectionSnapshot) { s.Sources[0].CreditedAssetID = uuid.Nil },
		"double source":       func(s *SelectionSnapshot) { s.Sources = append(s.Sources, s.Sources[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			s, p, ids := selectionFixture()
			mutate(&s)
			_, err := PlanSelection(s, p, ids)
			require.ErrorIs(t, err, ErrSelectionCredits)
		})
	}
	s, p, ids := selectionFixture()
	_, err := PlanSelection(s, p, ids[:2])
	require.ErrorIs(t, err, ErrSelectionMedia)
	s.Removed = true
	_, err = PlanSelection(s, p, ids)
	require.ErrorIs(t, err, ErrSelectionRemoved)
}

// TestSelectionScopeAndStrongerBridge keeps verified/hash pools separate and direct bridges valid.
func TestSelectionScopeAndStrongerBridge(t *testing.T) {
	s, p, ids := selectionFixture()
	// A now directly matches B but remains in a different verification pool.
	s.Nodes[0].Asset.FrameHashes = slices.Clone(s.Nodes[2].Asset.FrameHashes)
	s.Nodes[0].Share.TimestampVerified = false
	s.Nodes[0].Share.ExtractedMinute = nil
	plan, err := PlanSelection(s, p, ids)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{ids[0]}, plan.Restored)
	s.Nodes[0].Share.TimestampVerified = true
	s.Nodes[0].Asset.FrameHashVersion = "another-v1"
	plan, err = PlanSelection(s, p, ids)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{ids[0]}, plan.Restored)
	s, p, ids = selectionFixture()
	s.Nodes[2].Asset.FrameHashes = slices.Clone(s.Nodes[1].Asset.FrameHashes)
	plan, err = PlanSelection(s, p, ids)
	require.NoError(t, err)
	require.Empty(t, plan.Restored)
	require.Len(t, plan.Selected, 1)
	s.Nodes[0].Asset.SupersededBySet(ids[1])
	s.Nodes[1].Asset.SupersededBySet(ids[0])
	_, err = PlanSelection(s, p, ids)
	require.ErrorContains(t, err, "cycle")
}

// TestSelectionFingerprintIncludesDurableInputs protects against stale credit, proof and media preparation.
func TestSelectionFingerprintIncludesDurableInputs(t *testing.T) {
	s, _, _ := selectionFixture()
	original, err := s.Fingerprint()
	require.NoError(t, err)
	body, err := json.Marshal(s)
	require.NoError(t, err)
	for _, mutate := range []func(*SelectionSnapshot){
		func(s *SelectionSnapshot) { s.Nodes[0].Share.ExtractedMinute = selectionMinute(21) },
		func(s *SelectionSnapshot) { s.Sources[0].CreditedAssetID = s.Nodes[0].Asset.ID },
		func(s *SelectionSnapshot) { s.Nodes[0].Asset.FrameHashes[0] = 1 },
		func(s *SelectionSnapshot) { s.Removed = true },
	} {
		var copy SelectionSnapshot
		require.NoError(t, json.Unmarshal(body, &copy))
		mutate(&copy)
		changed, err := copy.Fingerprint()
		require.NoError(t, err)
		require.NotEqual(t, original, changed)
	}
}

// TestSelectionChoosesOneDirectOwner keeps valid credit stable and uses existing quality only when reassignment is needed.
func TestSelectionChoosesOneDirectOwner(t *testing.T) {
	for _, mode := range []string{"preserve valid owner", "better direct owner", "incumbent tie"} {
		t.Run(mode, func(t *testing.T) {
			s, policy, ids := selectionFixture()
			for i := range 2 {
				a := NewAsset(s.EventID, s.FixtureID, "test", fmt.Sprint(i+3), []byte(strings.Repeat(string(rune('d'+i)), 16)), "test-v1", []uint64{0, 0, 0}, 1280, 720, 10000, 1000, time.Unix(int64(i+3), 0))
				if i == 1 && mode != "incumbent tie" {
					a.Width, a.Height = 1920, 1080
				}
				share, err := NewShare(a.ID, s.EventID, true, selectionMinute(22), i+4, a.FirstSeenAt)
				require.NoError(t, err)
				s.Nodes = append(s.Nodes, SelectionNode{Asset: a, Share: share})
				s.Sources = append(s.Sources, SelectionSource{ID: uuid.New(), ObservedAssetID: a.ID, CreditedAssetID: a.ID})
				ids = append(ids, a.ID)
			}
			want := ids[4]
			switch mode {
			case "incumbent tie":
				want = ids[3]
			case "preserve valid owner":
				s.Nodes[2].Asset.FrameHashes = slices.Clone(s.Nodes[1].Asset.FrameHashes)
				want = ids[2]
			}
			plan, err := PlanSelection(s, policy, ids)
			require.NoError(t, err)
			require.Empty(t, plan.Restored, "existing selected roots already support A")
			require.Equal(t, SelectionOwner{AssetID: ids[0], KeeperID: want, Direct: true}, plan.Owners[0])
			counts := make(map[uuid.UUID]int)
			for _, keep := range plan.Selected {
				counts[keep.AssetID] = keep.Popularity
			}
			if mode == "preserve valid owner" {
				require.Equal(t, map[uuid.UUID]int{ids[2]: 16, ids[3]: 16, ids[4]: 16}, counts)
			} else {
				require.Equal(t, map[uuid.UUID]int{ids[2]: 12, ids[3]: 9, ids[4]: 9}, counts,
					"routing to one keeper does not allocate that keeper exclusive support")
			}
		})
	}
}

// selectionMinute makes nullable own-clock fixtures explicit.
func selectionMinute(n int) *int { return &n }
