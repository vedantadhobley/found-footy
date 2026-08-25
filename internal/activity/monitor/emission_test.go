// Completion-audit payload helper tests.
package monitor

import (
	"testing"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

func TestPenaltyResultDecided(t *testing.T) {
	intp := func(value int) *int { return &value }
	tests := []struct {
		name       string
		status     apifootball.APIStatusCode
		home, away *int
		want       *bool
	}{
		{name: "not penalty", status: apifootball.StatusFullTime},
		{name: "missing", status: apifootball.StatusPenaltyDone, want: boolp(false)},
		{name: "tied", status: apifootball.StatusPenaltyDone, home: intp(4), away: intp(4), want: boolp(false)},
		{name: "decided", status: apifootball.StatusPenaltyDone, home: intp(5), away: intp(4), want: boolp(true)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := penaltyResultDecided(&fixture.Fixture{
				APIStatus:   fixture.APIStatus{Short: tt.status},
				HomePenalty: tt.home,
				AwayPenalty: tt.away,
			})
			if tt.want == nil {
				if got != nil {
					t.Fatalf("got %v, want nil", *got)
				}
				return
			}
			if got == nil || *got != *tt.want {
				t.Fatalf("got %v, want %v", got, *tt.want)
			}
		})
	}
}

func boolp(value bool) *bool { return &value }
