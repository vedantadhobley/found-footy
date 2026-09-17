// Audit explicit local evidence exports; no network, model, database or runtime write path.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"time"

	video "github.com/vedantadhobley/found-footy/internal/domain/video"
	vision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// row decodes only allowlisted evidence and current public-read inputs.
type row struct {
	Source         string                    `json:"source"`
	ID             string                    `json:"id"`
	EventID        string                    `json:"event_id"`
	AssetID        string                    `json:"asset_id"`
	Player         string                    `json:"player"`
	At             time.Time                 `json:"at"`
	Expected       vision.Expected           `json:"expected"`
	CurrentElapsed int                       `json:"current_elapsed"`
	CurrentExtra   int                       `json:"current_extra"`
	Frames         []vision.FrameObservation `json:"frames"`
	Evaluation     vision.Evaluation         `json:"evaluation"`
	Evaluator      string                    `json:"evaluator"`
	Tolerance      int                       `json:"tolerance"`
	Reason         string                    `json:"reason"`
	Origin         struct {
		WorkflowID string `json:"workflow_id"`
	} `json:"origin"`
	Detail struct {
		Frames         []vision.FrameObservation `json:"frame_observations"`
		ExpectedMinute *int                      `json:"expected_minute"`
		ExpectedPeriod string                    `json:"expected_period"`
	} `json:"detail"`
	Verified   bool  `json:"verified"`
	Popularity int   `json:"popularity"`
	FileSize   int64 `json:"file_size"`
}

// comparison retains raw observations; excluded indices are a counterfactual, not rewritten OCR.
type comparison struct {
	Source         string
	ID             string
	EventID        string
	AssetID        string
	Player         string
	Expected       vision.Expected
	Frames         []vision.FrameObservation
	ExcludedFrames []int
	Before         vision.Evaluation
	Naive          vision.Evaluation
	Bounded        vision.Evaluation
}

// report separates experimental outcomes, exclusions and a fixed-score public projection.
type report struct {
	Contract      string
	Sources       map[string]string
	Counts        map[string]int
	Transitions   map[string]int
	Cases         []comparison
	PublicChanges []map[string]any
	Limit         string
}

// main emits no partial report when baseline or input validation fails.
func main() {
	args := os.Args[1:]
	operation := run
	if len(args) > 0 && args[0] == "--replay-current" {
		args, operation = args[1:], replayCurrent
	}
	if err := operation(args, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// readRows bounds local input and refuses duplicate evidence identities or unknown row kinds.
func readRows(paths []string) ([]row, map[string]string, error) {
	if len(paths) == 0 || len(paths) > 16 {
		return nil, nil, fmt.Errorf("provide 1–16 local NDJSON exports")
	}
	var rows []row
	sources := map[string]string{}
	seen := map[string]bool{}
	for _, path := range paths {
		f, err := os.Open(path) // #nosec G304 G703 -- explicit operator-selected local evidence, read-only CLI
		if err != nil {
			return nil, nil, err
		}
		stat, err := f.Stat()
		if err != nil || stat.Size() > 64<<20 {
			f.Close()
			return nil, nil, fmt.Errorf("invalid or oversized input: %s", path)
		}
		h := sha256.New()
		scan := bufio.NewScanner(io.TeeReader(f, h))
		scan.Buffer(make([]byte, 64<<10), 1<<20)
		for scan.Scan() {
			var r row
			if err := json.Unmarshal(scan.Bytes(), &r); err != nil {
				f.Close()
				return nil, nil, err
			}
			switch r.Source {
			case "snapshot":
			case "accepted", "rejected", "share", "history":
				key := r.Source + "/" + r.ID
				if r.ID == "" || r.EventID == "" || seen[key] {
					f.Close()
					return nil, nil, fmt.Errorf("missing/duplicate identity %s", key)
				}
				seen[key] = true
			default:
				f.Close()
				return nil, nil, fmt.Errorf("unknown source %q", r.Source)
			}
			rows = append(rows, r)
		}
		f.Close()
		if err := scan.Err(); err != nil {
			return nil, nil, err
		}
		sources[path] = hex.EncodeToString(h.Sum(nil))
	}
	return rows, sources, nil
}

// run checks saved baselines before applying the isolated frozen-only policy controls.
func run(paths []string, output io.Writer) error {
	rows, sources, err := readRows(paths)
	if err != nil {
		return err
	}
	out := report{Contract: "frozen-only-policy-screen-v1", Sources: sources, Counts: map[string]int{}, Transitions: map[string]int{},
		Limit: "Frozen-compatible raw shapes are not visual proof. Public projection holds current roots and scores fixed; no dHash reselection or support recomputation. Historical missing evidence remains unknown."}
	shares := []row{}
	changed := map[string]vision.Outcome{}
	covered := map[string]bool{}
	for _, r := range rows {
		out.Counts[r.Source+"_rows"]++
		if r.Source == "snapshot" {
			continue
		}
		if r.Source == "share" {
			shares = append(shares, r)
			continue
		}
		if r.Source == "accepted" && r.Origin.WorkflowID == "" {
			out.Counts["manual_evaluations_excluded"]++
			continue
		}
		frames, exp, tolerance, want := r.Frames, r.Expected, r.Tolerance, r.Evaluation
		if r.Source == "rejected" {
			frames, exp, tolerance = r.Detail.Frames, vision.Expected{Elapsed: r.CurrentElapsed, Extra: r.CurrentExtra}, 1
			if r.Detail.ExpectedMinute == nil {
				out.Counts["reject_without_clock_expectation_excluded"]++
				continue
			}
			want = vision.Evaluation{Outcome: vision.OutcomeRejected, Reason: r.Reason}
		} else if r.Source == "accepted" && r.Evaluator != vision.EvaluatorVersion {
			return fmt.Errorf("%s: unsupported evaluator %s", r.ID, r.Evaluator)
		}
		if r.Source == "history" && len(frames) == 0 {
			out.Counts["history_missing_frames_excluded"]++
			continue
		}
		if len(frames) != 3 || tolerance < 0 {
			return fmt.Errorf("%s: incomplete frames or tolerance", r.ID)
		}
		base := vision.Evaluate(frames, exp, tolerance)
		if r.Source == "rejected" && (base.ExpectedMinute != *r.Detail.ExpectedMinute || base.ExpectedPeriod != r.Detail.ExpectedPeriod) {
			out.Counts["changed_expectation_excluded"]++
			continue
		}
		if base.Outcome != want.Outcome || base.Reason != want.Reason || !reflect.DeepEqual(base.MatchedMinute, want.MatchedMinute) {
			return fmt.Errorf("%s: saved baseline does not reproduce", r.ID)
		}
		out.Counts[r.Source+"_baseline_reproduced"]++
		if r.Source == "accepted" {
			covered[r.AssetID] = true
		}
		if base.Reason == "frozen-boundary stoppage, period match (minute unpinned)" {
			out.Counts[r.Source+"_legacy_frozen_reason"]++
		}
		naive, excluded := evaluateControl(frames, exp, tolerance, false)
		bounded, _ := evaluateControl(frames, exp, tolerance, true)
		for mode, after := range map[string]vision.Evaluation{"naive": naive, "bounded": bounded} {
			out.Transitions[r.Source+"/"+mode+"/"+string(base.Outcome)+"->"+string(after.Outcome)]++
		}
		if len(excluded) > 0 {
			out.Counts[r.Source+"_frozen_compatible_rows"]++
			out.Cases = append(out.Cases, comparison{r.Source, r.ID, r.EventID, r.AssetID, r.Player, exp, frames, excluded, base, naive, bounded})
		}
		if base.Outcome != bounded.Outcome && base.Outcome != vision.OutcomeRejected {
			if r.AssetID == "" {
				out.Counts["changed_without_asset_link"]++
				continue
			}
			if previous, exists := changed[r.AssetID]; exists && previous != bounded.Outcome {
				return fmt.Errorf("conflicting proposed acceptance for %s", r.AssetID)
			}
			changed[r.AssetID] = bounded.Outcome
		}
	}
	before := publicRanks(shares)
	afterShares := append([]row(nil), shares...)
	for i := range afterShares {
		if outcome, ok := changed[afterShares[i].AssetID]; ok {
			afterShares[i].Verified = outcome == vision.OutcomeVerified
			if outcome == vision.OutcomeRejected {
				return fmt.Errorf("rejected existing asset requires a selection replay, not this projection")
			}
			out.Counts["changed_live_share_classification"]++
		}
	}
	after := publicRanks(afterShares)
	for i, share := range shares {
		if covered[share.AssetID] {
			out.Counts["live_shares_with_natural_raw_evidence"]++
			if before[share.ID] > 0 {
				out.Counts["public_shares_with_natural_raw_evidence"]++
			}
		}
		if before[share.ID] != after[share.ID] || share.Verified != afterShares[i].Verified {
			out.PublicChanges = append(out.PublicChanges, map[string]any{"share_id": share.ID, "event_id": share.EventID, "before_rank": before[share.ID], "after_rank": after[share.ID], "before_verified": share.Verified, "after_verified": afterShares[i].Verified})
		}
	}
	out.Counts["public_before"] = len(before)
	out.Counts["public_after_fixed_scores"] = len(after)
	enc := json.NewEncoder(output)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// publicRanks mirrors FF-078 and the API read ordering for a fixed score/root set.
// An absent rank is hidden, not deleted; scores are never treated as additive.
func publicRanks(shares []row) map[string]int {
	byEvent := map[string][]row{}
	for _, share := range shares {
		byEvent[share.EventID] = append(byEvent[share.EventID], share)
	}
	out := map[string]int{}
	for _, event := range byEvent {
		verified, unverified := false, false
		for _, s := range event {
			if s.Popularity >= video.PublicVisibilityPopularityThreshold {
				if s.Verified {
					verified = true
				} else {
					unverified = true
				}
			}
		}
		visible := []row{}
		for _, s := range event {
			threshold := verified || (!s.Verified && unverified)
			hidden := s.Popularity == video.PublicVisibilitySingletonPopularity && threshold
			if !hidden {
				visible = append(visible, s)
			}
		}
		sort.Slice(visible, func(i, j int) bool {
			a, b := visible[i], visible[j]
			if a.Verified != b.Verified {
				return a.Verified
			}
			if a.Popularity != b.Popularity {
				return a.Popularity > b.Popularity
			}
			if a.FileSize != b.FileSize {
				return a.FileSize > b.FileSize
			}
			if !a.At.Equal(b.At) {
				return a.At.Before(b.At)
			}
			return a.ID < b.ID
		})
		for i, s := range visible {
			out[s.ID] = i + 1
		}
	}
	return out
}
