// score_consistency.go — score-to-event inventory checks used before a goal
// absence vote can become a destructive VAR classification.
package monitor

import (
	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// scoreEventInventory captures the provider's aggregate score and the scoring
// events present in the same response, keyed by beneficiary team ID. It counts
// only domain goals: open-play missed penalties and shootout events do not
// contribute to the match score represented by APIFixture.Goals.
type scoreEventInventory struct {
	reportedScore   map[int]*int
	reportedPenalty map[int]*int
	presentGoals    map[int]int
}

// newScoreEventInventory derives one immutable reconciliation view from a
// provider fixture response. API-Football reports the beneficiary team in
// event.team for own goals, so ordinary team counting is correct for them.
func newScoreEventInventory(f apifootball.APIFixture) scoreEventInventory {
	inv := scoreEventInventory{
		reportedScore: map[int]*int{
			f.Teams.Home.ID: f.Goals.Home,
			f.Teams.Away.ID: f.Goals.Away,
		},
		reportedPenalty: map[int]*int{
			f.Teams.Home.ID: f.Score.Penalty.Home,
			f.Teams.Away.ID: f.Score.Penalty.Away,
		},
		presentGoals: make(map[int]int, 2),
	}
	for _, apiEvent := range f.Events {
		if trackableType(apiEvent) == event.TypeGoal {
			inv.presentGoals[apiEvent.Team.ID]++
		}
	}
	return inv
}

// scoreRequiresMissingGoal reports whether the aggregate score proves that
// the current API event array is missing at least one goal for teamID. A nil
// score or an unknown team cannot prove the omission, so the existing
// three-vote absence debounce remains the fallback for those responses.
func (i scoreEventInventory) scoreRequiresMissingGoal(teamID int) bool {
	reported, knownTeam := i.reportedScore[teamID]
	if !knownTeam || reported == nil {
		return false
	}
	return *reported > i.presentGoals[teamID]
}

// completionVote reports whether this provider response may advance the
// fixture's completion debounce. Played terminal results must be internally
// coherent: both scores are present and exactly equal the non-shootout goal
// events in the same response, per beneficiary team. Exceptional terminal
// statuses have no reliable score/event contract and vote on terminal status
// alone. Non-terminal responses always reset the counter.
func (i scoreEventInventory) completionVote(
	status apifootball.APIStatusCode,
	homeTeamID, awayTeamID int,
) bool {
	switch status {
	case apifootball.StatusFullTime,
		apifootball.StatusAfterExtra,
		apifootball.StatusPenaltyDone:
		homeScore, homeKnown := i.reportedScore[homeTeamID]
		awayScore, awayKnown := i.reportedScore[awayTeamID]
		if !homeKnown || !awayKnown || homeScore == nil || awayScore == nil {
			return false
		}
		if *homeScore != i.presentGoals[homeTeamID] || *awayScore != i.presentGoals[awayTeamID] {
			return false
		}

		// Reject a malformed response that attributes an additional scoring
		// event to neither fixture team. Per-team equality alone would miss it.
		presentTotal := 0
		for _, goals := range i.presentGoals {
			presentTotal += goals
		}
		if presentTotal != *homeScore+*awayScore {
			return false
		}

		// A PEN result is not coherent until the provider reports a decided
		// shootout. The match score is necessarily tied and cannot identify the
		// winner on its own.
		if status == apifootball.StatusPenaltyDone {
			homePenalty, homeKnown := i.reportedPenalty[homeTeamID]
			awayPenalty, awayKnown := i.reportedPenalty[awayTeamID]
			return homeKnown && awayKnown && homePenalty != nil && awayPenalty != nil &&
				*homePenalty != *awayPenalty
		}
		return true

	case apifootball.StatusCancelled,
		apifootball.StatusAbandoned,
		apifootball.StatusTechnicalLoss,
		apifootball.StatusWalkover:
		return true
	default:
		return false
	}
}
