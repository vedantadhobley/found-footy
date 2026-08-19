// candidate_replay.go provides a narrow, auditable repair path for terminal
// candidate verdicts that a corrected deterministic evaluator can reconsider.
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
)

const (
	// ClockMismatchRejectReason is the terminal reason emitted when the vision
	// model found a clock but its normalized minute or period did not match.
	ClockMismatchRejectReason = "clock present but does not match expected (wrong minute or wrong half)"

	// ClockBoundaryReplayKind identifies the one-time repair introduced with
	// FF-057's reset-per-period boundary handling.
	ClockBoundaryReplayKind = "ff-057-clock-boundary"
)

// candidateReplayDB is the transaction boundary required by CandidateReplayStore.
// Both the production pgx connection and the instrumented Pool implement it.
type candidateReplayDB interface {
	Begin(context.Context) (pgx.Tx, error)
}

// CandidateReplayStore owns the Postgres half of a historical candidate replay.
// It never starts Temporal workflows; callers commit the durable ownership row
// before asking Temporal to execute the corresponding deterministic identity.
type CandidateReplayStore struct {
	db candidateReplayDB
}

// NewCandidateReplayStore constructs a replay store over a pgx transaction source.
func NewCandidateReplayStore(db candidateReplayDB) *CandidateReplayStore {
	return &CandidateReplayStore{db: db}
}

// CandidateReplayEvent is one fixture event plus the number of terminal rows
// selected by the exact replay predicate.
type CandidateReplayEvent struct {
	Input              discoverycontract.EventWorkflowInput
	EventType          string
	Detail             string
	EligibleCandidates int
	AlreadyPrepared    bool
	Completed          bool
}

// ListCandidateReplayEvents returns processed, non-removed fixture events in
// match order. It is read-only and includes events with zero eligible rows so
// an operator can detect an incomplete or over-broad fixture selection.
func (s *CandidateReplayStore) ListCandidateReplayEvents(
	ctx context.Context,
	fixtureID int64,
	rejectReason string,
	workflowIDPrefix string,
) ([]CandidateReplayEvent, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("pg.CandidateReplayStore.List: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT e.id, e.fixture_id, COALESCE(e.player_name, ''),
		       e.team_name, e.team_id, e.minute, e.extra, e.first_seen_at,
		       e.event_type::text, e.detail,
		       COALESCE(
		           (replay.metadata->>'selected_candidates')::int,
		           COUNT(c.id) FILTER (
		               WHERE c.outcome_class = 'rejected' AND c.reject_reason = $2
		           )::int
		       ),
		       replay.workflow_id IS NOT NULL,
		       replay.completed_at IS NOT NULL
		FROM events e
		LEFT JOIN event_search_candidates c ON c.event_id = e.id
		LEFT JOIN event_downstream_workflows replay
		       ON replay.event_id = e.id
		      AND replay.workflow_type = 'discovery'
		      AND replay.workflow_id = $3 || e.id::text
		WHERE e.fixture_id = $1
		  AND e.downstream_triggered
		  AND NOT e.removed
		GROUP BY e.id, replay.workflow_id, replay.completed_at, replay.metadata
		ORDER BY e.minute, COALESCE(e.extra, 0), e.first_seen_at, e.id
	`, fixtureID, rejectReason, workflowIDPrefix)
	if err != nil {
		return nil, fmt.Errorf("pg.CandidateReplayStore.List: query fixture=%d: %w", fixtureID, err)
	}
	defer rows.Close()

	var events []CandidateReplayEvent
	for rows.Next() {
		var event CandidateReplayEvent
		if err := rows.Scan(
			&event.Input.EventID,
			&event.Input.FixtureID,
			&event.Input.PlayerName,
			&event.Input.TeamName,
			&event.Input.TeamID,
			&event.Input.Minute,
			&event.Input.Extra,
			&event.Input.FirstSeenAt,
			&event.EventType,
			&event.Detail,
			&event.EligibleCandidates,
			&event.AlreadyPrepared,
			&event.Completed,
		); err != nil {
			return nil, fmt.Errorf("pg.CandidateReplayStore.List: scan fixture=%d: %w", fixtureID, err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg.CandidateReplayStore.List: rows fixture=%d: %w", fixtureID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("pg.CandidateReplayStore.List: commit fixture=%d: %w", fixtureID, err)
	}
	return events, nil
}

// PrepareCandidateReplayInput identifies one deterministic repair execution.
type PrepareCandidateReplayInput struct {
	EventID      uuid.UUID
	WorkflowID   string
	ReplayKind   string
	RejectReason string
	MaxAttempts  int
}

// PrepareCandidateReplayOutput reports the durable selection. AlreadyPrepared
// means an earlier invocation owns this identity; Completed means its normal
// EventWorkflow checklist has already closed.
type PrepareCandidateReplayOutput struct {
	SelectedCandidates int
	AlreadyPrepared    bool
	Completed          bool
}

// PrepareCandidateReplay atomically registers a replay checklist and moves
// only the exact terminal selection back to pending. A rerun with the same
// identity never resets candidates a second time. The old verdict remains in
// outcome_detail.replay and survives the replacement terminal upsert.
func (s *CandidateReplayStore) PrepareCandidateReplay(
	ctx context.Context,
	in PrepareCandidateReplayInput,
) (PrepareCandidateReplayOutput, error) {
	var out PrepareCandidateReplayOutput
	if in.EventID == uuid.Nil || in.WorkflowID == "" || in.ReplayKind == "" ||
		in.RejectReason == "" || in.MaxAttempts <= 0 {
		return out, fmt.Errorf("pg.CandidateReplayStore.Prepare: incomplete replay identity")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return out, fmt.Errorf("pg.CandidateReplayStore.Prepare: begin event=%s: %w", in.EventID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)::int
		FROM event_search_candidates
		WHERE event_id = $1
		  AND outcome_class = 'rejected'
		  AND reject_reason = $2
	`, in.EventID, in.RejectReason).Scan(&out.SelectedCandidates); err != nil {
		return out, fmt.Errorf("pg.CandidateReplayStore.Prepare: count event=%s: %w", in.EventID, err)
	}

	inserted := false
	err = tx.QueryRow(ctx, `
		INSERT INTO event_downstream_workflows (
			event_id, workflow_type, workflow_id, metadata
		) VALUES (
			$1, 'discovery', $2,
			jsonb_build_object(
				'attempts_completed', $3::int,
				'replay_kind', $4::text,
				'replay_selector', jsonb_build_object(
					'outcome_class', 'rejected',
					'reject_reason', $5::text
				),
				'selected_candidates', $6::int
			)
		)
		ON CONFLICT (event_id, workflow_type, workflow_id) DO NOTHING
		RETURNING true
	`, in.EventID, in.WorkflowID, in.MaxAttempts, in.ReplayKind,
		in.RejectReason, out.SelectedCandidates).Scan(&inserted)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, fmt.Errorf("pg.CandidateReplayStore.Prepare: register event=%s: %w", in.EventID, err)
	}

	if !inserted {
		out.AlreadyPrepared = true
		var (
			storedKind, storedReason string
			storedAttempts           int
		)
		if err := tx.QueryRow(ctx, `
			SELECT completed_at IS NOT NULL,
			       COALESCE(metadata->>'replay_kind', ''),
			       COALESCE(metadata#>>'{replay_selector,reject_reason}', ''),
			       COALESCE((metadata->>'attempts_completed')::int, 0),
			       COALESCE((metadata->>'selected_candidates')::int, 0)
			FROM event_downstream_workflows
			WHERE event_id = $1 AND workflow_type = 'discovery' AND workflow_id = $2
		`, in.EventID, in.WorkflowID).Scan(
			&out.Completed,
			&storedKind,
			&storedReason,
			&storedAttempts,
			&out.SelectedCandidates,
		); err != nil {
			return out, fmt.Errorf("pg.CandidateReplayStore.Prepare: load identity event=%s: %w", in.EventID, err)
		}
		if storedKind != in.ReplayKind || storedReason != in.RejectReason || storedAttempts != in.MaxAttempts {
			return out, fmt.Errorf(
				"pg.CandidateReplayStore.Prepare: workflow identity %q belongs to kind=%q reason=%q attempts=%d",
				in.WorkflowID, storedKind, storedReason, storedAttempts,
			)
		}
		if err := tx.Commit(ctx); err != nil {
			return out, fmt.Errorf("pg.CandidateReplayStore.Prepare: commit existing event=%s: %w", in.EventID, err)
		}
		return out, nil
	}

	if out.SelectedCandidates == 0 {
		return out, fmt.Errorf("pg.CandidateReplayStore.Prepare: event=%s has no candidates matching replay selector", in.EventID)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE event_search_candidates
		SET outcome_class = 'pending',
		    reject_reason = NULL,
		    outcome_detail = jsonb_build_object(
		        'replay', jsonb_build_object(
		            'run_id', $3::text,
		            'kind', $4::text,
		            'previous_outcome_class', outcome_class,
		            'previous_reject_reason', reject_reason,
		            'previous_outcome_detail', outcome_detail,
		            'queued_at', $5::timestamptz
		        )
		    ),
		    outcome_at = NULL
		WHERE event_id = $1
		  AND outcome_class = 'rejected'
		  AND reject_reason = $2
	`, in.EventID, in.RejectReason, in.WorkflowID, in.ReplayKind, time.Now().UTC())
	if err != nil {
		return out, fmt.Errorf("pg.CandidateReplayStore.Prepare: queue event=%s: %w", in.EventID, err)
	}
	if int(tag.RowsAffected()) != out.SelectedCandidates {
		return out, fmt.Errorf(
			"pg.CandidateReplayStore.Prepare: selector changed for event=%s: counted=%d updated=%d",
			in.EventID, out.SelectedCandidates, tag.RowsAffected(),
		)
	}

	if err := tx.Commit(ctx); err != nil {
		return out, fmt.Errorf("pg.CandidateReplayStore.Prepare: commit event=%s: %w", in.EventID, err)
	}
	return out, nil
}

// CandidateReplayResult is the persisted completion state for one replay
// identity. ReplayedCandidates counts only rows carrying that run's audit
// envelope, so unrelated fixture candidates cannot satisfy verification.
type CandidateReplayResult struct {
	ChecklistCompleted bool
	OutcomeClass       string
	ReplayedCandidates int
	PendingCandidates  int
}

// ReadCandidateReplayResult verifies the checklist and candidate rows after
// Temporal reports workflow completion.
func (s *CandidateReplayStore) ReadCandidateReplayResult(
	ctx context.Context,
	eventID uuid.UUID,
	workflowID string,
) (CandidateReplayResult, error) {
	var out CandidateReplayResult
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return out, fmt.Errorf("pg.CandidateReplayStore.ReadResult: begin event=%s: %w", eventID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := tx.QueryRow(ctx, `
		SELECT edw.completed_at IS NOT NULL,
		       COALESCE(edw.outcome_class, ''),
		       COUNT(c.id) FILTER (
		           WHERE c.outcome_detail#>>'{replay,run_id}' = $2
		       )::int,
		       COUNT(c.id) FILTER (
		           WHERE c.outcome_detail#>>'{replay,run_id}' = $2
		             AND c.outcome_class = 'pending'
		       )::int
		FROM event_downstream_workflows edw
		LEFT JOIN event_search_candidates c ON c.event_id = edw.event_id
		WHERE edw.event_id = $1
		  AND edw.workflow_type = 'discovery'
		  AND edw.workflow_id = $2
		GROUP BY edw.event_id, edw.workflow_type, edw.workflow_id
	`, eventID, workflowID).Scan(
		&out.ChecklistCompleted,
		&out.OutcomeClass,
		&out.ReplayedCandidates,
		&out.PendingCandidates,
	); err != nil {
		return out, fmt.Errorf("pg.CandidateReplayStore.ReadResult: event=%s: %w", eventID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return out, fmt.Errorf("pg.CandidateReplayStore.ReadResult: commit event=%s: %w", eventID, err)
	}
	return out, nil
}
