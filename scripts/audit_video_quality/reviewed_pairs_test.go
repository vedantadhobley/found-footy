// reviewed_pairs_test.go — Executable FF-081 human-review regression corpus.
package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

type reviewedPairCorpus struct {
	SchemaVersion int             `json:"schema_version"`
	CapturedAt    string          `json:"captured_at"`
	HashCadenceMS int             `json:"hash_cadence_ms"`
	Matcher       reviewedMatcher `json:"matcher"`
	Cases         []reviewedPair  `json:"cases"`
}

type reviewedMatcher struct {
	Primary   reviewedMatchRoute `json:"primary"`
	Sustained reviewedMatchRoute `json:"sustained"`
}

type reviewedMatchRoute struct {
	MaxHamming int `json:"max_hamming"`
	MinRun     int `json:"min_run"`
	MaxGaps    int `json:"max_gaps"`
}

type reviewedPair struct {
	ID         string                 `json:"id"`
	EventLabel string                 `json:"event_label"`
	Left       reviewedAsset          `json:"left"`
	Right      reviewedAsset          `json:"right"`
	Human      reviewedHumanJudgment  `json:"human"`
	Current    reviewedCurrentOutcome `json:"current"`
}

type reviewedAsset struct {
	AssetID     string  `json:"asset_id"`
	EventID     string  `json:"event_id"`
	HashVersion string  `json:"hash_version"`
	FrameHashes string  `json:"frame_hashes_hex"`
	Width       int     `json:"width"`
	Height      int     `json:"height"`
	DurationMS  int     `json:"duration_ms"`
	Bitrate     int     `json:"bitrate"`
	FrameRate   float64 `json:"frame_rate"`
	Popularity  int     `json:"popularity_at_capture"`
}

type reviewedHumanJudgment struct {
	DedupDecision string   `json:"dedup_decision"`
	QualityWinner string   `json:"quality_winner"`
	Reasons       []string `json:"reasons"`
	Notes         string   `json:"notes"`
}

type reviewedCurrentOutcome struct {
	Matches           bool   `json:"matches"`
	QualityPreference string `json:"quality_preference"`
}

// TestReviewedPairCorpusReplaysCurrentPolicy keeps production-derived hashes
// and metadata executable without retaining copyrighted media. Current output
// is a snapshot beside, never a substitute for, the human judgment.
func TestReviewedPairCorpusReplaysCurrentPolicy(t *testing.T) {
	corpus := loadReviewedPairCorpus(t)
	wantMatcher := reviewedMatcher{
		Primary: reviewedMatchRoute{
			MaxHamming: primaryMaxHamming, MinRun: primaryMinRun, MaxGaps: primaryMaxGaps,
		},
		Sustained: reviewedMatchRoute{
			MaxHamming: longMaxHamming, MinRun: longMinRun, MaxGaps: longMaxGaps,
		},
	}
	if !reflect.DeepEqual(corpus.Matcher, wantMatcher) {
		t.Fatalf("fixture matcher = %+v, current audit matcher = %+v", corpus.Matcher, wantMatcher)
	}

	seenIDs := make(map[string]struct{}, len(corpus.Cases))
	for _, pair := range corpus.Cases {
		pair := pair
		t.Run(pair.ID, func(t *testing.T) {
			if pair.ID == "" || pair.EventLabel == "" {
				t.Fatal("pair identity and event label must be present")
			}
			if _, exists := seenIDs[pair.ID]; exists {
				t.Fatalf("duplicate case ID %q", pair.ID)
			}
			seenIDs[pair.ID] = struct{}{}
			if pair.Left.AssetID == pair.Right.AssetID || pair.Left.EventID != pair.Right.EventID {
				t.Fatalf("invalid asset pair %q/%q in events %q/%q",
					pair.Left.AssetID, pair.Right.AssetID, pair.Left.EventID, pair.Right.EventID)
			}
			validateReviewedAsset(t, pair.Left)
			validateReviewedAsset(t, pair.Right)

			leftHashes, err := decodeFrameHashes(pair.Left.FrameHashes)
			if err != nil {
				t.Fatalf("decode left hashes: %v", err)
			}
			rightHashes, err := decodeFrameHashes(pair.Right.FrameHashes)
			if err != nil {
				t.Fatalf("decode right hashes: %v", err)
			}
			matches := dvideo.Match(
				leftHashes, rightHashes, primaryMaxHamming, primaryMinRun, primaryMaxGaps,
			) || dvideo.Match(
				leftHashes, rightHashes, longMaxHamming, longMinRun, longMaxGaps,
			)
			if matches != pair.Current.Matches {
				t.Errorf("current match = %t, snapshot = %t", matches, pair.Current.Matches)
			}

			left := reviewedAssetForPolicy(pair.Left)
			right := reviewedAssetForPolicy(pair.Right)
			if preference := currentPreference(left, right); preference != pair.Current.QualityPreference {
				t.Errorf("current quality preference = %q, snapshot = %q",
					preference, pair.Current.QualityPreference)
			}
		})
	}
}

// TestReviewedPairCorpusPreservesHumanJudgments validates the product labels
// independently of current behavior and requires coverage for both known
// shortcomings and accepted non-duplicates.
func TestReviewedPairCorpusPreservesHumanJudgments(t *testing.T) {
	corpus := loadReviewedPairCorpus(t)
	dedupDecisions := map[string]bool{"collapse": true, "keep_both": true, "uncertain": true}
	qualityWinners := map[string]bool{
		"left": true, "right": true, "tie": true, "not_applicable": true, "uncertain": true,
	}
	qualityPreferences := map[string]bool{
		"left": true, "right": true, "incumbent_tie": true, "inconsistent": true,
	}

	var hasMatcherMiss, hasComparatorDisagreement, hasDirectMatch, hasSeparate, hasUncertain bool
	for _, pair := range corpus.Cases {
		judgment := pair.Human
		if !dedupDecisions[judgment.DedupDecision] {
			t.Errorf("%s: invalid dedup decision %q", pair.ID, judgment.DedupDecision)
		}
		if !qualityWinners[judgment.QualityWinner] {
			t.Errorf("%s: invalid quality winner %q", pair.ID, judgment.QualityWinner)
		}
		if !qualityPreferences[pair.Current.QualityPreference] {
			t.Errorf("%s: invalid current preference %q", pair.ID, pair.Current.QualityPreference)
		}
		if len(judgment.Reasons) == 0 {
			t.Errorf("%s: human judgment has no reasons", pair.ID)
		}
		switch judgment.DedupDecision {
		case "keep_both":
			hasSeparate = true
			if judgment.QualityWinner != "not_applicable" {
				t.Errorf("%s: separate presentations have quality winner %q",
					pair.ID, judgment.QualityWinner)
			}
		case "uncertain":
			if judgment.QualityWinner != "uncertain" {
				t.Errorf("%s: uncertain identity has quality winner %q", pair.ID, judgment.QualityWinner)
			}
		}
		if judgment.DedupDecision == "collapse" && !pair.Current.Matches {
			hasMatcherMiss = true
		}
		if pair.Current.Matches {
			hasDirectMatch = true
		}
		if (judgment.QualityWinner == "left" || judgment.QualityWinner == "right") &&
			judgment.QualityWinner != pair.Current.QualityPreference {
			hasComparatorDisagreement = true
		}
		if judgment.QualityWinner == "uncertain" {
			hasUncertain = true
		}
	}
	if !hasMatcherMiss || !hasComparatorDisagreement || !hasDirectMatch || !hasSeparate || !hasUncertain {
		t.Fatalf("coverage matcher_miss=%t comparator_disagreement=%t direct_match=%t separate=%t uncertain=%t",
			hasMatcherMiss, hasComparatorDisagreement, hasDirectMatch, hasSeparate, hasUncertain)
	}
}

// TestStableOffsetPolicyExplainsReviewedMbappeBoundary pins the first accepted
// direct-match case used to evaluate aggregate coverage. Aggregation correctly
// recognizes the longer clip as containing the short cut, while the current
// experimental per-frame compression floor remains more conservative than the
// human quality judgment.
func TestStableOffsetPolicyExplainsReviewedMbappeBoundary(t *testing.T) {
	corpus := loadReviewedPairCorpus(t)
	for _, pair := range corpus.Cases {
		if pair.ID != "mbappe-80-overlay-short-cut" {
			continue
		}
		leftHashes, err := decodeFrameHashes(pair.Left.FrameHashes)
		if err != nil {
			t.Fatal(err)
		}
		rightHashes, err := decodeFrameHashes(pair.Right.FrameHashes)
		if err != nil {
			t.Fatal(err)
		}
		left, right := reviewedAssetForPolicy(pair.Left), reviewedAssetForPolicy(pair.Right)
		left.frameHashes, right.frameHashes = leftHashes, rightHashes
		measured := measureMatcherEvidence(left, right)
		if !measured.matches() {
			t.Fatal("reviewed Mbappe pair must remain a current direct match")
		}
		decision := evaluateStableOffsetSubstitution(left, right, measured)
		if decision.coverageClass != coverageRightContainsLeft {
			t.Fatalf("stable coverage = %s, want longer right clip to contain left", decision.coverageClass)
		}
		if decision.action() != "keep_both" || pair.Human.QualityWinner != "right" {
			t.Fatalf("experimental action/human winner = %s/%s, want measured keep_both versus right",
				decision.action(), pair.Human.QualityWinner)
		}
		cadenceAware := evaluateCadenceAwareSubstitution(left, right, measured)
		if cadenceAware.action() != "collapse_left" {
			t.Fatalf("cadence-aware action = %s, want longer human-preferred right clip",
				cadenceAware.action())
		}
		return
	}
	t.Fatal("reviewed Mbappe pair missing")
}

func loadReviewedPairCorpus(t *testing.T) reviewedPairCorpus {
	t.Helper()
	raw, err := os.ReadFile("testdata/reviewed-pairs.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus reviewedPairCorpus
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	if corpus.SchemaVersion != 1 || corpus.CapturedAt == "" || corpus.HashCadenceMS != 100 {
		t.Fatalf("invalid corpus envelope: version=%d captured_at=%q cadence_ms=%d",
			corpus.SchemaVersion, corpus.CapturedAt, corpus.HashCadenceMS)
	}
	if len(corpus.Cases) == 0 {
		t.Fatal("reviewed corpus has no cases")
	}
	return corpus
}

func validateReviewedAsset(t *testing.T, item reviewedAsset) {
	t.Helper()
	if item.AssetID == "" || item.EventID == "" || item.HashVersion == "" {
		t.Fatal("asset identity and hash version must be present")
	}
	if item.Width <= 0 || item.Height <= 0 || item.DurationMS <= 0 || item.Bitrate <= 0 ||
		item.FrameRate <= 0 || item.Popularity <= 0 {
		t.Fatalf("invalid retained metadata for asset %s: %+v", item.AssetID, item)
	}
}

func reviewedAssetForPolicy(item reviewedAsset) asset {
	return asset{
		id: item.AssetID, eventID: item.EventID,
		hashVersion: dvideo.NormalizeFrameHashVersion(dvideo.FrameHashVersion(item.HashVersion)),
		width:       item.Width, height: item.Height, durationMS: item.DurationMS,
		bitrate: item.Bitrate, frameRate: item.FrameRate, popularity: item.Popularity,
	}
}
