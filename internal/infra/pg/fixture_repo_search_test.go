// FixtureRepo public search integration tests.
package pg_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vedantadhobley/found-footy/internal/domain/fixture"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// TestFixtureRepo_SearchFixtures exercises case-insensitive competition, team,
// scorer, and assist matching across fixture states.
func TestFixtureRepo_SearchFixtures(t *testing.T) {
	ctx, pool, repo := setupRepo(t)

	// Fixture A: La Liga · Barcelona vs Real Madrid · goal (Lewandowski, assist Yamal).
	fa := makeStaging(8001, time.Date(2026, 8, 1, 19, 0, 0, 0, time.UTC))
	fa.Home = fixture.Team{ID: 529, Name: "Barcelona"}
	fa.Away = fixture.Team{ID: 541, Name: "Real Madrid"}
	fa.League = fixture.League{ID: 140, Name: "La Liga", Season: 2026}
	if err := fa.Activate(time.Date(2026, 8, 1, 20, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("activate A: %v", err)
	}
	if err := repo.Insert(ctx, fa); err != nil {
		t.Fatalf("insert A: %v", err)
	}
	insertSearchEvent(t, ctx, pool, 8001, "529_1_goal_1", "R. Lewandowski", "L. Yamal")

	// Fixture B: Serie A · Inter vs AC Milan · goal (Martinez, no assist).
	fb := makeStaging(8002, time.Date(2026, 8, 2, 19, 0, 0, 0, time.UTC))
	fb.Home = fixture.Team{ID: 505, Name: "Inter"}
	fb.Away = fixture.Team{ID: 489, Name: "AC Milan"}
	fb.League = fixture.League{ID: 135, Name: "Serie A", Season: 2026}
	if err := fb.Activate(time.Date(2026, 8, 2, 20, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("activate B: %v", err)
	}
	if err := repo.Insert(ctx, fb); err != nil {
		t.Fatalf("insert B: %v", err)
	}
	insertSearchEvent(t, ctx, pool, 8002, "505_2_goal_1", "L. Martinez", "")

	check := func(q string, wantID int64) {
		t.Helper()
		got, err := repo.SearchPublicFixtures(ctx, q, 100, 14)
		if err != nil {
			t.Fatalf("SearchPublicFixtures(%q): %v", q, err)
		}
		if wantID == 0 {
			if len(got) != 0 {
				t.Errorf("Search(%q) = %v, want none", q, idsOf(got))
			}
			return
		}
		if len(got) != 1 || got[0].ID != wantID {
			t.Errorf("Search(%q) = %v, want [%d]", q, idsOf(got), wantID)
		}
	}

	check("la liga", 8001)     // competition
	check("serie a", 8002)     // competition
	check("barcelona", 8001)   // home team
	check("milan", 8002)       // away team
	check("lewandowski", 8001) // scorer
	check("yamal", 8001)       // assist
	check("MARTINEZ", 8002)    // scorer, case-insensitive
	check("zzznomatch", 0)     // none
}

// insertSearchEvent seeds one non-removed goal event (raw SQL — the search test
// needs only the searchable name columns) with player_name + assist_name;
// assistName "" → NULL.
func insertSearchEvent(t *testing.T, ctx context.Context, pool *pg.Pool, fixtureID int64, naturalKey, playerName, assistName string) {
	t.Helper()
	var assist *string
	if assistName != "" {
		assist = &assistName
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO events (id, fixture_id, natural_key, event_type, detail,
			team_id, team_name, player_name, assist_name, minute)
		VALUES ($1, $2, $3, 'goal', 'normal goal', 40, 'Team', $4, $5, 30)
	`, uuid.New(), fixtureID, naturalKey, playerName, assist); err != nil {
		t.Fatalf("insert search event: %v", err)
	}
}

// idsOf projects fixture IDs for assertion messages.
func idsOf(fx []*fixture.Fixture) []int64 {
	out := make([]int64, len(fx))
	for i, f := range fx {
		out[i] = f.ID
	}
	return out
}

// #181: a candidate carries a per-candidate terminal outcome — defaults to
// 'pending', the consumer's UPDATE (the exact SQL RecordCandidateOutcome runs)
// stamps class/reason/detail/at, and the CHECK rejects an unknown class.
func TestEventSearchCandidate_Outcome(t *testing.T) {
	ctx, pool, repo := setupRepo(t)

	f := makeStaging(8101, time.Date(2026, 8, 1, 19, 0, 0, 0, time.UTC))
	if err := f.Activate(time.Date(2026, 8, 1, 20, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := repo.Insert(ctx, f); err != nil {
		t.Fatalf("insert: %v", err)
	}
	evID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO events (id, fixture_id, natural_key, event_type, detail, team_id, team_name, minute)
		VALUES ($1, $2, '40_1_goal_1', 'goal', 'normal goal', 40, 'T', 30)`, evID, f.ID); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO event_search_candidates (event_id, fixture_id, search_attempt, query, tweet_url, video_page_url)
		VALUES ($1, $2, 1, 'q', 'https://x.com/a/1', 'https://x.com/i/1')`, evID, f.ID); err != nil {
		t.Fatalf("seed candidate: %v", err)
	}

	// defaults to pending
	var class string
	if err := pool.QueryRow(ctx, `SELECT outcome_class FROM event_search_candidates WHERE event_id=$1`, evID).Scan(&class); err != nil {
		t.Fatalf("select default: %v", err)
	}
	if class != "pending" {
		t.Errorf("default outcome_class = %q, want pending", class)
	}

	// the consumer's terminal UPDATE stamps class + reason + detail + at
	if _, err := pool.Exec(ctx, `
		UPDATE event_search_candidates
		   SET outcome_class = 'rejected', reject_reason = 'geo_restricted',
		       outcome_detail = '{"soccer_votes":1}'::jsonb, outcome_at = NOW()
		 WHERE event_id = $1 AND tweet_url = 'https://x.com/a/1'`, evID); err != nil {
		t.Fatalf("outcome update: %v", err)
	}
	var (
		reason *string
		at     *time.Time
		detail []byte
	)
	if err := pool.QueryRow(ctx, `
		SELECT outcome_class, reject_reason, outcome_detail, outcome_at
		  FROM event_search_candidates WHERE event_id=$1`, evID).Scan(&class, &reason, &detail, &at); err != nil {
		t.Fatalf("select after update: %v", err)
	}
	if class != "rejected" || reason == nil || *reason != "geo_restricted" || at == nil || len(detail) == 0 {
		t.Errorf("outcome roundtrip: class=%q reason=%v at=%v detail=%s", class, reason, at, detail)
	}

	// the CHECK constraint rejects an unregistered class
	if _, err := pool.Exec(ctx, `UPDATE event_search_candidates SET outcome_class='bogus' WHERE event_id=$1`, evID); err == nil {
		t.Error("CHECK should reject an unknown outcome_class")
	}
}
