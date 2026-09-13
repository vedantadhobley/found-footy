// Deterministic, offline restoration reports keep topology experiments out of production.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	dvideo "github.com/vedantadhobley/found-footy/internal/domain/video"
)

// restorationOrders counts simulated arrival orders, not historical incidents.
type restorationOrders struct {
	Visited               int            `json:"visited"`
	Exhaustive            bool           `json:"exhaustive"`
	Changed               int            `json:"changed_final_sets"`
	BaselineUnsupported   int            `json:"baseline_orders_with_unsupported_prefix"`
	RestorationOperations int            `json:"restoration_operations"`
	BaselineFinalSets     map[string]int `json:"ff092_final_sets"`
	RestoredFinalSets     map[string]int `json:"restored_final_sets"`
}

// restorationReport labels the simulated baseline and the missing deployment
// evidence rather than presenting selected IDs as a production repair plan.
type restorationReport struct {
	Experiment      string            `json:"experiment"`
	Scope           string            `json:"scope"`
	EventID         string            `json:"event_id"`
	EventLabel      string            `json:"event_label"`
	Assets          []reviewedAsset   `json:"assets"`
	WithoutOwnShare []string          `json:"without_own_share"`
	RecordedRoots   []string          `json:"recorded_roots"`
	FF092           restorationReplay `json:"ff092_chronological"`
	Restored        restorationReplay `json:"restored_chronological"`
	Orders          restorationOrders `json:"orders"`
	Limitations     []string          `json:"limitations"`
}

// writeRestorationJSON emits all reports only after successful validation and
// analysis. CSV row order cannot change the chronological replay or report order.
func writeRestorationJSON(w io.Writer, assets []asset, maxOrders int) error {
	if len(assets) == 0 || maxOrders < 1 || maxOrders > 100_000 {
		return fmt.Errorf("restoration requires assets and 1..100000 orders")
	}
	assets = append([]asset(nil), assets...)
	sort.Slice(assets, func(i, j int) bool {
		if assets[i].eventID != assets[j].eventID {
			return assets[i].eventID < assets[j].eventID
		}
		if assets[i].firstSeenAt != assets[j].firstSeenAt {
			return assets[i].firstSeenAt < assets[j].firstSeenAt
		}
		return assets[i].id < assets[j].id
	})
	pools := make(map[poolKey][]asset)
	ids := make(map[string]bool)
	for _, item := range assets {
		if item.id == "" || item.eventID == "" || item.firstSeenAt == "" || ids[item.id] || len(item.frameHashes) == 0 {
			return fmt.Errorf("restoration requires unique identified assets, observation times and hashes")
		}
		if item.hashVersion != dvideo.CurrentFrameHashVersion(0.1) && item.hashVersion != dvideo.LegacyFrameHashVersion {
			return fmt.Errorf("restoration does not support hash version %q", item.hashVersion)
		}
		ids[item.id] = true
		key := poolKey{eventID: item.eventID, verified: item.verified, hashVersion: item.hashVersion}
		pools[key] = append(pools[key], item)
	}
	var reports []restorationReport
	for _, members := range pools {
		graph := buildPoolGraph(members)
		for _, indexes := range connectedComponents(graph.connect) {
			if len(indexes) < 2 {
				continue
			}
			slices.Sort(indexes)
			items := make([]asset, len(indexes))
			match := make([][]bool, len(indexes))
			for i, old := range indexes {
				items[i] = graph.assets[old]
				match[i] = make([]bool, len(indexes))
				for j, other := range indexes {
					match[i][j] = graph.match[old][other]
				}
			}
			report, err := analyzeRestoration(items, match, maxOrders)
			if err != nil {
				return err
			}
			reports = append(reports, report)
		}
	}
	sort.Slice(reports, func(i, j int) bool {
		if reports[i].EventID != reports[j].EventID {
			return reports[i].EventID < reports[j].EventID
		}
		return reports[i].Assets[0].AssetID < reports[j].Assets[0].AssetID
	})
	encoder := json.NewEncoder(w)
	for _, report := range reports {
		if err := encoder.Encode(report); err != nil {
			return fmt.Errorf("write restoration report: %w", err)
		}
	}
	return nil
}

// analyzeRestoration compares FF-092 and the additive support repair on every
// visited order. Population labels, media existence and vote transfer are absent.
func analyzeRestoration(assets []asset, match [][]bool, maxOrders int) (restorationReport, error) {
	order := make([]int, len(assets))
	for i := range order {
		order[i] = i
	}
	baseline, err := replayDirectRestoration(assets, match, order, false, true)
	if err != nil {
		return restorationReport{}, err
	}
	restored, err := replayDirectRestoration(assets, match, order, true, true)
	if err != nil {
		return restorationReport{}, err
	}
	report := restorationReport{
		Experiment: "direct-restoration-v1", Scope: "selected_roots_before_visibility_not_a_repair_plan",
		EventID: assets[0].eventID, EventLabel: componentName(componentFinding{assets: assets}),
		FF092: baseline, Restored: restored,
		Orders: restorationOrders{BaselineFinalSets: make(map[string]int), RestoredFinalSets: make(map[string]int)},
		Limitations: []string{
			"direct_match_is_not_whole_clip_substitution",
			"existing_quality_and_arrival_dependence_remain",
			"media_availability_and_own_validation_not_proven",
			"candidate_credit_and_public_visibility_not_replayed",
			"first_seen_order_is_not_recorded_placement_completion_order",
		},
	}
	for _, item := range assets {
		report.Assets = append(report.Assets, overlapAssetMetadata(item))
		if item.shareID == "" {
			report.WithoutOwnShare = append(report.WithoutOwnShare, item.id)
		}
		if item.supersededBy == "" {
			report.RecordedRoots = append(report.RecordedRoots, item.id)
		}
	}
	report.Orders.Exhaustive = visitOrders(len(assets), maxOrders, func(arrival []int) {
		if err != nil {
			return
		}
		var before, after restorationReplay
		before, err = replayDirectRestoration(assets, match, arrival, false, false)
		if err != nil {
			return
		}
		after, err = replayDirectRestoration(assets, match, arrival, true, false)
		if err != nil {
			return
		}
		report.Orders.Visited++
		report.Orders.BaselineFinalSets[strings.Join(before.Selected, ",")]++
		report.Orders.RestoredFinalSets[strings.Join(after.Selected, ",")]++
		if !slices.Equal(before.Selected, after.Selected) {
			report.Orders.Changed++
		}
		if before.UnsupportedSteps > 0 {
			report.Orders.BaselineUnsupported++
		}
		report.Orders.RestorationOperations += after.RestorationCount
	})
	return report, err
}
