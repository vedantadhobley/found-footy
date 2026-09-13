// Restoration topology controls distinguish direct support from transitive history.
package main

import (
	"bytes"
	"encoding/csv"
	"math/rand"
	"os"
	"reflect"
	"slices"
	"testing"
)

// simpleRestorationBridge has A/C and B/C matches, no A/B match, and B > C > A.
func simpleRestorationBridge() ([]asset, [][]bool) {
	return []asset{
		{id: "A", eventID: "event", durationMS: 10_000, bitrate: 1_000_000, width: 1280, height: 720},
		{id: "B", eventID: "event", durationMS: 10_000, bitrate: 3_000_000, width: 1280, height: 720},
		{id: "C", eventID: "event", durationMS: 10_000, bitrate: 2_000_000, width: 1280, height: 720},
	}, [][]bool{{true, false, true}, {false, true, true}, {true, true, true}}
}

// TestDirectRestorationReturnsDisplacedClip pins the user-described A -> C -> B
// sequence, along with the never-public and exact-recurrence variations.
func TestDirectRestorationReturnsDisplacedClip(t *testing.T) {
	assets, match := simpleRestorationBridge()
	baseline, err := replayDirectRestoration(assets, match, []int{0, 2, 1}, false, true)
	if err != nil || !slices.Equal(baseline.Selected, []string{"B"}) || !slices.Equal(baseline.FinalUncovered, []string{"A"}) {
		t.Fatalf("baseline=%+v err=%v", baseline, err)
	}
	for _, order := range [][]int{{0, 2, 1}, {2, 0, 1}, {0, 2, 1, 0, 2, 1}} {
		result, err := replayDirectRestoration(assets, match, order, true, true)
		if err != nil || !slices.Equal(result.Selected, []string{"A", "B"}) || result.RestorationCount != 1 || result.UnsupportedSteps != 0 {
			t.Fatalf("order=%v result=%+v err=%v", order, result, err)
		}
		if len(result.Steps) != 3 || !slices.Equal(result.Steps[2].Restored, []string{"A"}) {
			t.Fatalf("unexpected restoration trace %+v", result.Steps)
		}
	}
	// Knowing B exists in the corpus must not make it available before arrival.
	prefix, err := replayDirectRestoration(assets, match, []int{0, 2}, true, true)
	if err != nil || !slices.Equal(prefix.Selected, []string{"C"}) || prefix.RestorationCount != 0 {
		t.Fatalf("future evidence leaked into prefix %+v err=%v", prefix, err)
	}
}

// TestDirectRestorationPreservesLosingAndWinningBridgeRules covers all arrivals,
// without adopting a minimum-set objective or forcing all bridges to lose.
func TestDirectRestorationPreservesLosingAndWinningBridgeRules(t *testing.T) {
	for _, bridgeWins := range []bool{false, true} {
		assets, match := simpleRestorationBridge()
		want := []string{"A", "B"}
		if bridgeWins {
			assets[2].bitrate = 4_000_000
			want = []string{"C"}
		}
		visitOrders(3, 6, func(order []int) {
			result, err := replayDirectRestoration(assets, match, order, true, true)
			if err != nil || !slices.Equal(result.Selected, want) {
				t.Fatalf("bridgeWins=%t order=%v selected=%v err=%v", bridgeWins, order, result.Selected, err)
			}
		})
	}
	assets, match := simpleRestorationBridge()
	current, err := replayDirectRestoration(assets, match, []int{0, 1, 2}, false, false)
	if err != nil || !slices.Equal(current.Selected, []string{"A", "B"}) {
		t.Fatalf("FF-092 losing-bridge baseline drift: %+v err=%v", current, err)
	}
}

// TestRestoreUnsupportedUsesSelectedDirectWitnesses checks the invariant on
// directional evidence, without reinterpreting edges as symmetric duplicates.
func TestRestoreUnsupportedUsesSelectedDirectWitnesses(t *testing.T) {
	relation := [][]bool{{true, true, false}, {false, true, true}, {false, false, true}}
	seen := []int{0, 1, 2}
	kept, restored := restoreUnsupported(seen, []int{0}, relation)
	if !slices.Equal(kept, []int{0, 2}) || !slices.Equal(restored, []int{2}) {
		t.Fatalf("transitive coverage accepted: selected=%v restored=%v", kept, restored)
	}
	again, repeated := restoreUnsupported(seen, kept, relation)
	if !slices.Equal(again, kept) || len(repeated) != 0 {
		t.Fatalf("restoration not idempotent: %v/%v", again, repeated)
	}
}

// TestRestorationMaintainsSupportOnGeneratedGraphs checks every prefix and
// preserves the existing Danso-cycle limitation instead of promising total order.
func TestRestorationMaintainsSupportOnGeneratedGraphs(t *testing.T) {
	rng := rand.New(rand.NewSource(81092)) //nolint:gosec // deterministic graph tests
	for trial := 0; trial < 100; trial++ {
		size := 2 + rng.Intn(6)
		assets := make([]asset, size)
		match := completeMatchMatrix(size)
		for i := range assets {
			assets[i] = asset{id: string(rune('A' + i)), durationMS: 5_000 + rng.Intn(30_000), bitrate: 500_000 + rng.Intn(3_000_000), width: 1280, height: 720}
			for j := 0; j < i; j++ {
				match[i][j] = rng.Intn(2) == 0
				match[j][i] = match[i][j]
			}
		}
		visitOrders(size, 100, func(order []int) {
			result, err := replayDirectRestoration(assets, match, order, true, true)
			if err != nil || result.UnsupportedSteps != 0 {
				t.Fatalf("trial=%d order=%v result=%+v err=%v", trial, order, result, err)
			}
		})
	}
	cycle := []asset{
		{id: "a", durationMS: 8_900, bitrate: 1_308_000, width: 1280, height: 720},
		{id: "b", durationMS: 8_400, bitrate: 1_475_000, width: 1280, height: 720},
		{id: "c", durationMS: 10_100, bitrate: 960_000, width: 1280, height: 720},
	}
	outcomes := make(map[string]bool)
	visitOrders(3, 6, func(order []int) {
		result, err := replayDirectRestoration(cycle, completeMatchMatrix(3), order, true, false)
		if err != nil || result.RestorationCount != 0 {
			t.Fatalf("cycle %+v err=%v", result, err)
		}
		outcomes[result.Selected[0]] = true
	})
	if len(outcomes) < 2 {
		t.Fatal("restoration must not claim to resolve the existing quality cycle")
	}
}

// TestRestorationRejectsMixedOrInvalidGraphs prevents scope leakage and silent
// treatment of missing/corrupt evidence as permission to hide another asset.
func TestRestorationRejectsMixedOrInvalidGraphs(t *testing.T) {
	for _, change := range []func([]asset, [][]bool) []int{
		func(a []asset, _ [][]bool) []int { a[1].eventID = "other"; return []int{0} },
		func(a []asset, _ [][]bool) []int { a[1].verified = true; return []int{0} },
		func(a []asset, _ [][]bool) []int { a[1].hashVersion = "other"; return []int{0} },
		func(a []asset, _ [][]bool) []int { a[1].id = a[0].id; return []int{0} },
		func(_ []asset, m [][]bool) []int { m[1] = nil; return []int{0} },
		func(_ []asset, m [][]bool) []int { m[0][0] = false; return []int{0} },
		func(_ []asset, m [][]bool) []int { m[0][1] = true; return []int{0} },
		func(_ []asset, _ [][]bool) []int { return []int{-1} },
		func(_ []asset, _ [][]bool) []int { return []int{3} },
	} {
		assets, match := simpleRestorationBridge()
		if _, err := replayDirectRestoration(assets, match, change(assets, match), true, false); err == nil {
			t.Fatal("invalid graph accepted")
		}
	}
}

// TestRestorationAgainstMastantuono reuses the workflow's exact three originals
// and pins both the deployed six-order outcomes and the experimental repair.
func TestRestorationAgainstMastantuono(t *testing.T) {
	assets := readMastantuonoRestorationAssets(t)
	graph := buildPoolGraph(assets)
	if graph.match[0][1] || !graph.match[0][2] || !graph.match[1][2] {
		t.Fatalf("unexpected real triangle %v", graph.match)
	}
	baseline := map[string][]string{
		"ABC": {"A", "B"}, "BAC": {"A", "B"}, "ACB": {"A", "B"},
		"BCA": {"A"}, "CAB": {"A", "B"}, "CBA": {"A"},
	}
	visitOrders(3, 6, func(order []int) {
		key := ""
		for _, i := range order {
			key += assets[i].id
		}
		before, err := replayDirectRestoration(assets, graph.match, order, false, true)
		if err != nil || !slices.Equal(before.Selected, baseline[key]) {
			t.Fatalf("baseline %s=%v err=%v", key, before.Selected, err)
		}
		after, err := replayDirectRestoration(assets, graph.match, order, true, true)
		if err != nil || !slices.Equal(after.Selected, []string{"A", "B"}) {
			t.Fatalf("restored %s=%v err=%v", key, after.Selected, err)
		}
	})
}

// readMastantuonoRestorationAssets adapts the existing immutable hash fixture
// to the audit CSV decoder without duplicating or regenerating incident hashes.
func readMastantuonoRestorationAssets(t *testing.T) []asset {
	t.Helper()
	raw, err := os.ReadFile("../../internal/workflow/testdata/mastantuono-bridge.csv")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil || len(rows) != 4 {
		t.Fatalf("incident fixture rows=%d err=%v", len(rows), err)
	}
	labels := map[string]string{"731621c93d168bdfac320d60a77482c6": "A", "6e1003b5a45f3c44f012f29b2eecd213": "B", "bf1f217e900aba00b75d0575b2235ad4": "C"}
	md5Column := slices.Index(rows[0], "md5_hex")
	idColumn := slices.Index(rows[0], "asset_id")
	rows[0] = append(rows[0], "event_id", "first_seen_at", "timestamp_verified", "share_state", "fixture_id", "player_name", "minute", "extra", "home_team_name", "away_team_name")
	for i := 1; i < len(rows); i++ {
		rows[i][idColumn] = labels[rows[i][md5Column]]
		rows[i] = append(rows[i], "event", "2026-09-11", "true", "active", "1550126", "Mastantuono", "30", "", "Venezia", "Fiorentina")
	}
	var buffer bytes.Buffer
	w := csv.NewWriter(&buffer)
	if err := w.WriteAll(rows); err != nil {
		t.Fatal(err)
	}
	assets, err := readAssets(&buffer)
	if err != nil {
		t.Fatal(err)
	}
	slices.SortFunc(assets, func(a, b asset) int {
		if a.id < b.id {
			return -1
		}
		if a.id > b.id {
			return 1
		}
		return 0
	})
	if !reflect.DeepEqual([]string{assets[0].id, assets[1].id, assets[2].id}, []string{"A", "B", "C"}) {
		t.Fatal("incident identities changed")
	}
	return assets
}
