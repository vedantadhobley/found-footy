// FixtureRepo writer-ownership and stale-write regression tests.
package pg_test

import (
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/internal/contract/auditlog"
	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
)

func TestFixtureRepo_StoreFromIngestUsesNewestProviderSnapshot(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	base := time.Date(2026, 8, 28, 18, 0, 0, 0, time.UTC)
	f := makeStaging(9201, base)
	if err := repo.Insert(ctx, f); err != nil {
		t.Fatalf("insert staging: %v", err)
	}
	if err := f.Activate(base.Add(-time.Minute)); err != nil {
		t.Fatalf("activate: %v", err)
	}
	transitionFixture(t, ctx, repo, f, auditlog.KindFixtureActivated)

	elapsed, home, away := 55, 1, 0
	pollAt := base.Add(55 * time.Minute)
	f.UpdateFromPoll(
		fixture.APIStatus{Short: "2H", Long: "Second Half"},
		&elapsed, nil, &home, &away, pollAt,
	)
	monitorKickoff := base.Add(2 * time.Minute)
	f.UpdateMetadata(
		monitorKickoff,
		fixture.Team{ID: 40, Name: "Monitor Liverpool"},
		fixture.Team{ID: 42, Name: "Monitor Arsenal"},
		fixture.League{ID: 39, Name: "Premier League", Season: 2026, Country: "England", Round: "Monitor Round"},
	)
	if refreshed, err := repo.RefreshActivePoll(ctx, f); err != nil || !refreshed {
		t.Fatalf("refresh active: refreshed=%v err=%v", refreshed, err)
	}

	delayed := makeStaging(f.ID, base.Add(5*time.Minute))
	delayed.APIStatus = fixture.APIStatus{Short: "FT", Long: "Match Finished"}
	delayed.Home = fixture.Team{ID: 40, Name: "Stale Liverpool"}
	delayed.Away = fixture.Team{ID: 42, Name: "Stale Arsenal"}
	delayed.League = fixture.League{
		ID: 39, Name: "Premier League", Season: 2026,
		Country: "England", Round: "Stale Round",
	}
	nine, eight := 9, 8
	delayed.HomeScore, delayed.AwayScore = &nine, &eight
	delayed.HomeWinner, delayed.AwayWinner = boolPointer(false), boolPointer(true)
	// Equality is intentionally also rejected for ingest conflicts. Active poll
	// wins the tie when daily ingest and the 30-second schedule start together.
	delayedPollAt := pollAt
	delayed.LastPolledAt = &delayedPollAt

	storedState, err := repo.StoreFromIngest(ctx, delayed)
	if err != nil {
		t.Fatalf("StoreFromIngest: %v", err)
	}
	if storedState != fixture.StateActive {
		t.Fatalf("stored state = %s, want active", storedState)
	}
	got, err := repo.Get(ctx, f.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.APIStatus.Short != "2H" || got.APIElapsed == nil || *got.APIElapsed != elapsed {
		t.Errorf("active clock/status overwritten: status=%s elapsed=%v", got.APIStatus.Short, got.APIElapsed)
	}
	if got.HomeScore == nil || *got.HomeScore != home || got.AwayScore == nil || *got.AwayScore != away {
		t.Errorf("active score overwritten: %v-%v", got.HomeScore, got.AwayScore)
	}
	if got.LastPolledAt == nil || !got.LastPolledAt.Equal(pollAt) {
		t.Errorf("active last_polled_at = %v, want %v", got.LastPolledAt, pollAt)
	}
	if got.ActivatedAt == nil || !got.ActivatedAt.Equal(*f.ActivatedAt) || got.CompletedAt != nil {
		t.Errorf("lifecycle overwritten: activated=%v completed=%v", got.ActivatedAt, got.CompletedAt)
	}
	if !got.Kickoff.Equal(monitorKickoff) || got.Home.Name != "Monitor Liverpool" || got.League.Round != "Monitor Round" {
		t.Errorf("older ingest metadata overwrote provider snapshot: kickoff=%v home=%q round=%q", got.Kickoff, got.Home.Name, got.League.Round)
	}

	fresh := *delayed
	fresh.Home = fixture.Team{ID: 40, Name: "Liverpool FC"}
	fresh.Away = fixture.Team{ID: 42, Name: "Arsenal FC"}
	fresh.League.Round = "Regular Season - 1"
	freshPollAt := pollAt.Add(time.Minute)
	fresh.LastPolledAt = &freshPollAt
	storedState, err = repo.StoreFromIngest(ctx, &fresh)
	if err != nil || storedState != fixture.StateActive {
		t.Fatalf("fresh StoreFromIngest: state=%s err=%v", storedState, err)
	}
	got, err = repo.Get(ctx, f.ID)
	if err != nil {
		t.Fatalf("Get after fresh ingest: %v", err)
	}
	if got.APIStatus.Short != "FT" || got.HomeScore == nil || *got.HomeScore != nine || got.LastPolledAt == nil || !got.LastPolledAt.Equal(freshPollAt) {
		t.Errorf("newer ingest snapshot did not refresh provider fields: status=%s score=%v last_polled=%v", got.APIStatus.Short, got.HomeScore, got.LastPolledAt)
	}
	if got.Home.Name != "Liverpool FC" || got.League.Round != "Regular Season - 1" {
		t.Errorf("newer ingest metadata did not refresh: home=%q round=%q", got.Home.Name, got.League.Round)
	}
	if got.State != fixture.StateActive || got.ActivatedAt == nil || !got.ActivatedAt.Equal(*f.ActivatedAt) || got.CompletedAt != nil {
		t.Errorf("newer ingest changed lifecycle: state=%s activated=%v completed=%v", got.State, got.ActivatedAt, got.CompletedAt)
	}
}

func TestFixtureRepo_StateGuardsRejectDelayedPollWriters(t *testing.T) {
	ctx, _, repo := setupRepo(t)
	base := time.Date(2026, 8, 28, 18, 0, 0, 0, time.UTC)
	f := makeStaging(9202, base)
	if err := repo.Insert(ctx, f); err != nil {
		t.Fatalf("insert staging: %v", err)
	}

	delayedStaging := *f
	delayedStaging.RecordStagingPoll(
		fixture.APIStatus{Short: "HT", Long: "Half Time"},
		base.Add(time.Hour), base.Add(time.Minute),
	)
	f.RecordStagingPoll(
		fixture.APIStatus{Short: "1H", Long: "First Half"},
		base, base.Add(2*time.Minute),
	)
	if err := f.Activate(base.Add(2 * time.Minute)); err != nil {
		t.Fatalf("activate: %v", err)
	}
	transitionFixture(t, ctx, repo, f, auditlog.KindFixtureActivated)
	if refreshed, err := repo.RefreshStagingPoll(ctx, &delayedStaging); err != nil || refreshed {
		t.Fatalf("delayed staging refresh = %v, err=%v; want clean no-op", refreshed, err)
	}

	delayedActive := *f
	delayedActive.APIStatus = fixture.APIStatus{Short: "2H", Long: "Second Half"}
	if err := f.Complete(base.Add(2 * time.Hour)); err != nil {
		t.Fatalf("complete: %v", err)
	}
	transitionFixture(t, ctx, repo, f, auditlog.KindFixtureCompleted)
	if refreshed, err := repo.RefreshActivePoll(ctx, &delayedActive); err != nil || refreshed {
		t.Fatalf("delayed active refresh = %v, err=%v; want clean no-op", refreshed, err)
	}

	got, err := repo.Get(ctx, f.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != fixture.StateCompleted || got.APIStatus.Short != "1H" {
		t.Fatalf("delayed writers mutated completed fixture: state=%s status=%s", got.State, got.APIStatus.Short)
	}
}

func boolPointer(value bool) *bool { return &value }
