// event_identity.go — stable provider-to-row sequence assignment for monitor
// reconciliation across API reorder, minute correction, and VAR tombstones.
package monitor

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// eventIdentityClockTolerance bounds mutable-clock matching. API-Football
// commonly corrects a reported event by one minute; five minutes leaves room
// for stoppage-time representation changes without letting a genuinely later
// goal consume an older row's identity.
const eventIdentityClockTolerance = 5

type eventSequenceGroup struct {
	providerIndexes []int
	active          []storedEventSequence
	maxSequence     int
	sequences       map[int]struct{}
	teamID          int
	domainType      event.Type
}

type storedEventSequence struct {
	event    *event.Event
	sequence int
}

type eventSequenceMatch struct {
	providerIndex int
	stored        storedEventSequence
}

type eventSequenceMatchPlan struct {
	matches          []eventSequenceMatch
	totalDistance    int
	detailMismatches int
}

// assignEventSequences maps each trackable provider-array element to the
// immutable sequence used in its natural key. Active stored rows win by nearest
// clock within a conservative correction window. Removed rows reserve their
// historical sequences but never consume current provider evidence: a
// post-removal reappearance starts a fresh event generation above the complete
// active + removed history.
func assignEventSequences(
	apiEvents []apifootball.APIFixtureEvent,
	storedEvents []*event.Event,
	incompleteGoalTeams map[int]bool,
) (map[int]int, error) {
	groups := make(map[string]*eventSequenceGroup)
	for _, stored := range storedEvents {
		sequence, err := sequenceFromNaturalKey(stored.NaturalKey)
		if err != nil {
			return nil, err
		}
		key := eventIdentityGroupKey(stored.Team.ID, stored.Player.ID, stored.Type)
		group := groups[key]
		if group == nil {
			group = &eventSequenceGroup{
				sequences:  make(map[int]struct{}),
				teamID:     stored.Team.ID,
				domainType: stored.Type,
			}
			groups[key] = group
		}
		if _, duplicate := group.sequences[sequence]; duplicate {
			return nil, fmt.Errorf("monitor.assignEventSequences: duplicate sequence %d in group %q", sequence, key)
		}
		group.sequences[sequence] = struct{}{}
		if sequence > group.maxSequence {
			group.maxSequence = sequence
		}
		ref := storedEventSequence{event: stored, sequence: sequence}
		if !stored.Removed {
			group.active = append(group.active, ref)
		}
	}

	for index, apiEvent := range apiEvents {
		domainType := trackableType(apiEvent)
		if domainType == "" {
			continue
		}
		key := eventIdentityGroupKey(apiEvent.Team.ID, apiEvent.Player.ID, domainType)
		group := groups[key]
		if group == nil {
			group = &eventSequenceGroup{
				sequences:  make(map[int]struct{}),
				teamID:     apiEvent.Team.ID,
				domainType: domainType,
			}
			groups[key] = group
		}
		group.providerIndexes = append(group.providerIndexes, index)
	}

	assigned := make(map[int]int)
	for _, group := range groups {
		clockTolerance := eventIdentityClockTolerance
		if group.domainType == event.TypeGoal && incompleteGoalTeams[group.teamID] {
			// When aggregate score proves this team's event array is incomplete,
			// a nearby unmatched goal may be new rather than a clock correction.
			// Require exact time until the provider returns a coherent inventory.
			clockTolerance = 0
		}
		matchEventSequences(apiEvents, group.providerIndexes, group.active,
			clockTolerance, assigned)

		for _, providerIndex := range group.providerIndexes {
			if _, exists := assigned[providerIndex]; exists {
				continue
			}
			group.maxSequence++
			assigned[providerIndex] = group.maxSequence
		}
	}
	return assigned, nil
}

// matchEventSequences computes an order-preserving maximum-cardinality match
// for one scorer/type group. Both sides are sorted by match clock first, so API
// array reorder cannot swap identities. The dynamic program then minimizes
// total clock correction and detail mismatch without a greedy local choice
// consuming the wrong stored goal.
func matchEventSequences(
	apiEvents []apifootball.APIFixtureEvent,
	providerIndexes []int,
	stored []storedEventSequence,
	clockTolerance int,
	assigned map[int]int,
) {
	providers := make([]int, 0, len(providerIndexes))
	for _, providerIndex := range providerIndexes {
		if _, exists := assigned[providerIndex]; exists {
			continue
		}
		providers = append(providers, providerIndex)
	}
	sort.Slice(providers, func(i, j int) bool {
		left, right := apiEvents[providers[i]], apiEvents[providers[j]]
		leftClock := eventClockMinute(left.Time.Elapsed, left.Time.Extra)
		rightClock := eventClockMinute(right.Time.Elapsed, right.Time.Extra)
		if leftClock != rightClock {
			return leftClock < rightClock
		}
		return providers[i] < providers[j]
	})
	sort.Slice(stored, func(i, j int) bool {
		leftClock := eventClockMinute(stored[i].event.Minute, stored[i].event.Extra)
		rightClock := eventClockMinute(stored[j].event.Minute, stored[j].event.Extra)
		if leftClock != rightClock {
			return leftClock < rightClock
		}
		return stored[i].sequence < stored[j].sequence
	})

	type matchState struct{ provider, stored int }
	memo := make(map[matchState]eventSequenceMatchPlan)
	var best func(int, int) eventSequenceMatchPlan
	best = func(providerPosition, storedPosition int) eventSequenceMatchPlan {
		if providerPosition >= len(providers) || storedPosition >= len(stored) {
			return eventSequenceMatchPlan{}
		}
		state := matchState{provider: providerPosition, stored: storedPosition}
		if plan, exists := memo[state]; exists {
			return plan
		}

		plan := betterEventSequencePlan(
			best(providerPosition+1, storedPosition),
			best(providerPosition, storedPosition+1),
		)
		providerIndex := providers[providerPosition]
		apiEvent := apiEvents[providerIndex]
		storedEvent := stored[storedPosition]
		distance := absInt(eventClockMinute(apiEvent.Time.Elapsed, apiEvent.Time.Extra) -
			eventClockMinute(storedEvent.event.Minute, storedEvent.event.Extra))
		detailDiffers := apiEvent.Detail != storedEvent.event.Detail
		if distance <= clockTolerance {
			tail := best(providerPosition+1, storedPosition+1)
			matched := eventSequenceMatchPlan{
				matches: append([]eventSequenceMatch{{
					providerIndex: providerIndex,
					stored:        storedEvent,
				}}, tail.matches...),
				totalDistance:    distance + tail.totalDistance,
				detailMismatches: tail.detailMismatches,
			}
			if detailDiffers {
				matched.detailMismatches++
			}
			plan = betterEventSequencePlan(plan, matched)
		}
		memo[state] = plan
		return plan
	}

	for _, match := range best(0, 0).matches {
		assigned[match.providerIndex] = match.stored.sequence
	}
}

func betterEventSequencePlan(left, right eventSequenceMatchPlan) eventSequenceMatchPlan {
	if len(left.matches) != len(right.matches) {
		if len(left.matches) > len(right.matches) {
			return left
		}
		return right
	}
	if left.totalDistance != right.totalDistance {
		if left.totalDistance < right.totalDistance {
			return left
		}
		return right
	}
	if left.detailMismatches != right.detailMismatches {
		if left.detailMismatches < right.detailMismatches {
			return left
		}
		return right
	}
	for index := range left.matches {
		if left.matches[index].providerIndex != right.matches[index].providerIndex {
			if left.matches[index].providerIndex < right.matches[index].providerIndex {
				return left
			}
			return right
		}
		if left.matches[index].stored.sequence != right.matches[index].stored.sequence {
			if left.matches[index].stored.sequence < right.matches[index].stored.sequence {
				return left
			}
			return right
		}
	}
	return left
}

func sequenceFromNaturalKey(naturalKey string) (int, error) {
	separator := strings.LastIndexByte(naturalKey, '_')
	if separator < 0 || separator == len(naturalKey)-1 {
		return 0, fmt.Errorf("monitor.assignEventSequences: invalid natural key %q", naturalKey)
	}
	sequence, err := strconv.Atoi(naturalKey[separator+1:])
	if err != nil || sequence < 1 {
		return 0, fmt.Errorf("monitor.assignEventSequences: invalid sequence in natural key %q", naturalKey)
	}
	return sequence, nil
}

func eventIdentityGroupKey(teamID int, playerID *int, domainType event.Type) string {
	player := "unknown"
	if playerID != nil {
		player = strconv.Itoa(*playerID)
	}
	return fmt.Sprintf("%d_%s_%s", teamID, player, domainType)
}

func eventClockMinute(minute int, extra *int) int {
	if extra != nil {
		return minute + *extra
	}
	return minute
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
