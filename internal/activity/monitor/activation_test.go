// Activation and staging-poll activity tests.
package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.temporal.io/sdk/temporal"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/apifootball"
)

func TestActivateUpcoming_ActivatesImminent(t *testing.T) {
	kickoff := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(-10 * time.Minute)
	fRepo := newFakeFixtureRepo()
	staging := fixture.New(1,
		fixture.APIStatus{Short: "ns"},
		kickoff,
		fixture.Team{ID: 40, Name: "Liverpool"},
		fixture.Team{ID: 42, Name: "Arsenal"},
		fixture.League{ID: 39, Season: 2026},
	)
	fRepo.Upsert(context.Background(), staging)

	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), now)
	out, err := acts.ActivateUpcoming(context.Background(), ActivateUpcomingInput{Lookahead: 30 * time.Minute})
	if err != nil {
		t.Fatalf("ActivateUpcoming: %v", err)
	}
	if out.Considered != 1 || out.Activated != 1 {
		t.Errorf("out = %+v, want Considered=1 Activated=1", out)
	}
	got, _ := fRepo.Get(context.Background(), 1)
	if got.State != fixture.StateActive {
		t.Errorf("state = %q, want active", got.State)
	}
}

func TestActivateUpcoming_SkipsFarFuture(t *testing.T) {
	kickoff := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	now := kickoff.Add(-48 * time.Hour) // 2 days out
	fRepo := newFakeFixtureRepo()
	staging := fixture.New(1,
		fixture.APIStatus{Short: "ns"},
		kickoff,
		fixture.Team{ID: 40, Name: "Liverpool"},
		fixture.Team{ID: 42, Name: "Arsenal"},
		fixture.League{ID: 39, Season: 2026},
	)
	fRepo.Upsert(context.Background(), staging)

	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), now)
	out, _ := acts.ActivateUpcoming(context.Background(), ActivateUpcomingInput{Lookahead: 30 * time.Minute})
	if out.Considered != 0 || out.Activated != 0 {
		t.Errorf("out = %+v, want zero (fixture too far out)", out)
	}
}

// ── PollStagingFixtures ────────────────────────────────────────

// stagingFixture returns a fresh staging fixture at the given kickoff.
// No last_polled_at (never polled).
func stagingFixture(id int64, kickoff time.Time) *fixture.Fixture {
	return fixture.New(id,
		fixture.APIStatus{Short: "ns", Long: "Not Started"},
		kickoff,
		fixture.Team{ID: 40, Name: "Liverpool"},
		fixture.Team{ID: 42, Name: "Arsenal"},
		fixture.League{ID: 39, Name: "Premier League", Season: 2026},
	)
}

// mkAPIFixture builds a minimal APIFixture with just the fields
// PollStagingFixtures reads (ID, Status, Date).
func mkAPIFixture(id int64, statusShort apifootball.APIStatusCode, kickoff time.Time) apifootball.APIFixture {
	return apifootball.APIFixture{
		Fixture: apifootball.APIFixtureFixture{
			ID:   id,
			Date: kickoff,
			Status: apifootball.APIFixtureStatus{
				Short: statusShort,
				Long:  string(statusShort),
			},
		},
	}
}

func TestPollStagingFixtures_EmptyStaging_NoFetch(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	fetcher := &fakeFetcher{}
	acts := newActs(fetcher, fRepo, newFakeEventRepo(), now)

	out, err := acts.PollStagingFixtures(context.Background(),
		PollStagingFixturesInput{ActivationWindow: 30 * time.Minute})
	if err != nil {
		t.Fatalf("PollStagingFixtures: %v", err)
	}
	if out.Considered != 0 || out.Polled != 0 {
		t.Errorf("out = %+v, want zero", out)
	}
	if fetcher.lastIDs != nil {
		t.Errorf("empty staging should NOT hit fetcher; got IDs=%v", fetcher.lastIDs)
	}
}

func TestPollStagingFixtures_LiveStatus_EmergencyActivates(t *testing.T) {
	// now = 12:23 UTC → bucket = 12*4 + 23/15 = 49.
	// staging fixture never polled (bucket ≠ 49 trivially) with kickoff
	// 2h away. API says match is 1H (in progress) — emergency activate.
	now := time.Date(2026, 7, 10, 12, 23, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	staging := stagingFixture(101, now.Add(2*time.Hour))
	_ = fRepo.Upsert(context.Background(), staging)

	fetcher := &fakeFetcher{
		response: []apifootball.APIFixture{
			mkAPIFixture(101, apifootball.StatusFirstHalf, staging.Kickoff),
		},
	}
	acts := newActs(fetcher, fRepo, newFakeEventRepo(), now)

	out, err := acts.PollStagingFixtures(context.Background(),
		PollStagingFixturesInput{ActivationWindow: 30 * time.Minute})
	if err != nil {
		t.Fatalf("PollStagingFixtures: %v", err)
	}
	if out.EmergencyActivated != 1 {
		t.Errorf("EmergencyActivated = %d, want 1", out.EmergencyActivated)
	}
	if out.KickoffActivated != 0 {
		t.Errorf("KickoffActivated = %d, want 0", out.KickoffActivated)
	}
	if out.Polled != 1 {
		t.Errorf("Polled = %d, want 1", out.Polled)
	}

	got, _ := fRepo.Get(context.Background(), 101)
	if got.State != fixture.StateActive {
		t.Errorf("state = %q, want active", got.State)
	}
	if got.ActivatedAt == nil {
		t.Error("ActivatedAt should be set after emergency activation")
	}
}

func TestPollStagingFixtures_KickoffCorrected_Activates(t *testing.T) {
	// now = 13:00 UTC. Staging fixture stored with kickoff 3h out.
	// Vendor pushes corrected kickoff to 15 min from now (inside 30-min
	// activation window). Status is still NS. Kickoff activation should
	// fire.
	now := time.Date(2026, 7, 10, 13, 0, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	staging := stagingFixture(202, now.Add(3*time.Hour))
	_ = fRepo.Upsert(context.Background(), staging)

	correctedKickoff := now.Add(15 * time.Minute)
	fetcher := &fakeFetcher{
		response: []apifootball.APIFixture{
			mkAPIFixture(202, apifootball.StatusNotStarted, correctedKickoff),
		},
	}
	acts := newActs(fetcher, fRepo, newFakeEventRepo(), now)

	out, err := acts.PollStagingFixtures(context.Background(),
		PollStagingFixturesInput{ActivationWindow: 30 * time.Minute})
	if err != nil {
		t.Fatalf("PollStagingFixtures: %v", err)
	}
	if out.EmergencyActivated != 0 {
		t.Errorf("EmergencyActivated = %d, want 0", out.EmergencyActivated)
	}
	if out.KickoffActivated != 1 {
		t.Errorf("KickoffActivated = %d, want 1", out.KickoffActivated)
	}

	got, _ := fRepo.Get(context.Background(), 202)
	if got.State != fixture.StateActive {
		t.Errorf("state = %q, want active", got.State)
	}
	if !got.Kickoff.Equal(correctedKickoff) {
		t.Errorf("kickoff not updated: got %v, want %v", got.Kickoff, correctedKickoff)
	}
}

func TestPollStagingFixtures_NoStateChange_JustUpdatesFields(t *testing.T) {
	// Non-Live status, kickoff still far out. Should update APIStatus +
	// Kickoff + last_polled_at without transitioning to active.
	now := time.Date(2026, 7, 10, 14, 0, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	staging := stagingFixture(303, now.Add(5*time.Hour))
	_ = fRepo.Upsert(context.Background(), staging)

	// Vendor confirms status still NS and refreshes display metadata.
	apiFixture := mkAPIFixture(303, apifootball.StatusNotStarted, staging.Kickoff)
	apiFixture.Teams = apifootball.APIFixtureTeams{
		Home: apifootball.APIFixtureTeam{ID: 40, Name: "Liverpool FC"},
		Away: apifootball.APIFixtureTeam{ID: 42, Name: "Arsenal FC"},
	}
	apiFixture.League = apifootball.APIFixtureLeague{
		ID: 39, Name: "Premier League", Season: 2026,
		Country: "England", Round: "Regular Season - 1",
	}
	fetcher := &fakeFetcher{
		response: []apifootball.APIFixture{apiFixture},
	}
	acts := newActs(fetcher, fRepo, newFakeEventRepo(), now)

	out, err := acts.PollStagingFixtures(context.Background(),
		PollStagingFixturesInput{ActivationWindow: 30 * time.Minute})
	if err != nil {
		t.Fatalf("PollStagingFixtures: %v", err)
	}
	if out.EmergencyActivated != 0 || out.KickoffActivated != 0 {
		t.Errorf("out = %+v, want no activations", out)
	}
	if out.Polled != 1 {
		t.Errorf("Polled = %d, want 1", out.Polled)
	}

	got, _ := fRepo.Get(context.Background(), 303)
	if got.State != fixture.StateStaging {
		t.Errorf("state = %q, want staging (no transition expected)", got.State)
	}
	if got.LastPolledAt == nil {
		t.Error("LastPolledAt should be set after poll")
	}
	if got.Home.Name != "Liverpool FC" || got.League.Round != "Regular Season - 1" {
		t.Errorf("display metadata not refreshed: home=%q round=%q", got.Home.Name, got.League.Round)
	}
	// LastActivityAt should remain nil — RecordStagingPoll intentionally
	// doesn't touch it (matches Python's semantic).
	if got.LastActivityAt != nil {
		t.Errorf("LastActivityAt = %v, want nil (passive poll doesn't count as activity)", got.LastActivityAt)
	}
}

func TestPollStagingFixtures_PartialFailure_SurfacesMissed(t *testing.T) {
	now := time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC)
	fRepo := newFakeFixtureRepo()
	staging1 := stagingFixture(401, now.Add(2*time.Hour))
	staging2 := stagingFixture(402, now.Add(2*time.Hour))
	_ = fRepo.Upsert(context.Background(), staging1)
	_ = fRepo.Upsert(context.Background(), staging2)

	// Fetcher returns 1 of 2; other is in failedIDs.
	fetcher := &fakeFetcher{
		response: []apifootball.APIFixture{
			mkAPIFixture(401, apifootball.StatusNotStarted, staging1.Kickoff),
		},
		failedIDs: []int64{402},
	}
	acts := newActs(fetcher, fRepo, newFakeEventRepo(), now)

	out, err := acts.PollStagingFixtures(context.Background(),
		PollStagingFixturesInput{ActivationWindow: 30 * time.Minute})
	if err != nil {
		t.Fatalf("PollStagingFixtures: %v", err)
	}
	if out.MissedIDs != 1 {
		t.Errorf("MissedIDs = %d, want 1", out.MissedIDs)
	}
	if out.Polled != 1 {
		t.Errorf("Polled = %d, want 1 (only 401 was reconciled)", out.Polled)
	}
}

// ── ListActiveFixtureIDs ───────────────────────────────────────

func TestListActiveFixtureIDs(t *testing.T) {
	fRepo := newFakeFixtureRepo()
	fRepo.Upsert(context.Background(), mkActiveFixture(101, time.Now()))
	fRepo.Upsert(context.Background(), mkActiveFixture(102, time.Now()))
	staging := fixture.New(103,
		fixture.APIStatus{Short: "ns"},
		time.Now().Add(24*time.Hour),
		fixture.Team{ID: 40}, fixture.Team{ID: 42},
		fixture.League{ID: 39, Season: 2026},
	)
	fRepo.Upsert(context.Background(), staging)

	acts := newActs(&fakeFetcher{}, fRepo, newFakeEventRepo(), time.Now().UTC())
	out, err := acts.ListActiveFixtureIDs(context.Background())
	if err != nil {
		t.Fatalf("ListActiveFixtureIDs: %v", err)
	}
	if len(out.IDs) != 2 {
		t.Errorf("got %d IDs, want 2 (staging excluded)", len(out.IDs))
	}
}

// ── FetchLiveFixtures ──────────────────────────────────────────

func TestFetchLiveFixtures_EmptyShortCircuits(t *testing.T) {
	fetcher := &fakeFetcher{}
	acts := newActs(fetcher, newFakeFixtureRepo(), newFakeEventRepo(), time.Now().UTC())
	out, err := acts.FetchLiveFixtures(context.Background(), FetchLiveFixturesInput{IDs: nil})
	if err != nil {
		t.Fatalf("FetchLiveFixtures: %v", err)
	}
	if len(out.Fixtures) != 0 {
		t.Errorf("expected empty; fetcher called: %v", fetcher.lastIDs)
	}
	if fetcher.lastIDs != nil {
		t.Error("empty input should NOT hit fetcher")
	}
}

func TestFetchLiveFixtures_PreservesContractFailureEvidence(t *testing.T) {
	contractErr := &apifootball.FixtureContractError{
		Reason: apifootball.FixtureContractEventsNull,
	}
	fetcher := &fakeFetcher{
		failures: []apifootball.FixtureFetchFailure{{
			IDs: []int64{101, 102}, Kind: apifootball.FixtureFetchFailureContract,
			ContractReason: apifootball.FixtureContractEventsNull,
		}},
		err: contractErr,
	}
	acts := newActs(fetcher, newFakeFixtureRepo(), newFakeEventRepo(), time.Now().UTC())

	_, err := acts.FetchLiveFixtures(context.Background(), FetchLiveFixturesInput{
		IDs: []int64{101, 102},
	})
	if err == nil {
		t.Fatal("FetchLiveFixtures error = nil, want retryable classified failure")
	}
	var applicationErr *temporal.ApplicationError
	if !errors.As(err, &applicationErr) ||
		applicationErr.Type() != ProviderFetchFailureErrorType ||
		!applicationErr.HasDetails() {
		t.Fatalf("error = %T %v, want typed Temporal application error", err, err)
	}
	var out FetchLiveFixturesOutput
	if err := applicationErr.Details(&out); err != nil {
		t.Fatalf("decode failure details: %v", err)
	}
	if len(out.FailedIDs) != 2 || len(out.Failures) != 1 ||
		out.Failures[0].Kind != ProviderFetchFailureContract ||
		out.Failures[0].ContractReason != string(apifootball.FixtureContractEventsNull) {
		t.Fatalf("output = %+v, want typed contract failure", out)
	}
}

func TestFetchLiveFixtures_TotalTransportFailureStillRetriesActivity(t *testing.T) {
	fetcher := &fakeFetcher{
		failures: []apifootball.FixtureFetchFailure{{
			IDs: []int64{101}, Kind: apifootball.FixtureFetchFailureTransport,
		}},
		err: errors.New("connection reset"),
	}
	acts := newActs(fetcher, newFakeFixtureRepo(), newFakeEventRepo(), time.Now().UTC())

	_, err := acts.FetchLiveFixtures(context.Background(), FetchLiveFixturesInput{IDs: []int64{101}})
	if err == nil {
		t.Fatal("FetchLiveFixtures error = nil, want transport failure for Temporal retry")
	}
}
