// Domain equivalence tests use saved hashes with explicitly synthetic acceptance and source votes.
package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

// checkDomainSelection replays FF-092 plus the actual domain planner. Synthetic
// shares and one source per MD5 isolate topology; they are not recovered history.
func checkDomainSelection(t *testing.T, items []asset, match [][]bool, order []int) []time.Duration {
	t.Helper()
	var timings []time.Duration
	s := dvideo.SelectionSnapshot{EventID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(items[0].eventID)), FixtureID: 1}
	policy := dvideo.SelectionPolicy{MaxHamming: 12, MinRun: 30, MaxGaps: 3, LongMaxHamming: 16, LongMinRun: 50, LongMaxGaps: 5}
	ids := make([]uuid.UUID, len(items))
	byID := make(map[uuid.UUID]int)
	for i, item := range items {
		ids[i] = uuid.NewSHA1(s.EventID, []byte(item.id))
		byID[ids[i]] = i
	}
	var seen, selected []int
	nodes := make(map[int]*dvideo.Asset)
	for sequence, incoming := range order {
		if slices.Contains(seen, incoming) {
			continue
		}
		before := slices.Clone(selected)
		best := -1
		for _, old := range before {
			if match[incoming][old] && (best < 0 || dvideo.IsUpgrade(items[old].quality(), items[best].quality())) {
				best = old
			}
		}
		selected = placeFF092(items, match, slices.Clone(selected), incoming)
		winner := incoming
		if !slices.Contains(selected, incoming) {
			winner = best
		}
		item := items[incoming]
		md5, err := hex.DecodeString(item.md5)
		require.NoError(t, err)
		a := dvideo.NewAsset(s.EventID, 1, "synthetic", "synthetic", md5, item.hashVersion, item.frameHashes, item.width, item.height, item.durationMS, item.fileSizeBytes, time.Unix(int64(sequence), 0))
		a.ID = ids[incoming]
		a.Bitrate = item.quality().Bitrate
		nodes[incoming] = a
		if winner != incoming {
			a.SupersededBySet(ids[winner])
		}
		for _, old := range before {
			if !slices.Contains(selected, old) {
				nodes[old].SupersededBySet(ids[winner])
				for i := range s.Sources {
					if s.Sources[i].CreditedAssetID == ids[old] {
						s.Sources[i].CreditedAssetID = ids[winner]
					}
				}
			}
		}
		s.Nodes = append(s.Nodes, dvideo.SelectionNode{Asset: a, Share: &dvideo.Share{ID: "synthetic-" + item.id, AssetID: a.ID, EventID: s.EventID, TimestampVerified: item.verified}})
		s.Sources = append(s.Sources, dvideo.SelectionSource{ID: uuid.NewSHA1(a.ID, []byte("synthetic-source")), ObservedAssetID: a.ID, CreditedAssetID: ids[winner]})
		seen = append(seen, incoming)
		counts := map[uuid.UUID]int{}
		for _, source := range s.Sources {
			counts[source.CreditedAssetID]++
		}
		var prepared []uuid.UUID
		for i := range s.Nodes {
			n := &s.Nodes[i]
			prepared = append(prepared, n.Asset.ID)
			n.Share.State = dvideo.ShareStateSuperseded
			if n.Asset.SupersededBy == nil {
				n.Share.State = dvideo.ShareStateActive
				n.Asset.Popularity = counts[n.Asset.ID]
			}
		}
		started := time.Now()
		plan, err := dvideo.PlanSelection(s, policy, prepared)
		timings = append(timings, time.Since(started))
		require.NoError(t, err, "order=%v prefix=%d", order, sequence)
		for _, keep := range plan.Selected {
			nodes[byID[keep.AssetID]].Popularity = keep.Popularity
		}
		// Keep the existing placement tournament order. The planner returns a
		// selected set, not authority to reorder incumbents for the next arrival.
		for _, restored := range plan.Restored {
			selected = append(selected, byID[restored])
		}
		require.Len(t, selected, len(plan.Selected))
		owners := map[uuid.UUID]uuid.UUID{}
		for _, owner := range plan.Owners {
			owners[owner.AssetID] = owner.KeeperID
			node := nodes[byID[owner.AssetID]]
			node.SupersededBy = nil
			if owner.AssetID != owner.KeeperID {
				node.SupersededBySet(owner.KeeperID)
			}
		}
		for i := range s.Sources {
			s.Sources[i].CreditedAssetID = owners[s.Sources[i].ObservedAssetID]
		}
		want, err := replayDirectRestoration(items, match, order[:sequence+1], true, false)
		require.NoError(t, err)
		require.Equal(t, want.Selected, coverIDs(items, selected), "domain drift at order=%v prefix=%d", order, sequence)
		for _, keep := range plan.Selected {
			wantSupport := 0
			for _, observed := range seen {
				if observed == byID[keep.AssetID] || match[observed][byID[keep.AssetID]] {
					wantSupport++
				}
			}
			require.Equal(t, wantSupport, keep.Popularity, "per-clip direct evidence, not conserved assigned credit")
		}
	}
	return timings
}

// TestDomainSelectionMastantuono checks every arrival order against the saved incident's exact hashes.
func TestDomainSelectionMastantuono(t *testing.T) {
	items := readMastantuonoRestorationAssets(t)
	graph := buildPoolGraph(items)
	visitOrders(len(items), 6, func(order []int) { checkDomainSelection(t, items, graph.match, order) })
}

// TestDomainSelectionSavedCorpus is opt-in, local-file-only evidence. It never
// connects to a database or claims synthetic credits are historical popularity.
func TestDomainSelectionSavedCorpus(t *testing.T) {
	path := os.Getenv("FF_SELECTION_CORPUS")
	if path == "" {
		t.Skip("set FF_SELECTION_CORPUS to an existing saved CSV")
	}
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()
	items, err := readAssets(file)
	require.NoError(t, err)
	pools := map[poolKey][]asset{}
	for _, item := range items {
		key := poolKey{item.eventID, item.verified, item.hashVersion}
		pools[key] = append(pools[key], item)
	}
	components, prefixes := 0, 0
	var timings []time.Duration
	started := time.Now()
	for _, members := range pools {
		sort.Slice(members, func(i, j int) bool {
			if members[i].firstSeenAt != members[j].firstSeenAt {
				return members[i].firstSeenAt < members[j].firstSeenAt
			}
			return members[i].id < members[j].id
		})
		graph := buildPoolGraph(members)
		for _, indexes := range connectedComponents(graph.connect) {
			if len(indexes) < 2 {
				continue
			}
			var group []asset
			matrix := make([][]bool, len(indexes))
			order := make([]int, len(indexes))
			for i, index := range indexes {
				group = append(group, members[index])
				order[i] = i
				matrix[i] = make([]bool, len(indexes))
				for j, other := range indexes {
					matrix[i][j] = graph.match[index][other]
				}
			}
			t.Run(fmt.Sprint(components), func(t *testing.T) { timings = append(timings, checkDomainSelection(t, group, matrix, order)...) })
			components++
			prefixes += len(group)
		}
	}
	t.Logf("saved assets=%d components=%d prefixes=%d elapsed=%s; synthetic votes/acceptance, not a repair plan", len(items), components, prefixes, time.Since(started))
	if len(timings) > 0 {
		slices.Sort(timings)
		t.Logf("planner-only p50=%s p95=%s max=%s; no SQL, object checks or production contention", timings[len(timings)/2], timings[(len(timings)-1)*95/100], timings[len(timings)-1])
	}
}
