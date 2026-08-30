// Unit tests for retention worklist planning.
package retention

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeFixtureWindow struct {
	cutoff *time.Time
	err    error
	count  int
}

func (f *fakeFixtureWindow) PublicCompletedCutoff(_ context.Context, count int) (*time.Time, error) {
	f.count = count
	return f.cutoff, f.err
}

type fakeMediaAssets struct {
	ids    []uuid.UUID
	err    error
	cutoff time.Time
	calls  int
}

func (f *fakeMediaAssets) ListUnreclaimedEventIDsBefore(_ context.Context, cutoff time.Time) ([]uuid.UUID, error) {
	f.calls++
	f.cutoff = cutoff
	return f.ids, f.err
}

func TestPlanMediaRetention(t *testing.T) {
	cutoff := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	wantIDs := []uuid.UUID{uuid.New(), uuid.New()}
	fixtures := &fakeFixtureWindow{cutoff: &cutoff}
	assets := &fakeMediaAssets{ids: wantIDs}
	a := &Activities{Fixtures: fixtures, Assets: assets}

	out, err := a.PlanMediaRetention(context.Background(), PlanMediaRetentionInput{CompletedFixtureDates: 14})
	if err != nil {
		t.Fatalf("PlanMediaRetention: %v", err)
	}
	if fixtures.count != 14 {
		t.Errorf("completed fixture dates = %d, want 14", fixtures.count)
	}
	if out.Cutoff == nil || !out.Cutoff.Equal(cutoff) {
		t.Errorf("cutoff = %v, want %v", out.Cutoff, cutoff)
	}
	if len(out.EventIDs) != len(wantIDs) || out.EventIDs[0] != wantIDs[0] || out.EventIDs[1] != wantIDs[1] {
		t.Errorf("event IDs = %v, want %v", out.EventIDs, wantIDs)
	}
	if assets.calls != 1 || !assets.cutoff.Equal(cutoff) {
		t.Errorf("asset worklist calls=%d cutoff=%v, want 1/%v", assets.calls, assets.cutoff, cutoff)
	}
}

func TestPlanMediaRetention_NoCompletedFixturesSkipsAssets(t *testing.T) {
	assets := &fakeMediaAssets{}
	a := &Activities{Fixtures: &fakeFixtureWindow{}, Assets: assets}

	out, err := a.PlanMediaRetention(context.Background(), PlanMediaRetentionInput{CompletedFixtureDates: 14})
	if err != nil {
		t.Fatalf("PlanMediaRetention: %v", err)
	}
	if out.Cutoff != nil || len(out.EventIDs) != 0 || assets.calls != 0 {
		t.Errorf("output=%+v asset calls=%d, want empty plan", out, assets.calls)
	}
}

func TestPlanMediaRetention_RejectsInvalidWindow(t *testing.T) {
	a := &Activities{Fixtures: &fakeFixtureWindow{}, Assets: &fakeMediaAssets{}}
	if _, err := a.PlanMediaRetention(context.Background(), PlanMediaRetentionInput{}); err == nil {
		t.Fatal("zero completed fixture dates should fail")
	}
}

func TestPlanMediaRetention_PropagatesDependencies(t *testing.T) {
	cutoff := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	boom := errors.New("boom")

	for _, tc := range []struct {
		name     string
		fixtures *fakeFixtureWindow
		assets   *fakeMediaAssets
	}{
		{name: "cutoff", fixtures: &fakeFixtureWindow{err: boom}, assets: &fakeMediaAssets{}},
		{name: "assets", fixtures: &fakeFixtureWindow{cutoff: &cutoff}, assets: &fakeMediaAssets{err: boom}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Activities{Fixtures: tc.fixtures, Assets: tc.assets}
			if _, err := a.PlanMediaRetention(context.Background(), PlanMediaRetentionInput{CompletedFixtureDates: 14}); !errors.Is(err, boom) {
				t.Fatalf("error = %v, want wrapped boom", err)
			}
		})
	}
}
