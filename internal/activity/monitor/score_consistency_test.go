// score_consistency_test.go — unit coverage for provider score/event inventory
// reconciliation before destructive goal-absence voting.
package monitor

import (
	"testing"

	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// TestScoreEventInventoryRequiresMissingGoal covers normal goals, own goals,
// non-scoring tracked events, shootout filtering, and missing score data.
func TestScoreEventInventoryRequiresMissingGoal(t *testing.T) {
	intp := func(v int) *int { return &v }
	strp := func(v string) *string { return &v }
	goal := func(detail apifootball.APIEventDetail, teamID int) apifootball.APIFixtureEvent {
		return apifootball.APIFixtureEvent{
			Team:   apifootball.APIFixtureTeam{ID: teamID},
			Type:   apifootball.EventTypeGoal,
			Detail: detail,
		}
	}
	base := func(homeScore, awayScore *int, events ...apifootball.APIFixtureEvent) apifootball.APIFixture {
		return apifootball.APIFixture{
			Teams: apifootball.APIFixtureTeams{
				Home: apifootball.APIFixtureTeam{ID: 40},
				Away: apifootball.APIFixtureTeam{ID: 42},
			},
			Goals:  apifootball.APIFixtureGoals{Home: homeScore, Away: awayScore},
			Events: events,
		}
	}

	shootout := goal(apifootball.DetailPenalty, 40)
	shootout.Comments = strp("Penalty Shootout")

	tests := []struct {
		name    string
		fixture apifootball.APIFixture
		teamID  int
		want    bool
	}{
		{
			name:    "score proves one omitted goal",
			fixture: base(intp(2), intp(0), goal(apifootball.DetailNormalGoal, 40)),
			teamID:  40,
			want:    true,
		},
		{
			name:    "score drop supports VAR absence",
			fixture: base(intp(0), intp(0)),
			teamID:  40,
			want:    false,
		},
		{
			name:    "replacement goal accounts for score",
			fixture: base(intp(1), intp(0), goal(apifootball.DetailNormalGoal, 40)),
			teamID:  40,
			want:    false,
		},
		{
			name:    "own goal counts for beneficiary",
			fixture: base(intp(1), intp(0), goal(apifootball.DetailOwnGoal, 40)),
			teamID:  40,
			want:    false,
		},
		{
			name:    "missed penalty does not satisfy score",
			fixture: base(intp(1), intp(0), goal(apifootball.DetailMissedPenalty, 40)),
			teamID:  40,
			want:    true,
		},
		{
			name:    "shootout goal does not satisfy match score",
			fixture: base(intp(1), intp(0), shootout),
			teamID:  40,
			want:    true,
		},
		{
			name:    "nil score falls back to absence debounce",
			fixture: base(nil, nil),
			teamID:  40,
			want:    false,
		},
		{
			name:    "foreign team cannot be reconciled",
			fixture: base(intp(1), intp(0)),
			teamID:  99,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newScoreEventInventory(tt.fixture).scoreRequiresMissingGoal(tt.teamID)
			if got != tt.want {
				t.Fatalf("scoreRequiresMissingGoal(%d) = %v, want %v", tt.teamID, got, tt.want)
			}
		})
	}
}

// TestScoreEventInventoryCompletionVote proves that only consecutive coherent
// played-result snapshots may advance fixture completion. Exceptional terminal
// statuses retain terminal-only voting because they have no score/event parity
// guarantee.
func TestScoreEventInventoryCompletionVote(t *testing.T) {
	intp := func(v int) *int { return &v }
	goal := func(teamID int) apifootball.APIFixtureEvent {
		return apifootball.APIFixtureEvent{
			Team:   apifootball.APIFixtureTeam{ID: teamID},
			Type:   apifootball.EventTypeGoal,
			Detail: apifootball.DetailNormalGoal,
		}
	}
	base := func(homeScore, awayScore *int, events ...apifootball.APIFixtureEvent) apifootball.APIFixture {
		return apifootball.APIFixture{
			Teams: apifootball.APIFixtureTeams{
				Home: apifootball.APIFixtureTeam{ID: 40},
				Away: apifootball.APIFixtureTeam{ID: 42},
			},
			Goals:  apifootball.APIFixtureGoals{Home: homeScore, Away: awayScore},
			Events: events,
		}
	}

	tests := []struct {
		name    string
		status  apifootball.APIStatusCode
		fixture apifootball.APIFixture
		want    bool
	}{
		{
			name:    "played result exactly matches",
			status:  apifootball.StatusFullTime,
			fixture: base(intp(1), intp(1), goal(40), goal(42)),
			want:    true,
		},
		{
			name:    "played result omits a goal",
			status:  apifootball.StatusFullTime,
			fixture: base(intp(2), intp(0), goal(40)),
			want:    false,
		},
		{
			name:    "played result has an extra goal",
			status:  apifootball.StatusAfterExtra,
			fixture: base(intp(0), intp(0), goal(40)),
			want:    false,
		},
		{
			name:    "foreign beneficiary is incoherent",
			status:  apifootball.StatusPenaltyDone,
			fixture: base(intp(0), intp(0), goal(99)),
			want:    false,
		},
		{
			name:    "nil played score is not evidence",
			status:  apifootball.StatusFullTime,
			fixture: base(nil, nil),
			want:    false,
		},
		{
			name:    "non-terminal coherent score resets",
			status:  apifootball.StatusSecondHalf,
			fixture: base(intp(0), intp(0)),
			want:    false,
		},
		{
			name:    "exceptional terminal bypasses parity",
			status:  apifootball.StatusCancelled,
			fixture: base(nil, nil),
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv := newScoreEventInventory(tt.fixture)
			got := inv.completionVote(tt.status, tt.fixture.Teams.Home.ID, tt.fixture.Teams.Away.ID)
			if got != tt.want {
				t.Fatalf("completionVote(%q) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}
