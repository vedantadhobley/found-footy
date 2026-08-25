// repo.go — the storage-side port. Infra (internal/infra/pg/fixture_repo.go)
// implements this; domain callers depend only on the interface.
package fixture

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is returned by Repo.Get when the fixture ID doesn't exist.
// Callers use errors.Is(err, fixture.ErrNotFound) to distinguish this
// from transport/DB errors.
var ErrNotFound = errors.New("fixture: not found")

// CompletionAssessment is the storage-backed answer to one active fixture's
// completion check. DurableScoreEventParity is nil for exceptional terminal
// outcomes, whose aggregate score is not authoritative; otherwise it records
// audit evidence without gating completion.
type CompletionAssessment struct {
	Ready                   bool
	DurableScoreEventParity *bool
}

// Repo is the storage-side port. The pg adapter implements it; test
// code can substitute an in-memory fake without touching Postgres.
//
// All methods are context-scoped. Cancellation propagates to the
// underlying pgx call.
type Repo interface {
	// Get returns the fixture by ID or ErrNotFound.
	Get(ctx context.Context, id int64) (*Fixture, error)

	// Upsert inserts or updates the fixture. Uses id as the primary key.
	// Called at ingest time (fresh fixtures land in staging) and at
	// active/staging poll (existing fixtures get UpdateFromPoll'd).
	Upsert(ctx context.Context, f *Fixture) error

	// ListByState returns all fixtures currently in the given state,
	// most recently updated first. Used for callers that need the
	// full Fixture rows (e.g. building a dashboard view).
	ListByState(ctx context.Context, state State) ([]*Fixture, error)

	// ListActiveIDs returns only the IDs of active fixtures. Called by
	// ActivePollWorkflow at active cadence to build the batched
	// `apifootball.ListFixturesByIDs` call. Distinct from ListByState
	// because the monitor doesn't need the whole row — hitting the DB
	// for just the ID column keeps the per-cycle overhead near-zero.
	ListActiveIDs(ctx context.Context) ([]int64, error)

	// ListStagingBeforeKickoff returns staging fixtures whose kickoff
	// is before threshold. ActivePollWorkflow's PreActivateUpcoming
	// step pre-activates the returned fixtures within a lookahead
	// window (default 30 min).
	ListStagingBeforeKickoff(ctx context.Context, threshold time.Time) ([]*Fixture, error)

	// AssessCompletion returns the current completion decision for id
	// satisfies the full completion contract per
	// docs/decisions/2026-08-25-terminal-observation-grace-bounds-completion.md:
	//
	//   1. api_status_short is in the Terminal set
	//      (ft, aet, pen, canc, abd, wo, awd)
	//   2. terminal_observed_at is at or before terminalBefore
	//   3. Every non-removed named event has settled its debounce
	//      (debounce settled — no events in flight)
	//   4. No rows in event_downstream_workflows where
	//      completed_at IS NULL for any event in this fixture
	//      (no downstream workflows still writing)
	//
	// Score/event parity and PEN decision state are audit evidence, not
	// permanent completion gates. The grace interval bounds upstream lateness.
	//
	// Returns ErrNotFound if the fixture id doesn't exist. Cheap by
	// design (partial index on event_downstream_workflows_pending +
	// early-exit CHECK constraints); intended to be called from
	// ReconcileFixture at the end of each per-fixture ActivePoll pass.
	AssessCompletion(ctx context.Context, id int64, terminalBefore time.Time) (CompletionAssessment, error)

	// PruneCompleted hard-deletes completed fixtures older than threshold
	// that have NO surviving video_shares — the CLIPLESS half of the
	// two-part retention rule (#176). A share-less fixture never minted a
	// public URL, so deleting its rows 404s nothing; the RESTRICT chain
	// (video_shares → events → fixtures) also enforces "no share ⇒
	// deletable" at the DB layer. Returns count of rows deleted.
	//
	// The clip-BEARING half — reclaiming Garage bytes for aged fixtures
	// that DO have shares, while keeping their rows as 410 tombstones — is
	// ListReclaimableEventIDs + video.DestroyEvent, NOT this method. See
	// decisions.md 2026-08-11 (URL-stability vs retention, revised).
	PruneCompleted(ctx context.Context, threshold time.Time) (int, error)

	// ListReclaimableEventIDs returns the IDs of events belonging to
	// completed fixtures older than threshold that still have at least one
	// non-removed video_share — the byte-reclaim worklist for the
	// clip-BEARING half of retention (#176 option B). The caller runs
	// video.DestroyEvent on each: revoke its shares to state='removed'
	// (→ the #167 redirect 410s) and delete its Garage objects, WITHOUT
	// deleting any rows. The KB-sized rows persist as tombstones so no
	// shared URL ever 404s (rebuild-plan §3 URL-stability, revised
	// 2026-08-11); the GB-sized video bytes are the real reclaimable cost.
	//
	// Filtering on state <> 'removed' makes the daily job idempotent: once
	// an event's shares are revoked it drops off this list, so DestroyEvent
	// never re-runs against it. Returns event IDs (uuid) — DestroyEvent is
	// event-scoped — not fixture IDs.
	ListReclaimableEventIDs(ctx context.Context, threshold time.Time) ([]uuid.UUID, error)
}
