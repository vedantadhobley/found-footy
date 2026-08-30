// FixtureRepo public-history window integration tests.
package pg_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	retentionactivity "github.com/vedantadhobley/found-footy/internal/activity/retention"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// completedFixture inserts a fixture through the real staging, active, and
// completed domain transitions.
func completedFixture(t *testing.T, ctx context.Context, repo *pg.FixtureRepo, id int64, completedAt time.Time) *fixture.Fixture {
	t.Helper()
	kickoff := completedAt.Add(-2 * time.Hour)
	f := makeStaging(id, kickoff)
	if err := f.Activate(kickoff); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := f.Complete(completedAt); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := repo.Insert(ctx, f); err != nil {
		t.Fatalf("Insert completed: %v", err)
	}
	return f
}

func TestFixtureRepo_PublicCompletedWindowUsesDistinctUTCKickoffDates(t *testing.T) {
	ctx, pool, repo := setupRepo(t)
	base := time.Date(2026, 8, 30, 20, 0, 0, 0, time.UTC)

	// Fifteen distinct UTC kickoff dates. Add a second fixture on the newest
	// date to prove the policy counts dates, not fixtures.
	for i := 0; i < 15; i++ {
		completedFixture(t, ctx, repo, int64(7000+i), base.AddDate(0, 0, -i).Add(2*time.Hour))
	}
	completedFixture(t, ctx, repo, 7099, base.Add(3*time.Hour))
	completedFixture(t, ctx, repo, 7098, base.AddDate(0, 0, -14).Add(3*time.Hour))

	// Active and staging fixtures remain public regardless of the completed
	// cutoff.
	staging := makeStaging(7100, base.Add(24*time.Hour))
	if err := repo.Insert(ctx, staging); err != nil {
		t.Fatalf("insert staging: %v", err)
	}
	active := makeStaging(7101, base.Add(-time.Hour))
	if err := active.Activate(base.Add(-time.Hour)); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := repo.Insert(ctx, active); err != nil {
		t.Fatalf("insert active: %v", err)
	}

	cutoff, err := repo.PublicCompletedCutoff(ctx, 14)
	if err != nil {
		t.Fatalf("PublicCompletedCutoff: %v", err)
	}
	wantCutoff := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	if cutoff == nil || !cutoff.Equal(wantCutoff) {
		t.Fatalf("cutoff = %v, want %v", cutoff, wantCutoff)
	}

	got, err := repo.ListPublicWindow(ctx, 14)
	if err != nil {
		t.Fatalf("ListPublicWindow: %v", err)
	}
	if len(got) != 17 { // 15 completed rows on 14 dates + active + staging
		t.Fatalf("public fixtures = %d, want 17", len(got))
	}
	seen := make(map[int64]bool, len(got))
	for _, f := range got {
		seen[f.ID] = true
	}
	if seen[7014] {
		t.Error("oldest completed fixture leaked into the public window")
	}
	if !seen[7100] || !seen[7101] || !seen[7099] {
		t.Errorf("public window omitted staging, active, or same-date completed fixture: %v", seen)
	}

	// Retention is a read-model boundary, not deletion. Targeted audit access
	// still resolves the hidden fixture.
	byID, err := repo.GetByIDs(ctx, []int64{7014})
	if err != nil {
		t.Fatalf("GetByIDs hidden fixture: %v", err)
	}
	if len(byID) != 1 || byID[0].ID != 7014 {
		t.Fatalf("targeted hidden fixture = %+v, want 7014", byID)
	}

	// A hidden shareless failure corpus and a hidden empty fixture both survive
	// the real retention planner. Neither public visibility nor share existence
	// is an audit-row deletion signal.
	eventID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO events (id, fixture_id, natural_key, event_type, detail,
			team_id, team_name, minute)
		VALUES ($1, 7014, '40_7014_Goal_1', 'goal', 'Normal Goal', 40, 'Liverpool', 23)
	`, eventID); err != nil {
		t.Fatalf("insert hidden event: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO event_search_candidates
			(event_id, fixture_id, search_attempt, query, tweet_url, video_page_url,
			 outcome_class, reject_reason, outcome_at)
		VALUES ($1, 7014, 1, 'test', 'https://x.test/rejected',
			'https://video.test/rejected.mp4', 'rejected', 'vision', now())
	`, eventID); err != nil {
		t.Fatalf("insert hidden candidate: %v", err)
	}
	planner := &retentionactivity.Activities{
		Fixtures: repo,
		Assets:   pg.NewAssetRepo(pool),
	}
	plan, err := planner.PlanMediaRetention(ctx, retentionactivity.PlanMediaRetentionInput{CompletedFixtureDates: 14})
	if err != nil {
		t.Fatalf("PlanMediaRetention: %v", err)
	}
	if len(plan.EventIDs) != 0 {
		t.Fatalf("shareless/no-asset plan = %v, want no media work", plan.EventIDs)
	}
	for _, fixtureID := range []int64{7098, 7014} {
		if _, err := repo.Get(ctx, fixtureID); err != nil {
			t.Fatalf("retention removed fixture %d: %v", fixtureID, err)
		}
	}
	var candidateCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM event_search_candidates WHERE event_id = $1`, eventID,
	).Scan(&candidateCount); err != nil || candidateCount != 1 {
		t.Fatalf("hidden candidate count = %d, %v; want 1", candidateCount, err)
	}
}

func TestFixtureRepo_PublicCompletedCutoffWithoutCompletedFixtures(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	if err := repo.Insert(ctx, makeStaging(7200, time.Now().UTC())); err != nil {
		t.Fatalf("insert staging: %v", err)
	}
	cutoff, err := repo.PublicCompletedCutoff(ctx, 14)
	if err != nil {
		t.Fatalf("PublicCompletedCutoff: %v", err)
	}
	if cutoff != nil {
		t.Fatalf("cutoff = %v, want nil", cutoff)
	}
	got, err := repo.ListPublicWindow(ctx, 14)
	if err != nil || len(got) != 1 || got[0].ID != 7200 {
		t.Fatalf("public window = %+v, %v; want staging fixture", got, err)
	}
}
