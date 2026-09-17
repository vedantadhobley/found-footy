// reconcile_guarded_test.go pins frontend notification effects returned by
// durable mutation receipts, including retries after a lost activity response.
package monitor

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/domain/providerintegrity"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

// TestClassifyGuardedFeedRetainsReceiptEffects proves routing does not depend
// on a fresh read of a fixture whose original change has already committed.
func TestClassifyGuardedFeedRetainsReceiptEffects(t *testing.T) {
	minute := 61
	f := &fixture.Fixture{APIStatus: fixture.APIStatus{Short: apifootball.StatusSecondHalf, Long: "Second Half"}, APIElapsed: &minute}
	for _, tc := range []struct {
		name   string
		result providerintegrity.MutationResult
		want   FixtureFeedAction
	}{
		{"unchanged", providerintegrity.MutationResult{}, ""},
		{"clock or halftime", providerintegrity.MutationResult{FixturePresentationChanged: true}, FixtureFeedStatus},
		{"score or grouping", providerintegrity.MutationResult{FixtureStructuralChanged: true}, FixtureFeedUpdate},
		{"event", providerintegrity.MutationResult{EventChanged: true}, FixtureFeedUpdate},
		{"completion", providerintegrity.MutationResult{Completed: true}, FixtureFeedUpdate},
		{"structural dominates clock", providerintegrity.MutationResult{FixtureStructuralChanged: true, FixturePresentationChanged: true}, FixtureFeedUpdate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.result.Fixture = f
			for range 2 {
				var out ReconcileFixtureOutput
				classifyGuardedFeed(&out, tc.result)
				require.Equal(t, tc.want, out.FeedAction)
			}
		})
	}
}
