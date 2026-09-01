// substitution.go — Experimental shared-footage coverage and conservative
// substitution policy for FF-081's read-only corpus replay.
package main

import (
	"math"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

const (
	// wholeClipCoverage is intentionally conservative: a dHash edge must align
	// at least 90% of a clip before the experiment treats that clip as covered.
	wholeClipCoverage = 0.90
	// qualityRetentionFloor permits only a bounded 10% loss in every available
	// technical dimension. It is an experiment parameter, not production policy.
	qualityRetentionFloor = 0.90
	// stableOffsetCoverage lowers the whole-clip boundary because a qualified
	// offset can leave short intros/outros outside the shared timeline.
	stableOffsetCoverage = 0.80
	// stableOffsetSimilarity requires three quarters of the complete aligned
	// timeline to satisfy the route's frame-level Hamming threshold.
	stableOffsetSimilarity = 0.75
)

type coverageClass string

const (
	coverageEquivalent        coverageClass = "equivalent"
	coverageLeftContainsRight coverageClass = "left_contains_right"
	coverageRightContainsLeft coverageClass = "right_contains_left"
	coveragePartial           coverageClass = "partial_overlap"
)

// matcherEvidence keeps the strongest alignment from both production routes.
// A route's evidence remains useful even when it falls below that route's
// minimum run, but only a qualifying route can establish a current match.
type matcherEvidence struct {
	primary dvideo.AlignmentEvidence
	long    dvideo.AlignmentEvidence
}

func measureMatcherEvidence(left, right asset) matcherEvidence {
	return matcherEvidence{
		primary: dvideo.BestAlignment(
			left.frameHashes, right.frameHashes, primaryMaxHamming, primaryMaxGaps,
		),
		long: dvideo.BestAlignment(
			left.frameHashes, right.frameHashes, longMaxHamming, longMaxGaps,
		),
	}
}

func (e matcherEvidence) matches() bool {
	return e.primary.Frames >= primaryMinRun || e.long.Frames >= longMinRun
}

// strongest returns the longest qualifying span. Equal spans prefer the
// stricter primary route so the diagnostic label remains conservative.
func (e matcherEvidence) strongest() (string, dvideo.AlignmentEvidence) {
	primaryMatches := e.primary.Frames >= primaryMinRun
	longMatches := e.long.Frames >= longMinRun
	switch {
	case primaryMatches && (!longMatches || e.primary.Frames >= e.long.Frames):
		return "primary", e.primary
	case longMatches:
		return "sustained", e.long
	case e.primary.Frames >= e.long.Frames:
		return "none", e.primary
	default:
		return "none", e.long
	}
}

func (e matcherEvidence) swapped() matcherEvidence {
	return matcherEvidence{primary: swapAlignment(e.primary), long: swapAlignment(e.long)}
}

func swapAlignment(e dvideo.AlignmentEvidence) dvideo.AlignmentEvidence {
	e.LeftStart, e.RightStart = e.RightStart, e.LeftStart
	return e
}

// substitutionDecision distinguishes content coverage from technical quality.
// Direction means the named presentation is sufficient to hide the other one.
type substitutionDecision struct {
	coverageClass        coverageClass
	leftCoverage         float64
	rightCoverage        float64
	leftSubstitutesRight bool
	rightSubstitutesLeft bool
}

// stableOffsetEvidence aggregates every frame comparison at one offset already
// anchored by a production-qualified contiguous window. It can recover several
// matching regions split by overlays or edits without combining unrelated
// offsets or changing which pairs production considers matches.
type stableOffsetEvidence struct {
	route         string
	leftStart     int
	rightStart    int
	overlapFrames int
	similarFrames int
	similarity    float64
}

func (d substitutionDecision) action() string {
	switch {
	case d.leftSubstitutesRight && d.rightSubstitutesLeft:
		return "collapse_equivalent"
	case d.leftSubstitutesRight:
		return "collapse_right"
	case d.rightSubstitutesLeft:
		return "collapse_left"
	default:
		return "keep_both"
	}
}

func evaluateSubstitution(left, right asset, measured matcherEvidence) substitutionDecision {
	_, alignment := measured.strongest()
	decision := substitutionDecision{
		leftCoverage:  coverage(alignment.Frames, len(left.frameHashes)),
		rightCoverage: coverage(alignment.Frames, len(right.frameHashes)),
	}
	leftCovered := decision.leftCoverage >= wholeClipCoverage
	rightCovered := decision.rightCoverage >= wholeClipCoverage
	switch {
	case leftCovered && rightCovered:
		decision.coverageClass = coverageEquivalent
		decision.leftSubstitutesRight = notMateriallyWorse(left, right)
		decision.rightSubstitutesLeft = notMateriallyWorse(right, left)
	case rightCovered:
		decision.coverageClass = coverageLeftContainsRight
		decision.leftSubstitutesRight = notMateriallyWorse(left, right)
	case leftCovered:
		decision.coverageClass = coverageRightContainsLeft
		decision.rightSubstitutesLeft = notMateriallyWorse(right, left)
	default:
		decision.coverageClass = coveragePartial
	}
	return decision
}

func evaluateStableOffsetSubstitution(left, right asset, measured matcherEvidence) substitutionDecision {
	return evaluateStableOffsetWithQuality(left, right, measured, notMateriallyWorse)
}

func evaluateCadenceAwareSubstitution(left, right asset, measured matcherEvidence) substitutionDecision {
	return evaluateStableOffsetWithQuality(left, right, measured, notMateriallyWorseCadenceAware)
}

func evaluateStableOffsetWithQuality(
	left, right asset,
	measured matcherEvidence,
	notWorse func(asset, asset) bool,
) substitutionDecision {
	aggregate := measureStableOffset(left, right, measured)
	decision := substitutionDecision{
		leftCoverage:  coverage(aggregate.overlapFrames, len(left.frameHashes)),
		rightCoverage: coverage(aggregate.overlapFrames, len(right.frameHashes)),
	}
	if aggregate.similarity < stableOffsetSimilarity {
		decision.coverageClass = coveragePartial
		return decision
	}
	leftCovered := decision.leftCoverage >= stableOffsetCoverage
	rightCovered := decision.rightCoverage >= stableOffsetCoverage
	switch {
	case leftCovered && rightCovered:
		decision.coverageClass = coverageEquivalent
		decision.leftSubstitutesRight = notWorse(left, right)
		decision.rightSubstitutesLeft = notWorse(right, left)
	case rightCovered:
		decision.coverageClass = coverageLeftContainsRight
		decision.leftSubstitutesRight = notWorse(left, right)
	case leftCovered:
		decision.coverageClass = coverageRightContainsLeft
		decision.rightSubstitutesLeft = notWorse(right, left)
	default:
		decision.coverageClass = coveragePartial
	}
	return decision
}

func measureStableOffset(left, right asset, measured matcherEvidence) stableOffsetEvidence {
	var best stableOffsetEvidence
	if measured.primary.Frames >= primaryMinRun {
		best = stableEvidenceAtOffset(
			left.frameHashes, right.frameHashes, "primary", measured.primary,
			primaryMaxHamming,
		)
	}
	if measured.long.Frames >= longMinRun {
		candidate := stableEvidenceAtOffset(
			left.frameHashes, right.frameHashes, "sustained", measured.long,
			longMaxHamming,
		)
		if strongerStableEvidence(candidate, best) {
			best = candidate
		}
	}
	return best
}

func stableEvidenceAtOffset(
	left, right []uint64,
	route string,
	anchor dvideo.AlignmentEvidence,
	maxHamming int,
) stableOffsetEvidence {
	offset := anchor.RightStart - anchor.LeftStart
	leftStart := max(0, -offset)
	leftEnd := min(len(left), len(right)-offset)
	evidence := stableOffsetEvidence{
		route: route, leftStart: leftStart, rightStart: leftStart + offset,
		overlapFrames: max(0, leftEnd-leftStart),
	}
	for leftIndex := leftStart; leftIndex < leftEnd; leftIndex++ {
		if dvideo.Hamming(left[leftIndex], right[leftIndex+offset]) <= maxHamming {
			evidence.similarFrames++
		}
	}
	if evidence.overlapFrames > 0 {
		evidence.similarity = float64(evidence.similarFrames) / float64(evidence.overlapFrames)
	}
	return evidence
}

func strongerStableEvidence(candidate, incumbent stableOffsetEvidence) bool {
	candidateQualifies := candidate.similarity >= stableOffsetSimilarity
	incumbentQualifies := incumbent.similarity >= stableOffsetSimilarity
	if candidateQualifies != incumbentQualifies {
		return candidateQualifies
	}
	if candidateQualifies && candidate.overlapFrames != incumbent.overlapFrames {
		return candidate.overlapFrames > incumbent.overlapFrames
	}
	if candidate.similarFrames != incumbent.similarFrames {
		return candidate.similarFrames > incumbent.similarFrames
	}
	if candidate.similarity != incumbent.similarity {
		return candidate.similarity > incumbent.similarity
	}
	if candidate.overlapFrames != incumbent.overlapFrames {
		return candidate.overlapFrames > incumbent.overlapFrames
	}
	return candidate.route == "primary" && incumbent.route != "primary"
}

func coverage(frames, total int) float64 {
	if frames <= 0 || total <= 0 {
		return 0
	}
	return math.Min(1, float64(frames)/float64(total))
}

// notMateriallyWorse applies no cross-dimension exchange rate. The candidate
// must retain at least the configured fraction of every available signal:
// resolution, cadence, and compression budget. Unknown on both sides is
// neutral; unknown on only one side cannot prove safe substitution.
func notMateriallyWorse(candidate, incumbent asset) bool {
	if !retainsSignal(float64(candidate.width*candidate.height), float64(incumbent.width*incumbent.height)) {
		return false
	}
	if !retainsSignal(candidate.frameRate, incumbent.frameRate) {
		return false
	}
	candidateCompression, incumbentCompression := candidate.spatialBitrateDensity(), incumbent.spatialBitrateDensity()
	if candidate.frameRate > 0 && incumbent.frameRate > 0 {
		candidateCompression = candidate.bitsPerPixelFrame()
		incumbentCompression = incumbent.bitsPerPixelFrame()
	}
	return retainsSignal(candidateCompression, incumbentCompression)
}

// notMateriallyWorseCadenceAware treats cadence and spatial compression as
// independent signals. Dividing density by frame rate falsely assumes each
// encoded frame consumes an independent bitrate budget; inter-frame codecs do
// not obey that model, and the reviewed 1080p50 Mbappe clip exposes the error.
func notMateriallyWorseCadenceAware(candidate, incumbent asset) bool {
	return retainsSignal(
		float64(candidate.width*candidate.height), float64(incumbent.width*incumbent.height),
	) && retainsSignal(candidate.frameRate, incumbent.frameRate) && retainsSignal(
		candidate.spatialBitrateDensity(), incumbent.spatialBitrateDensity(),
	)
}

func retainsSignal(candidate, incumbent float64) bool {
	switch {
	case candidate <= 0 && incumbent <= 0:
		return true
	case candidate <= 0 || incumbent <= 0:
		return false
	default:
		return candidate >= incumbent*qualityRetentionFloor
	}
}
