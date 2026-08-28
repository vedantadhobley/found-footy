// video_placement_repo_test.go verifies accepted candidate placement as one
// retry-safe transaction across attribution, popularity, shares, and assets.
package pg_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	"github.com/vedantadhobley/found-footy/internal/domain/video"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// setupPlacementRepo creates one event and every repository needed to inspect
// a placement transaction through both storage and public-read interfaces.
func setupPlacementRepo(t *testing.T) (*pg.Pool, *pg.PlacementRepo, *pg.AssetRepo, *pg.ShareRepo, int64, uuid.UUID) {
	t.Helper()
	ctx, pool, fixtures := setupRepo(t)
	fixture := completedFixture(t, ctx, fixtures, 9200, time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC))
	eventID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO events (id, fixture_id, natural_key, event_type, detail,
			team_id, team_name, minute)
		VALUES ($1, $2, 'placement_goal_1', 'goal', 'Normal Goal', 40, 'Liverpool', 23)
	`, eventID, fixture.ID); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	return pool, pg.NewPlacementRepo(pool), pg.NewAssetRepo(pool), pg.NewShareRepo(pool), fixture.ID, eventID
}

// placementEvidence returns distinct, valid evidence for one source vote.
func placementEvidence(eventID uuid.UUID, fixtureID int64, suffix string) discoverycontract.CandidateEvidence {
	return discoverycontract.CandidateEvidence{
		EventID:         eventID,
		FixtureID:       fixtureID,
		SearchAttempt:   1,
		Query:           "Liverpool goal filter:videos",
		TweetURL:        "https://x.com/example/status/" + suffix,
		TweetText:       "goal",
		VideoPageURL:    "https://video.twimg.com/" + suffix + ".m3u8",
		DurationSeconds: 12,
		Username:        "example",
	}
}

// insertPlacementAsset creates a durable live asset with an explicit
// popularity so rank movement is easy to assert.
func insertPlacementAsset(t *testing.T, assets *pg.AssetRepo, eventID uuid.UUID, fixtureID int64, suffix string, popularity int) *video.Asset {
	t.Helper()
	a := newAsset(eventID, fixtureID, "placement-md5-"+suffix, []uint64{1, 2, 3}, int64(1_000_000+popularity))
	a.S3Key = "9200/" + suffix + ".mp4"
	a.Popularity = popularity
	if _, err := assets.InsertAsset(t.Context(), a); err != nil {
		t.Fatalf("insert asset %s: %v", suffix, err)
	}
	return a
}

func insertPlacementShare(t *testing.T, shares *pg.ShareRepo, asset *video.Asset, rank int) *video.Share {
	t.Helper()
	share, err := video.NewShare(asset.ID, asset.EventID, true, nil, rank,
		time.Date(2026, 8, 28, 12, 5, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("new share: %v", err)
	}
	if err := shares.Insert(t.Context(), share); err != nil {
		t.Fatalf("insert share: %v", err)
	}
	return share
}

func TestPlacementRepo_RetryCreditsPopularityOnceAndReadDerivesRank(t *testing.T) {
	pool, placements, assets, shares, fixtureID, eventID := setupPlacementRepo(t)

	first := insertPlacementAsset(t, assets, eventID, fixtureID, "first", 1)
	second := insertPlacementAsset(t, assets, eventID, fixtureID, "second", 2)
	insertPlacementShare(t, shares, first, 1)
	insertPlacementShare(t, shares, second, 2)

	before, err := shares.ListLiveForEvent(t.Context(), eventID)
	if err != nil {
		t.Fatalf("list before placement: %v", err)
	}
	if len(before) != 2 || before[0].Popularity != 2 {
		t.Fatalf("initial derived order = %+v, want popularity 2 first", before)
	}

	candidates := []video.PlacementCandidate{
		{Evidence: placementEvidence(eventID, fixtureID, "1001"), Outcome: discoverycontract.OutcomeDuplicate, Detail: json.RawMessage(`{"match":"exact"}`)},
		{Evidence: placementEvidence(eventID, fixtureID, "1002"), Outcome: discoverycontract.OutcomeDuplicate, Detail: json.RawMessage(`{"match":"exact"}`)},
	}
	in := video.ClipPlacement{
		EventID:       eventID,
		FixtureID:     fixtureID,
		WinnerAssetID: first.ID,
		Verified:      true,
		Candidates:    candidates,
		CommittedAt:   time.Date(2026, 8, 28, 12, 10, 0, 0, time.UTC),
	}
	firstResult, err := placements.CommitClipPlacement(t.Context(), in)
	if err != nil {
		t.Fatalf("CommitClipPlacement: %v", err)
	}
	retryResult, err := placements.CommitClipPlacement(t.Context(), in)
	if err != nil {
		t.Fatalf("CommitClipPlacement retry: %v", err)
	}
	if firstResult.ShareID != retryResult.ShareID || firstResult.WinnerAssetID != retryResult.WinnerAssetID {
		t.Fatalf("retry result changed: first=%+v retry=%+v", firstResult, retryResult)
	}

	got, err := assets.Get(t.Context(), first.ID)
	if err != nil {
		t.Fatalf("get credited asset: %v", err)
	}
	if got.Popularity != 3 {
		t.Errorf("popularity after placement retry = %d, want 3", got.Popularity)
	}

	after, err := shares.ListLiveForEvent(t.Context(), eventID)
	if err != nil {
		t.Fatalf("list after placement: %v", err)
	}
	if len(after) != 2 || after[0].ShareID != firstResult.ShareID || after[0].Rank != 1 || after[0].Popularity != 3 {
		t.Fatalf("derived order after votes = %+v, want credited asset at rank 1", after)
	}

	var credited int
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*) FROM event_search_candidates
		WHERE event_id = $1 AND credited_asset_id = $2 AND outcome_class = 'duplicate'
	`, eventID, first.ID).Scan(&credited); err != nil {
		t.Fatalf("read candidate credits: %v", err)
	}
	if credited != 2 {
		t.Errorf("credited candidates = %d, want 2", credited)
	}
}

func TestPlacementRepo_PromotionSupersedesAtomicallyAndRetries(t *testing.T) {
	pool, placements, assets, shares, fixtureID, eventID := setupPlacementRepo(t)
	loser := insertPlacementAsset(t, assets, eventID, fixtureID, "loser", 3)
	loserShare := insertPlacementShare(t, shares, loser, 1)

	for i, suffix := range []string{"2001", "2002", "2003"} {
		e := placementEvidence(eventID, fixtureID, suffix)
		outcome := discoverycontract.OutcomeDuplicate
		if i == 0 {
			outcome = discoverycontract.OutcomePromoted
		}
		if _, err := pool.Exec(t.Context(), `
			INSERT INTO event_search_candidates (
				event_id, fixture_id, search_attempt, query, tweet_url,
				tweet_text, video_page_url, duration_seconds, username,
				outcome_class, outcome_at, credited_asset_id
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NOW(),$11)
		`, e.EventID, e.FixtureID, e.SearchAttempt, e.Query, e.TweetURL,
			e.TweetText, e.VideoPageURL, e.DurationSeconds, e.Username,
			string(outcome), loser.ID); err != nil {
			t.Fatalf("seed loser candidate %d: %v", i, err)
		}
	}

	winner := newAsset(eventID, fixtureID, "placement-md5-winner", []uint64{4, 5, 6}, 2_000_000)
	winner.S3Key = "9200/winner.mp4"
	winner.Popularity = 99 // PlacementRepo deliberately derives this from candidate votes.
	candidate := video.PlacementCandidate{
		Evidence: placementEvidence(eventID, fixtureID, "2004"),
		Outcome:  discoverycontract.OutcomePromoted,
		Detail:   json.RawMessage(`{"verified":true}`),
	}
	in := video.ClipPlacement{
		EventID:       eventID,
		FixtureID:     fixtureID,
		Winner:        winner,
		Verified:      true,
		LoserAssetIDs: []uuid.UUID{loser.ID},
		Candidates:    []video.PlacementCandidate{candidate},
		CommittedAt:   time.Date(2026, 8, 28, 12, 15, 0, 0, time.UTC),
	}
	firstResult, err := placements.CommitClipPlacement(t.Context(), in)
	if err != nil {
		t.Fatalf("CommitClipPlacement: %v", err)
	}
	if !firstResult.WinnerCreated || len(firstResult.LoserObjects) != 1 {
		t.Fatalf("first result = %+v, want new winner and one loser object", firstResult)
	}
	retryResult, err := placements.CommitClipPlacement(t.Context(), in)
	if err != nil {
		t.Fatalf("CommitClipPlacement retry: %v", err)
	}
	if retryResult.WinnerCreated || retryResult.ShareID != firstResult.ShareID {
		t.Fatalf("retry result = %+v, want existing same share", retryResult)
	}

	gotWinner, err := assets.Get(t.Context(), winner.ID)
	if err != nil {
		t.Fatalf("get winner: %v", err)
	}
	if gotWinner.Popularity != 4 {
		t.Errorf("winner popularity = %d, want 4 (one new vote + three merged once)", gotWinner.Popularity)
	}
	gotLoser, err := assets.Get(t.Context(), loser.ID)
	if err != nil {
		t.Fatalf("get loser: %v", err)
	}
	if gotLoser.SupersededBy == nil || *gotLoser.SupersededBy != winner.ID {
		t.Errorf("loser successor = %v, want %s", gotLoser.SupersededBy, winner.ID)
	}
	storedLoserShare, err := shares.Get(t.Context(), loserShare.ID)
	if err != nil {
		t.Fatalf("get loser share: %v", err)
	}
	if storedLoserShare.State != video.ShareStateSuperseded {
		t.Errorf("loser share state = %s, want superseded", storedLoserShare.State)
	}

	live, err := shares.ListLiveForEvent(t.Context(), eventID)
	if err != nil {
		t.Fatalf("list live: %v", err)
	}
	if len(live) != 1 || live[0].ShareID != firstResult.ShareID || live[0].Rank != 1 || live[0].Popularity != 4 {
		t.Fatalf("live clips = %+v, want one derived rank-1 winner", live)
	}

	var winnerCredits, superseded int
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*), count(*) FILTER (WHERE outcome_class = 'superseded')
		FROM event_search_candidates WHERE credited_asset_id = $1
	`, winner.ID).Scan(&winnerCredits, &superseded); err != nil {
		t.Fatalf("read moved credits: %v", err)
	}
	if winnerCredits != 4 || superseded != 1 {
		t.Errorf("winner credits/superseded = %d/%d, want 4/1", winnerCredits, superseded)
	}
}

func TestPlacementRepo_RemovalLockRejectsLatePlacement(t *testing.T) {
	pool, placements, _, _, fixtureID, eventID := setupPlacementRepo(t)
	winner := newAsset(eventID, fixtureID, "placement-md5-removed", []uint64{7, 8, 9}, 1_500_000)
	winner.S3Key = "9200/removed.mp4"
	in := video.ClipPlacement{
		EventID: eventID, FixtureID: fixtureID, Winner: winner, Verified: true,
		Candidates: []video.PlacementCandidate{{
			Evidence: placementEvidence(eventID, fixtureID, "3001"),
			Outcome:  discoverycontract.OutcomePromoted,
		}},
		CommittedAt: time.Date(2026, 8, 28, 12, 20, 0, 0, time.UTC),
	}

	removalTx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin removal: %v", err)
	}
	defer func() { _ = removalTx.Rollback(context.Background()) }()
	if _, err := removalTx.Exec(t.Context(), `
		UPDATE events
		SET removed = TRUE, removed_reason = 'var', removed_at = NOW()
		WHERE id = $1
	`, eventID); err != nil {
		t.Fatalf("stage removal: %v", err)
	}

	type placementCall struct {
		result video.ClipPlacementResult
		err    error
	}
	started := make(chan struct{})
	done := make(chan placementCall, 1)
	go func() {
		close(started)
		result, err := placements.CommitClipPlacement(context.Background(), in)
		done <- placementCall{result: result, err: err}
	}()
	<-started
	select {
	case call := <-done:
		t.Fatalf("placement crossed uncommitted removal lock: result=%+v err=%v", call.result, call.err)
	case <-time.After(250 * time.Millisecond):
	}
	if err := removalTx.Commit(t.Context()); err != nil {
		t.Fatalf("commit removal: %v", err)
	}

	var call placementCall
	select {
	case call = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("placement did not resume after removal commit")
	}
	if call.err != nil || !call.result.EventRemoved {
		t.Fatalf("late placement = %+v, err=%v; want removed result", call.result, call.err)
	}

	var assets, shares, rejected int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM video_assets WHERE event_id = $1`, eventID).Scan(&assets); err != nil {
		t.Fatalf("count assets: %v", err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM video_shares WHERE event_id = $1`, eventID).Scan(&shares); err != nil {
		t.Fatalf("count shares: %v", err)
	}
	if err := pool.QueryRow(t.Context(), `
		SELECT count(*) FROM event_search_candidates
		WHERE event_id = $1 AND outcome_class = 'rejected'
		  AND reject_reason = 'event_removed' AND credited_asset_id IS NULL
	`, eventID).Scan(&rejected); err != nil {
		t.Fatalf("count rejected candidates: %v", err)
	}
	if assets != 0 || shares != 0 || rejected != 1 {
		t.Fatalf("removed placement state assets/shares/rejected = %d/%d/%d, want 0/0/1", assets, shares, rejected)
	}
}

func TestPlacementRepo_RemovalPreservesEarlierCommittedAttribution(t *testing.T) {
	pool, placements, assets, shares, fixtureID, eventID := setupPlacementRepo(t)
	winner := insertPlacementAsset(t, assets, eventID, fixtureID, "placed-first", 1)
	in := video.ClipPlacement{
		EventID: eventID, FixtureID: fixtureID, WinnerAssetID: winner.ID, Verified: true,
		Candidates: []video.PlacementCandidate{{
			Evidence: placementEvidence(eventID, fixtureID, "3002"),
			Outcome:  discoverycontract.OutcomeDuplicate,
		}},
		CommittedAt: time.Date(2026, 8, 28, 12, 25, 0, 0, time.UTC),
	}
	first, err := placements.CommitClipPlacement(t.Context(), in)
	if err != nil {
		t.Fatalf("placement before removal: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `
		UPDATE events SET removed = TRUE, removed_reason = 'var', removed_at = NOW()
		WHERE id = $1
	`, eventID); err != nil {
		t.Fatalf("remove event: %v", err)
	}
	if err := shares.RemoveByEvent(t.Context(), eventID, video.RemovalVAR); err != nil {
		t.Fatalf("revoke committed share: %v", err)
	}

	retry, err := placements.CommitClipPlacement(t.Context(), in)
	if err != nil || !retry.EventRemoved {
		t.Fatalf("retry after removal = %+v, err=%v; want removed", retry, err)
	}
	var outcome string
	var credited uuid.UUID
	if err := pool.QueryRow(t.Context(), `
		SELECT outcome_class, credited_asset_id FROM event_search_candidates
		WHERE event_id = $1 AND tweet_url = $2
	`, eventID, in.Candidates[0].Evidence.TweetURL).Scan(&outcome, &credited); err != nil {
		t.Fatalf("read prior attribution: %v", err)
	}
	if outcome != string(discoverycontract.OutcomeDuplicate) || credited != winner.ID {
		t.Fatalf("prior attribution = %s/%s, want duplicate/%s", outcome, credited, winner.ID)
	}
	if first.ShareID == "" {
		t.Fatal("initial placement did not create a share")
	}
	live, err := shares.ListLiveForEvent(t.Context(), eventID)
	if err != nil {
		t.Fatalf("list live after removal: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("live shares after teardown = %+v, want none", live)
	}
}
