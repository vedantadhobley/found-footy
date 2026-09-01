// review.go — Stable direct-pair manifest for human FF-081 judgments.
package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

var reviewHeader = []string{
	"pair_id", "priority", "fixture_id", "event_id", "match", "player", "minute", "verified",
	"component_size", "component_bridge_nodes", "component_order_sensitive", "component_quality_cycles",
	"primary_window", "long_window", "best_route", "aligned_frames", "left_start_frame", "right_start_frame",
	"aligned_gaps", "left_coverage", "right_coverage", "coverage_class", "experimental_action",
	"stable_route", "stable_left_start_frame", "stable_right_start_frame", "stable_overlap_frames",
	"stable_similar_frames", "stable_similarity", "stable_left_coverage", "stable_right_coverage",
	"stable_coverage_class", "stable_experimental_action", "cadence_aware_action",
	"left_direct_cover", "right_direct_cover", "direct_cover_alternatives",
	"left_asset_id", "left_share_id", "left_share_state", "left_source_tweet_url", "left_terminal",
	"left_duration_ms", "left_width", "left_height", "left_bitrate", "left_frame_rate",
	"left_spatial_bitrate_density", "left_bits_per_pixel_frame", "left_popularity", "left_exact_observations",
	"right_asset_id", "right_share_id", "right_share_state", "right_source_tweet_url", "right_terminal",
	"right_duration_ms", "right_width", "right_height", "right_bitrate", "right_frame_rate",
	"right_spatial_bitrate_density", "right_bits_per_pixel_frame", "right_popularity", "right_exact_observations",
	"current_preference", "dedup_decision", "quality_winner", "quality_reasons", "notes",
}

// writeReviewCSV emits one row for every direct perceptual match. Reviewers
// fill only the final four columns; every preceding column is reproducible
// evidence or current-policy context.
func writeReviewCSV(w io.Writer, result auditResult) error {
	encoder := csv.NewWriter(w)
	if err := encoder.Write(reviewHeader); err != nil {
		return fmt.Errorf("write review header: %w", err)
	}
	for _, finding := range result.findings {
		assets := make(map[string]asset, len(finding.assets))
		for _, item := range finding.assets {
			assets[item.id] = item
		}
		for _, edge := range finding.matchEdges {
			left, right := assets[edge.leftID], assets[edge.rightID]
			evidence := edge.evidence
			if left.id > right.id {
				left, right = right, left
				evidence = evidence.swapped()
			}
			if err := encoder.Write(reviewRow(finding, evidence, left, right)); err != nil {
				return fmt.Errorf("write review pair %s: %w", pairKey(left.id, right.id), err)
			}
		}
	}
	encoder.Flush()
	if err := encoder.Error(); err != nil {
		return fmt.Errorf("flush review manifest: %w", err)
	}
	return nil
}

// reviewRow flattens one direct match and leaves reviewer-owned fields blank.
func reviewRow(finding componentFinding, evidence matcherEvidence, left, right asset) []string {
	route, alignment := evidence.strongest()
	decision := evaluateSubstitution(left, right, evidence)
	stable := measureStableOffset(left, right, evidence)
	stableDecision := evaluateStableOffsetSubstitution(left, right, evidence)
	cadenceDecision := evaluateCadenceAwareSubstitution(left, right, evidence)
	return []string{
		pairKey(left.id, right.id), strconv.Itoa(findingPriority(finding)), strconv.FormatInt(left.fixtureID, 10),
		left.eventID, left.homeTeam + " - " + left.awayTeam, left.playerName, eventMinute(left),
		strconv.FormatBool(left.verified), strconv.Itoa(len(finding.assets)), strconv.Itoa(finding.bridgeNodes),
		strconv.FormatBool(len(finding.outcomes) > 1), strconv.Itoa(finding.qualityCycles),
		strconv.Itoa(evidence.primary.Frames), strconv.Itoa(evidence.long.Frames), route,
		strconv.Itoa(alignment.Frames), strconv.Itoa(alignment.LeftStart), strconv.Itoa(alignment.RightStart),
		strconv.Itoa(alignment.Gaps), formatFloat(decision.leftCoverage), formatFloat(decision.rightCoverage),
		string(decision.coverageClass), decision.action(),
		stable.route, strconv.Itoa(stable.leftStart), strconv.Itoa(stable.rightStart),
		strconv.Itoa(stable.overlapFrames), strconv.Itoa(stable.similarFrames), formatFloat(stable.similarity),
		formatFloat(stableDecision.leftCoverage), formatFloat(stableDecision.rightCoverage),
		string(stableDecision.coverageClass), stableDecision.action(), cadenceDecision.action(),
		strconv.FormatBool(containsString(finding.directCoverIDs, left.id)),
		strconv.FormatBool(containsString(finding.directCoverIDs, right.id)),
		strconv.Itoa(finding.directCoverAlternatives),
		left.id, left.shareID, left.shareState, left.sourceTweetURL, strconv.FormatBool(left.supersededBy == ""),
		strconv.Itoa(left.durationMS), strconv.Itoa(left.width), strconv.Itoa(left.height), strconv.Itoa(left.bitrate),
		formatFloat(left.frameRate), formatFloat(left.spatialBitrateDensity()), formatFloat(left.bitsPerPixelFrame()),
		strconv.Itoa(left.popularity), strconv.Itoa(left.observedPopularity),
		right.id, right.shareID, right.shareState, right.sourceTweetURL, strconv.FormatBool(right.supersededBy == ""),
		strconv.Itoa(right.durationMS), strconv.Itoa(right.width), strconv.Itoa(right.height), strconv.Itoa(right.bitrate),
		formatFloat(right.frameRate), formatFloat(right.spatialBitrateDensity()), formatFloat(right.bitsPerPixelFrame()),
		strconv.Itoa(right.popularity), strconv.Itoa(right.observedPopularity),
		currentPreference(left, right), "", "", "", "",
	}
}

// currentPreference describes the existing pairwise policy without treating it
// as a label. A tie means the earlier incumbent survives.
func currentPreference(left, right asset) string {
	leftWins := dvideo.IsUpgrade(left.quality(), right.quality())
	rightWins := dvideo.IsUpgrade(right.quality(), left.quality())
	switch {
	case leftWins && !rightWins:
		return "left"
	case rightWins && !leftWins:
		return "right"
	case leftWins && rightWins:
		return "inconsistent"
	default:
		return "incumbent_tie"
	}
}

// eventMinute renders regulation and added-time event minutes for a reviewer.
func eventMinute(item asset) string {
	if item.extra == "" {
		return strconv.Itoa(item.minute)
	}
	return strconv.Itoa(item.minute) + "+" + item.extra
}

// formatFloat emits a stable decimal while preserving unknown values as blank.
func formatFloat(value float64) string {
	if value <= 0 {
		return ""
	}
	return strconv.FormatFloat(value, 'f', 6, 64)
}
