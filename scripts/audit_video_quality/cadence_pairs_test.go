// cadence_pairs_test.go — Replay natural FF-082/083 evidence separately from explicit user judgments.
package main

import (
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"
)

// cadenceReviewAsset adds source-copy provenance to metadata used by the policy.
// Probe diagnostics are retained in JSON for review, not promoted to quality scores.
type cadenceReviewAsset struct {
	reviewedAsset
	ShareState string `json:"share_state_at_capture"`
}

// cadenceSnapshot keeps experimental predictions separate from accepted labels.
type cadenceSnapshot struct {
	StableAction  string        `json:"stable_action"`
	CadenceAction string        `json:"cadence_action"`
	CoverageClass coverageClass `json:"coverage_class"`
	LeftCoverage  float64       `json:"left_coverage"`
	RightCoverage float64       `json:"right_coverage"`
	Similarity    float64       `json:"similarity"`
	LeftStart     int           `json:"left_start"`
	RightStart    int           `json:"right_start"`
	OverlapFrames int           `json:"overlap_frames"`
	SimilarFrames int           `json:"similar_frames"`
}

// TestNaturalCadencePairsReplayPolicySnapshots preserves what each policy does,
// not what a user should prefer. Human acceptance remains a separate review.
func TestNaturalCadencePairsReplayPolicySnapshots(t *testing.T) {
	raw, err := os.ReadFile("testdata/cadence-pairs-2026-09-08.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		SchemaVersion int             `json:"schema_version"`
		CapturedAt    string          `json:"captured_at"`
		HashCadenceMS int             `json:"hash_cadence_ms"`
		ReviewStatus  string          `json:"review_status"`
		Matcher       reviewedMatcher `json:"matcher"`
		Cases         []struct {
			ID           string                 `json:"id"`
			EventLabel   string                 `json:"event_label"`
			KeeperID     string                 `json:"keeper_id_at_capture"`
			Left         cadenceReviewAsset     `json:"left"`
			Right        cadenceReviewAsset     `json:"right"`
			Current      reviewedCurrentOutcome `json:"current"`
			Experimental cadenceSnapshot        `json:"experimental"`
			Human        *reviewedHumanJudgment `json:"human"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.SchemaVersion != 1 || corpus.CapturedAt == "" || corpus.HashCadenceMS != 100 ||
		corpus.ReviewStatus != "partially_reviewed" || len(corpus.Cases) != 5 {
		t.Fatal("invalid natural cadence evidence envelope")
	}
	wantMatcher := reviewedMatcher{
		Primary:   reviewedMatchRoute{MaxHamming: primaryMaxHamming, MinRun: primaryMinRun, MaxGaps: primaryMaxGaps},
		Sustained: reviewedMatchRoute{MaxHamming: longMaxHamming, MinRun: longMinRun, MaxGaps: longMaxGaps},
	}
	if !reflect.DeepEqual(corpus.Matcher, wantMatcher) {
		t.Fatalf("captured matcher = %+v, audit matcher = %+v", corpus.Matcher, wantMatcher)
	}
	seen := make(map[string]bool)
	for _, pair := range corpus.Cases {
		t.Run(pair.ID, func(t *testing.T) {
			if pair.ID == "" || pair.EventLabel == "" || seen[pair.ID] {
				t.Fatal("cases require unique identities")
			}
			seen[pair.ID] = true
			if pair.ID == "adams-82" {
				if pair.Human == nil || pair.Human.DedupDecision != "collapse" ||
					pair.Human.QualityWinner != "right" || len(pair.Human.Reasons) == 0 || pair.Human.Notes == "" {
					t.Fatal("Adams must preserve the user's shorter-right-copy judgment")
				}
			} else if pair.Human != nil {
				t.Fatal("remaining natural pairs have no accepted user judgment")
			}
			if pair.Left.AssetID == pair.Right.AssetID || pair.Left.EventID != pair.Right.EventID ||
				(pair.KeeperID != pair.Left.AssetID && pair.KeeperID != pair.Right.AssetID) {
				t.Fatal("invalid pair or captured keeper identity")
			}
			for _, item := range []cadenceReviewAsset{pair.Left, pair.Right} {
				validateReviewedAsset(t, item.reviewedAsset)
				checksum, err := hex.DecodeString(item.SHA256)
				if err != nil || len(checksum) != 32 || item.ExactObservations <= 0 {
					t.Fatalf("invalid evidence provenance for %s", item.AssetID)
				}
				if (item.AssetID == pair.KeeperID && item.ShareState != "active") ||
					(item.AssetID != pair.KeeperID && item.ShareState != "observed") {
					t.Fatalf("captured first-loss/share boundary changed for %s", item.AssetID)
				}
			}
			left, right := reviewedAssetForPolicy(pair.Left.reviewedAsset), reviewedAssetForPolicy(pair.Right.reviewedAsset)
			left.frameHashes, err = decodeFrameHashes(pair.Left.FrameHashes)
			if err != nil {
				t.Fatal(err)
			}
			right.frameHashes, err = decodeFrameHashes(pair.Right.FrameHashes)
			if err != nil {
				t.Fatal(err)
			}
			measured := measureMatcherEvidence(left, right)
			if measured.matches() != pair.Current.Matches || currentPreference(left, right) != pair.Current.QualityPreference {
				t.Fatalf("current snapshot changed: match=%t preference=%s", measured.matches(), currentPreference(left, right))
			}
			stable := evaluateStableOffsetSubstitution(left, right, measured)
			cadence := evaluateCadenceAwareSubstitution(left, right, measured)
			want := pair.Experimental
			if stable.action() != want.StableAction || cadence.action() != want.CadenceAction ||
				stable.coverageClass != want.CoverageClass {
				t.Fatalf("experimental snapshot changed: stable=%s cadence=%s coverage=%s",
					stable.action(), cadence.action(), stable.coverageClass)
			}
			overlap := measureStableOffset(left, right, measured)
			if math.Abs(stable.leftCoverage-want.LeftCoverage) > 0.000001 ||
				math.Abs(stable.rightCoverage-want.RightCoverage) > 0.000001 ||
				math.Abs(overlap.similarity-want.Similarity) > 0.000001 ||
				overlap.leftStart != want.LeftStart || overlap.rightStart != want.RightStart ||
				overlap.overlapFrames != want.OverlapFrames || overlap.similarFrames != want.SimilarFrames {
				t.Fatalf("stable-offset evidence changed: %+v", overlap)
			}
		})
	}
}
