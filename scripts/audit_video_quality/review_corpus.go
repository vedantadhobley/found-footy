// review_corpus.go — Shared non-media evidence schema for offline reports and regressions.
package main

import dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"

// reviewedPairCorpus preserves independent human and observed-policy results.
type reviewedPairCorpus struct {
	SchemaVersion int             `json:"schema_version"`
	CapturedAt    string          `json:"captured_at"`
	HashCadenceMS int             `json:"hash_cadence_ms"`
	Matcher       reviewedMatcher `json:"matcher"`
	Cases         []reviewedPair  `json:"cases"`
}

// reviewedMatcher pins the two evidence routes used at capture.
type reviewedMatcher struct {
	Primary   reviewedMatchRoute `json:"primary"`
	Sustained reviewedMatchRoute `json:"sustained"`
}

// reviewedMatchRoute keeps frame-level similarity separate from window length.
type reviewedMatchRoute struct {
	MaxHamming int `json:"max_hamming"`
	MinRun     int `json:"min_run"`
	MaxGaps    int `json:"max_gaps"`
}

// reviewedPair keeps side order stable so a human's left/right label survives.
type reviewedPair struct {
	ID         string                 `json:"id"`
	EventLabel string                 `json:"event_label"`
	Left       reviewedAsset          `json:"left"`
	Right      reviewedAsset          `json:"right"`
	Human      reviewedHumanJudgment  `json:"human"`
	Current    reviewedCurrentOutcome `json:"current"`
}

// reviewedAsset contains derived evidence, never media or source URLs.
type reviewedAsset struct {
	AssetID           string  `json:"asset_id"`
	EventID           string  `json:"event_id"`
	HashVersion       string  `json:"hash_version"`
	FrameHashes       string  `json:"frame_hashes_hex,omitempty"`
	Width             int     `json:"width"`
	Height            int     `json:"height"`
	DurationMS        int     `json:"duration_ms"`
	Bitrate           int     `json:"bitrate"`
	FrameRate         float64 `json:"frame_rate"`
	Popularity        int     `json:"popularity_at_capture"`
	ExactObservations int     `json:"exact_observations_at_capture,omitempty"`
	SHA256            string  `json:"sha256,omitempty"`
}

// reviewedHumanJudgment is editorial ground truth, not inferred from metadata.
type reviewedHumanJudgment struct {
	DedupDecision string   `json:"dedup_decision"`
	QualityWinner string   `json:"quality_winner"`
	Reasons       []string `json:"reasons"`
	Notes         string   `json:"notes"`
	ReviewedBy    string   `json:"reviewed_by,omitempty"`
	ReviewedAt    string   `json:"reviewed_at,omitempty"`
}

// reviewedCurrentOutcome describes behavior without endorsing its result.
type reviewedCurrentOutcome struct {
	Matches           bool   `json:"matches"`
	QualityPreference string `json:"quality_preference"`
}

// reviewedAssetForPolicy projects existing inputs without changing their units.
func reviewedAssetForPolicy(item reviewedAsset) asset {
	return asset{
		id: item.AssetID, eventID: item.EventID,
		hashVersion: dvideo.NormalizeFrameHashVersion(dvideo.FrameHashVersion(item.HashVersion)),
		width:       item.Width, height: item.Height, durationMS: item.DurationMS,
		bitrate: item.Bitrate, frameRate: item.FrameRate, popularity: item.Popularity,
		observedPopularity: item.ExactObservations,
	}
}
