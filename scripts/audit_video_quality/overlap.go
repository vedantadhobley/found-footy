// overlap.go — Offline aligned-section evidence, independent of keeper policy.
package main

import dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"

const (
	// These diagnostic boundaries join brief hash failures, not arbitrary cuts.
	overlapGapFrames     = 2
	overlapMinFrames     = 10
	overlapMinSimilarity = 0.80
)

// frameInterval is half-open on the stored sample timeline. Only explicit
// hash versions establish cadence; it never classifies the meaning of footage.
type frameInterval struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// alignedInterval preserves both positions instead of inventing a global
// video timeline or deriving transitive coverage through a third clip.
type alignedInterval struct {
	Left  frameInterval `json:"left"`
	Right frameInterval `json:"right"`
}

// overlapSection joins nearby hash hits but keeps every tolerated miss visible.
type overlapSection struct {
	alignedInterval
	SimilarFrames int               `json:"similar_frames"`
	ToleratedGaps []alignedInterval `json:"tolerated_gaps"`
}

// overlapSide distinguishes sampled hash support from the larger section span.
// Unsupported footage may be a matching failure; it is not proven new content.
type overlapSide struct {
	TotalFrames             int             `json:"total_frames"`
	SupportedCoverage       float64         `json:"supported_coverage"`
	SectionSpanCoverage     float64         `json:"section_span_coverage"`
	UnsupportedPrefixFrames int             `json:"unsupported_prefix_frames"`
	UnsupportedSuffixFrames int             `json:"unsupported_suffix_frames"`
	InteriorGaps            []frameInterval `json:"interior_gaps"`
}

// routeOverlap reports one route's strongest qualified offset. Different
// routes remain separate and alternative offsets are not exhausted.
type routeOverlap struct {
	Route                   string           `json:"route"`
	MaxHamming              int              `json:"max_hamming"`
	OffsetFrames            int              `json:"right_minus_left_offset_frames"`
	Aligned                 alignedInterval  `json:"aligned"`
	RawSimilarFrames        int              `json:"raw_similar_frames"`
	SupportedFrames         int              `json:"supported_frames"`
	SectionSpanFrames       int              `json:"section_span_frames"`
	UnassignedSimilarFrames int              `json:"unassigned_similar_frames"`
	Sections                []overlapSection `json:"sections"`
	Left                    overlapSide      `json:"left"`
	Right                   overlapSide      `json:"right"`
}

// measureOverlap reports qualified anchors without inventing a new match,
// containment verdict, quality score, or winner.
func measureOverlap(left, right asset, evidence matcherEvidence) []routeOverlap {
	out := make([]routeOverlap, 0, 2)
	if evidence.primary.Frames >= primaryMinRun {
		out = append(out, segmentOffset(left.frameHashes, right.frameHashes,
			"primary", evidence.primary, primaryMaxHamming))
	}
	if evidence.long.Frames >= longMinRun {
		out = append(out, segmentOffset(left.frameHashes, right.frameHashes,
			"sustained", evidence.long, longMaxHamming))
	}
	return out
}

// segmentOffset makes linear passes over one anchored offset. A section needs
// ten samples of span and 80% similar hashes; gaps over two samples split it.
// Misses inside a section never contribute to supported coverage.
func segmentOffset(left, right []uint64, route string, anchor dvideo.AlignmentEvidence, maxHamming int) routeOverlap {
	offset := anchor.RightStart - anchor.LeftStart
	start := max(0, -offset)
	end := min(len(left), len(right)-offset)
	out := routeOverlap{
		Route: route, MaxHamming: maxHamming, OffsetFrames: offset,
		Aligned: intervalAt(start, end, offset), Sections: []overlapSection{},
	}
	hits := make([]bool, max(0, end-start))
	for i := range hits {
		hits[i] = dvideo.Hamming(left[start+i], right[start+i+offset]) <= maxHamming
		if hits[i] {
			out.RawSimilarFrames++
		}
	}
	first, last, similar := -1, -1, 0
	flush := func() {
		span := last - first + 1
		if first < 0 || span < overlapMinFrames || float64(similar)/float64(span) < overlapMinSimilarity {
			return
		}
		section := overlapSection{
			alignedInterval: intervalAt(start+first, start+last+1, offset),
			SimilarFrames:   similar, ToleratedGaps: []alignedInterval{},
		}
		for i := first; i <= last; {
			if hits[i] {
				i++
				continue
			}
			gapStart := i
			for i <= last && !hits[i] {
				i++
			}
			section.ToleratedGaps = append(section.ToleratedGaps, intervalAt(start+gapStart, start+i, offset))
		}
		out.Sections = append(out.Sections, section)
		out.SupportedFrames += similar
		out.SectionSpanFrames += span
	}
	for i, hit := range hits {
		if !hit {
			continue
		}
		if first < 0 || i-last-1 > overlapGapFrames {
			flush()
			first, similar = i, 0
		}
		last = i
		similar++
	}
	flush()
	out.UnassignedSimilarFrames = out.RawSimilarFrames - out.SupportedFrames
	leftSpans, rightSpans := make([]frameInterval, 0, len(out.Sections)), make([]frameInterval, 0, len(out.Sections))
	for _, section := range out.Sections {
		leftSpans = append(leftSpans, section.Left)
		rightSpans = append(rightSpans, section.Right)
	}
	out.Left = describeOverlapSide(len(left), out.SupportedFrames, out.SectionSpanFrames, leftSpans)
	out.Right = describeOverlapSide(len(right), out.SupportedFrames, out.SectionSpanFrames, rightSpans)
	return out
}

// intervalAt records the same sampled span in each clip's coordinates.
func intervalAt(start, end, offset int) alignedInterval {
	return alignedInterval{Left: frameInterval{start, end}, Right: frameInterval{start + offset, end + offset}}
}

// describeOverlapSide partitions the samples outside qualified section spans.
// With no sections the whole timeline is unsupported prefix, not an intro.
func describeOverlapSide(total, supported, span int, sections []frameInterval) overlapSide {
	out := overlapSide{
		TotalFrames: total, SupportedCoverage: coverage(supported, total),
		SectionSpanCoverage: coverage(span, total), InteriorGaps: []frameInterval{},
		UnsupportedPrefixFrames: total,
	}
	if len(sections) == 0 {
		return out
	}
	out.UnsupportedPrefixFrames = sections[0].Start
	out.UnsupportedSuffixFrames = total - sections[len(sections)-1].End
	for i := 1; i < len(sections); i++ {
		out.InteriorGaps = append(out.InteriorGaps, frameInterval{sections[i-1].End, sections[i].Start})
	}
	return out
}
