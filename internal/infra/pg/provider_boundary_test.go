// provider_boundary_test.go records actual SQL writer gaps before FF-075
// enforcement. These are explicit current-behavior tests, not safety guarantees.
package pg_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
)

// TestProviderBoundary_CurrentSQLWritersAcceptNewerRegressions demonstrates
// that observation ordering protects chronology, not semantic integrity.
func TestProviderBoundary_CurrentSQLWritersAcceptNewerRegressions(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	base := time.Date(2026, 9, 16, 21, 0, 0, 0, time.UTC)
	for index, path := range []string{"active", "ingest-active", "ingest-completed", "staging"} {
		t.Run(path, func(t *testing.T) {
			f := makeStaging(int64(9600+index), base.Add(-2*time.Hour))
			if path != "staging" {
				require.NoError(t, f.Activate(base.Add(-2*time.Hour)))
				minute, home, away := 65, 4, 2
				f.UpdateFromPoll(fixture.APIStatus{Short: "2h", Long: "Second Half"}, &minute, nil, &home, &away, base)
				if path == "ingest-completed" {
					f.APIStatus = fixture.APIStatus{Short: "ft", Long: "Match Finished"}
					require.NoError(t, f.Complete(base))
				}
			} else {
				f.LastPolledAt = timePtr(base)
			}
			require.NoError(t, repo.Insert(ctx, f))
			incoming := *f
			incoming.LastPolledAt = timePtr(base.Add(30 * time.Second))
			incoming.Home.Name = ""
			minute, zero := 46, 0
			incoming.APIElapsed, incoming.HomeScore, incoming.AwayScore = &minute, &zero, &zero
			incoming.APIStatus = fixture.APIStatus{Short: "ns", Long: "Not Started"}
			switch path {
			case "active":
				applied, err := repo.RefreshActivePoll(ctx, &incoming)
				require.NoError(t, err)
				require.True(t, applied)
			case "staging":
				applied, err := repo.RefreshStagingPoll(ctx, &incoming)
				require.NoError(t, err)
				require.True(t, applied)
			default:
				state, err := repo.StoreFromIngest(ctx, &incoming)
				require.NoError(t, err)
				require.Equal(t, f.State, state)
			}
			got, err := repo.Get(ctx, f.ID)
			require.NoError(t, err)
			require.Empty(t, got.Home.Name, "a newer empty name currently overwrites retained metadata")
			require.Equal(t, f.State, got.State, "lifecycle ownership remains intact")
			require.Equal(t, incoming.APIStatus.Short, got.APIStatus.Short)
			if path != "staging" {
				require.Equal(t, 0, *got.HomeScore, "this semantic regression is currently accepted")
				require.Equal(t, 46, *got.APIElapsed)
			}
		})
	}
}

// timePtr returns an independent timestamp for nullable repository fields.
func timePtr(at time.Time) *time.Time { return &at }
