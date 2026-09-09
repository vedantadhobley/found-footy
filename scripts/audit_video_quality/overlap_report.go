// overlap_report.go — Streaming overlap evidence beside unchanged quality experiments.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

// overlapBaselines retain prior experiments so better coverage cannot silently
// become a new quality judgment or erase a known comparator disagreement.
type overlapBaselines struct {
	ContiguousAction    string        `json:"contiguous_action"`
	StableAction        string        `json:"stable_action"`
	CadenceAction       string        `json:"cadence_action"`
	StableClass         coverageClass `json:"stable_class"`
	StableRoute         string        `json:"stable_route"`
	StableLeftCoverage  float64       `json:"stable_left_span_coverage"`
	StableRightCoverage float64       `json:"stable_right_span_coverage"`
	StableSimilarity    float64       `json:"stable_similarity"`
}

// overlapReport is one NDJSON row. It has no inferred keeper/containment field.
// Sample coordinates cover hash bins, not exact audiovisual cut boundaries.
type overlapReport struct {
	Experiment           string                 `json:"experiment"`
	HashCadenceMS        *int                   `json:"hash_cadence_ms"`
	MaxGapFrames         int                    `json:"max_gap_frames"`
	MinSectionFrames     int                    `json:"min_section_frames"`
	MinSectionSimilarity float64                `json:"min_section_similarity"`
	PairID               string                 `json:"pair_id"`
	EventLabel           string                 `json:"event_label"`
	CapturedAt           string                 `json:"captured_at,omitempty"`
	Left                 reviewedAsset          `json:"left"`
	Right                reviewedAsset          `json:"right"`
	Current              reviewedCurrentOutcome `json:"current"`
	Baselines            overlapBaselines       `json:"baselines"`
	Human                *reviewedHumanJudgment `json:"human"`
	Routes               []routeOverlap         `json:"routes"`
}

// makeOverlapReport measures content support without changing any baseline.
func makeOverlapReport(left, right asset, evidence matcherEvidence) overlapReport {
	stable := evaluateStableOffsetSubstitution(left, right, evidence)
	stableEvidence := measureStableOffset(left, right, evidence)
	var cadenceMS *int
	if left.hashVersion == dvideo.CurrentFrameHashVersion(0.1) {
		value := 100
		cadenceMS = &value
	}
	return overlapReport{
		Experiment: "aligned-sections-v1", HashCadenceMS: cadenceMS,
		MaxGapFrames: overlapGapFrames, MinSectionFrames: overlapMinFrames,
		MinSectionSimilarity: overlapMinSimilarity,
		PairID:               pairKey(left.id, right.id), EventLabel: componentName(componentFinding{assets: []asset{left}}),
		Left: overlapAssetMetadata(left), Right: overlapAssetMetadata(right),
		Current: reviewedCurrentOutcome{Matches: evidence.matches(), QualityPreference: currentPreference(left, right)},
		Baselines: overlapBaselines{
			ContiguousAction: evaluateSubstitution(left, right, evidence).action(),
			StableAction:     stable.action(), CadenceAction: evaluateCadenceAwareSubstitution(left, right, evidence).action(),
			StableClass: stable.coverageClass, StableRoute: stableEvidence.route, StableLeftCoverage: stable.leftCoverage,
			StableRightCoverage: stable.rightCoverage, StableSimilarity: stableEvidence.similarity,
		},
		Routes: measureOverlap(left, right, evidence),
	}
}

// overlapAssetMetadata omits source URLs and the large raw hash array while
// retaining encoded cadence and exact popularity as distinct evidence.
func overlapAssetMetadata(a asset) reviewedAsset {
	return reviewedAsset{
		AssetID: a.id, EventID: a.eventID, HashVersion: string(a.hashVersion),
		Width: a.width, Height: a.height, DurationMS: a.durationMS,
		Bitrate: a.bitrate, FrameRate: a.frameRate, Popularity: a.popularity,
		ExactObservations: a.observedPopularity,
	}
}

// writeOverlapJSON measures scoped direct pairs without running graph-order
// permutations or selecting a new public set. Input order cannot reorder rows.
func writeOverlapJSON(w io.Writer, assets []asset) error {
	assets = append([]asset(nil), assets...)
	sort.Slice(assets, func(i, j int) bool {
		if assets[i].eventID != assets[j].eventID {
			return assets[i].eventID < assets[j].eventID
		}
		return assets[i].id < assets[j].id
	})
	for _, item := range assets {
		if item.hashVersion != dvideo.CurrentFrameHashVersion(0.1) && item.hashVersion != dvideo.LegacyFrameHashVersion {
			return fmt.Errorf("overlap report supports 100-ms v2 or unknown-cadence legacy hashes: asset %s has %q", item.id, item.hashVersion)
		}
	}
	encoder := json.NewEncoder(w)
	for i, left := range assets {
		for _, right := range assets[i+1:] {
			if right.eventID != left.eventID {
				break
			}
			if right.verified != left.verified || right.hashVersion != left.hashVersion {
				continue
			}
			evidence := measureMatcherEvidence(left, right)
			if !evidence.matches() {
				continue
			}
			if err := encoder.Encode(makeOverlapReport(left, right, evidence)); err != nil {
				return fmt.Errorf("write overlap pair %s: %w", pairKey(left.id, right.id), err)
			}
		}
	}
	return nil
}

// writePairCorpusOverlapJSON includes curated non-matches and passes human
// labels through unchanged. Corpus side order must not be sorted by asset ID.
func writePairCorpusOverlapJSON(w io.Writer, r io.Reader) error {
	const maxCorpusBytes = 32 << 20
	raw, err := io.ReadAll(io.LimitReader(r, maxCorpusBytes+1))
	if err != nil {
		return fmt.Errorf("read pair corpus: %w", err)
	}
	if len(raw) > maxCorpusBytes {
		return fmt.Errorf("pair corpus exceeds 32 MiB")
	}
	var corpus reviewedPairCorpus
	if err := json.Unmarshal(raw, &corpus); err != nil {
		return fmt.Errorf("decode pair corpus: %w", err)
	}
	wantMatcher := reviewedMatcher{
		Primary:   reviewedMatchRoute{primaryMaxHamming, primaryMinRun, primaryMaxGaps},
		Sustained: reviewedMatchRoute{longMaxHamming, longMinRun, longMaxGaps},
	}
	if corpus.SchemaVersion != 1 || corpus.HashCadenceMS != 100 || corpus.CapturedAt == "" ||
		corpus.Matcher != wantMatcher || len(corpus.Cases) == 0 {
		return fmt.Errorf("pair corpus requires schema v1, capture date, 100-ms hashes, current matcher and nonempty cases")
	}
	encoder := json.NewEncoder(w)
	seen := make(map[string]bool)
	for _, pair := range corpus.Cases {
		if pair.ID == "" || seen[pair.ID] || pair.Left.AssetID == "" || pair.Right.AssetID == "" ||
			pair.Left.AssetID == pair.Right.AssetID || pair.Left.EventID == "" || pair.Left.EventID != pair.Right.EventID {
			return fmt.Errorf("invalid pair identity %q", pair.ID)
		}
		seen[pair.ID] = true
		left, right := reviewedAssetForPolicy(pair.Left), reviewedAssetForPolicy(pair.Right)
		if left.hashVersion != dvideo.CurrentFrameHashVersion(0.1) || left.hashVersion != right.hashVersion {
			return fmt.Errorf("pair %s requires comparable explicit 100-ms v2 hashes", pair.ID)
		}
		left.frameHashes, err = decodeFrameHashes(pair.Left.FrameHashes)
		if err != nil {
			return fmt.Errorf("pair %s left: %w", pair.ID, err)
		}
		right.frameHashes, err = decodeFrameHashes(pair.Right.FrameHashes)
		if err != nil {
			return fmt.Errorf("pair %s right: %w", pair.ID, err)
		}
		row := makeOverlapReport(left, right, measureMatcherEvidence(left, right))
		if row.Current != pair.Current {
			return fmt.Errorf("pair %s current-policy snapshot drift", pair.ID)
		}
		row.PairID, row.EventLabel, row.CapturedAt = pair.ID, pair.EventLabel, corpus.CapturedAt
		row.Left.SHA256, row.Right.SHA256 = pair.Left.SHA256, pair.Right.SHA256
		if pair.Human.DedupDecision != "" {
			row.Human = &pair.Human
		}
		if err := encoder.Encode(row); err != nil {
			return fmt.Errorf("write pair %s: %w", pair.ID, err)
		}
	}
	return nil
}
