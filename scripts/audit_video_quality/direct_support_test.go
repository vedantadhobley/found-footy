// Direct-support regressions keep overlapping evidence distinct from duplicated votes or keeper policy.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"testing"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

// TestDirectSupportCountsEachSourceOncePerClip pins 2/22 assigned versus 22/22
// direct support. Forty-four memberships still represent only 24 source tweets.
func TestDirectSupportCountsEachSourceOncePerClip(t *testing.T) {
	items := popularityTestBridge()
	r := compareDirectSupport(items)
	if r.Sources != 24 || r.ConditionalCovered != 24 || r.ConditionalShared != 20 || r.ConditionalSupportTotal != 44 {
		t.Fatalf("incorrect source accounting: %+v", r)
	}
	for _, root := range r.Roots {
		if root.Exact != 2 || root.Known != 22 || root.Conditional != 22 {
			t.Fatalf("exact sources were added twice: %+v", root)
		}
	}
	if len(r.ConditionalEffect.Reversals) != 0 || len(r.ConditionalEffect.UnresolvedOrder) != 1 {
		t.Fatalf("unresolved tie presented as a reversal: %+v", r.ConditionalEffect)
	}
	// Changing exclusive ownership changes assigned scores, not direct support.
	items[2].supersededBy, items[0].popularity, items[1].popularity = "A", 22, 2
	other := compareDirectSupport(items)
	if other.Roots[0].Conditional != r.Roots[0].Conditional || other.Roots[1].Conditional != r.Roots[1].Conditional {
		t.Fatal("direct support depends on exclusive owner")
	}
	items[0].bitrate *= 10
	items[0].durationMS *= 10
	if got := compareDirectSupport(items); got.Roots[0].Conditional != 22 {
		t.Fatal("quality influenced a fixed clip's support")
	}
}

// TestDirectSupportDoesNotFollowTransitiveLineage excludes A's sightings from
// B when only their hidden bridge matches each endpoint.
func TestDirectSupportDoesNotFollowTransitiveLineage(t *testing.T) {
	a := popularityTestBridge()
	a[0].supersededBy, a[0].shareState = "C", "superseded"
	a[1].popularity = 24
	r := compareDirectSupport(a)
	if len(r.Roots) != 1 || r.Roots[0].Conditional != 22 || r.ConditionalCovered != 22 || len(r.Witnesses) != 1 {
		t.Fatalf("transitive support credited: %+v", r)
	}
	if r.Witnesses[0].AssetID != "A" || r.Witnesses[0].OwnerDirect {
		t.Fatalf("unsupported source attribution hidden: %+v", r.Witnesses)
	}
	p, err := projectPopularityRestorationMode(a, true)
	if err != nil || len(p.Restored) != 1 || p.DirectSupport.ConditionalSupportTotal != 44 {
		t.Fatalf("restoration projection failed: %+v %v", p, err)
	}
}

// TestDirectSupportKeepsUnknownAndRevokedEvidenceSeparate does not confuse
// retained source evidence with the ability to restore its media for playback.
func TestDirectSupportKeepsUnknownAndRevokedEvidenceSeparate(t *testing.T) {
	a := popularityTestBridge()
	a[2].shareID, a[2].shareState = "", "observed"
	r := compareDirectSupport(a)
	if r.UnknownSources != 20 || r.Roots[0].Known != 2 || r.Roots[0].Conditional != 22 {
		t.Fatalf("unknown category counted as known: %+v", r)
	}
	a[2].shareID, a[2].shareState = "share-C", "removed"
	r = compareDirectSupport(a)
	if r.ExcludedSources != 20 || r.Roots[0].Conditional != 2 || r.Roots[1].Conditional != 2 {
		t.Fatalf("removed acceptance reused: %+v", r)
	}
	a[2].shareState, a[2].objectReclaimedAt = "superseded", "2026-09-12"
	r = compareDirectSupport(a)
	if r.Roots[0].Known != 22 || r.UnknownSources != 0 || r.ExcludedSources != 0 {
		t.Fatal("media reclamation erased retained source evidence")
	}
}

// TestDirectSupportKeepsScopesAndVisibility pins the unchanged verified-first
// and asymmetric singleton rules, including count effects of a shared source.
func TestDirectSupportKeepsScopesAndVisibility(t *testing.T) {
	a := popularityTestBridge()
	a[0].popularity, a[0].observedPopularity = 1, 1
	a[1].popularity, a[1].observedPopularity = 21, 1
	r := compareDirectSupport(a)
	if !slices.Equal(r.ConditionalEffect.Added, []string{"A"}) || len(r.ConditionalEffect.Hidden) != 0 {
		t.Fatalf("singleton filter comparison failed: %+v", r)
	}
	for _, mutate := range []func(*asset){
		func(a *asset) { a.eventID = "other" },
		func(a *asset) { a.verified = !a.verified },
		func(a *asset) { a.hashVersion = dvideo.LegacyFrameHashVersion },
	} {
		items := popularityTestBridge()
		mutate(&items[2])
		if got := compareDirectSupport(items); got.Roots[0].Conditional != 2 || got.Roots[1].Conditional != 2 {
			t.Fatal("support crossed a matching scope")
		}
	}
}

// TestDirectSupportIncludesOtherSelectedSources has no hidden-only shortcut:
// selected MD5 sightings are evidence for every directly matching selected clip.
func TestDirectSupportIncludesOtherSelectedSources(t *testing.T) {
	a := popularityTestBridge()[:2]
	a[1].frameHashes, a[1].popularity = a[0].frameHashes, 2
	r := compareDirectSupport(a)
	if r.Sources != 4 || r.ConditionalShared != 4 || r.ConditionalSupportTotal != 8 || r.Roots[0].Conditional != 4 {
		t.Fatalf("selected-source evidence omitted or counted twice: %+v", r)
	}
}

// TestDirectSupportExactSelfDoesNotRequirePerceptualWindow preserves exact
// evidence even when a damaged/short hash trace cannot satisfy a dHash route.
func TestDirectSupportExactSelfDoesNotRequirePerceptualWindow(t *testing.T) {
	a := popularityTestBridge()
	a[0].frameHashes = a[0].frameHashes[:2]
	r := compareDirectSupport(a)
	if r.Roots[0].Known != 2 || r.Roots[0].Conditional != 2 {
		t.Fatal("exact self support depended on a perceptual match")
	}
}

// TestDirectSupportSavedSeptember11 pins measured counts from the offline
// corpus; all old missing-category assumptions remain explicit in the report.
func TestDirectSupportSavedSeptember11(t *testing.T) {
	path := os.Getenv("FF_POPULARITY_CORPUS")
	if path == "" {
		t.Skip("set FF_POPULARITY_CORPUS to the converted September 11 export")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	items, err := readAssets(f)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := writeSupportReport(&out, items, true); err != nil {
		t.Fatal(err)
	}
	qualified, changed, visible, shared := 0, 0, 0, 0
	for _, line := range bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n")) {
		var r popularityReport
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatal(err)
		}
		if r.DirectSupport == nil {
			continue
		}
		qualified++
		p := r.Restoration.DirectSupport
		shared += p.ConditionalShared
		if len(p.ConditionalEffect.Added) != 0 || len(p.ConditionalEffect.Hidden) != 0 {
			visible++
		}
		if len(p.ConditionalEffect.Reversals) != 0 || len(p.ConditionalEffect.UnresolvedOrder) != 0 {
			t.Fatalf("unexpected historical ordering change: %s", r.EventID)
		}
		different := false
		for _, root := range p.Roots {
			different = different || root.Assigned != root.Conditional
		}
		if different {
			changed++
		}
		switch r.EventID {
		case "01dedc54-da16-4e3c-9539-ac43c81ffd77":
			for _, root := range p.Roots {
				if root.AssetID == "32b50be4-0ad5-50a5-8ebe-f590cc4b57a4" &&
					(root.Assigned != 1 || root.Known != 1 || root.Conditional != 6) {
					t.Fatalf("Maitland-Niles score drift: %+v", root)
				}
			}
		case "d7db1660-1055-4651-b3ef-75d4e6dc3ad3":
			for _, root := range p.Roots {
				if root.AssetID == "3da095d5-23eb-5c83-ac6f-90b9037e17ad" &&
					(root.Assigned != 9 || root.Known != 9 || root.Conditional != 19) {
					t.Fatalf("Mastantuono score drift: %+v", root)
				}
			}
		}
	}
	if qualified != 278 || changed != 2 || visible != 1 || shared != 10 {
		t.Fatalf("corpus drift: qualified=%d changed=%d visibility=%d shared=%d", qualified, changed, visible, shared)
	}
}

// TestDirectSupportReportPreservesOriginalMode and sorting prevent the new
// experimental meaning from silently replacing earlier allocation reports.
func TestDirectSupportReportPreservesOriginalMode(t *testing.T) {
	a := popularityTestBridge()
	var old, current, reversed bytes.Buffer
	if err := writePopularityJSON(&old, a); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(old.Bytes(), []byte("direct_support_comparison")) {
		t.Fatal("prior report mode changed")
	}
	if err := writeSupportReport(&current, a, true); err != nil {
		t.Fatal(err)
	}
	slices.Reverse(a)
	if err := writeSupportReport(&reversed, a, true); err != nil || !bytes.Equal(current.Bytes(), reversed.Bytes()) {
		t.Fatalf("nondeterministic report: %v", err)
	}
	var r popularityReport
	if err := json.Unmarshal(current.Bytes(), &r); err != nil || r.Experiment != "assigned-vs-direct-support-v1" ||
		r.DirectSupport == nil || r.Restoration.DirectSupport == nil {
		t.Fatalf("bad report contract: %v", err)
	}
	a[0].observedPopularity = 0
	var incomplete bytes.Buffer
	if err := writeSupportReport(&incomplete, a, true); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(incomplete.Bytes(), []byte("direct_support_comparison")) {
		t.Fatal("incomplete attribution presented as measured zero support")
	}
	a = popularityTestBridge()
	a[1].md5 = a[0].md5
	var invalid bytes.Buffer
	if err := writeSupportReport(&invalid, a, true); err == nil || invalid.Len() != 0 {
		t.Fatal("duplicate MD5 source group accepted")
	}
	if validateDirectSupportFlags(true, false, true) == nil || validateDirectSupportFlags(false, true) != nil {
		t.Fatal("output mode validation failed")
	}
}
