// event_replay.go owns guarded registration for operator-requested event recovery.
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
)

// EventReplayMode states exactly which exhausted work an operator is reopening.
type EventReplayMode string

const (
	// EventReplayFreshSearch repeats the bounded Twitter search but never resets
	// a terminal candidate. Existing URLs remain exclusions.
	EventReplayFreshSearch EventReplayMode = "fresh-search"
	// EventReplayRetryTransient re-drives only allowlisted transient failures and
	// checkpoints search at its configured maximum.
	EventReplayRetryTransient EventReplayMode = "retry-transient"
	// EventReplayRecover combines both paths in one EventWorkflow execution.
	EventReplayRecover EventReplayMode = "recover"

	eventReplayKind                     = "operator-event-recovery-v1"
	eventReplaySearchSuppressedAttempts = 20
)

var eventReplayOperationPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,47}$`)

// Valid reports whether mode is part of the public operator contract.
func (m EventReplayMode) Valid() bool {
	switch m {
	case EventReplayFreshSearch, EventReplayRetryTransient, EventReplayRecover:
		return true
	default:
		return false
	}
}

// Searches reports whether this replay receives a fresh usable-search budget.
func (m EventReplayMode) Searches() bool {
	return m == EventReplayFreshSearch || m == EventReplayRecover
}

// RetriesTransient reports whether this replay reopens classified failures.
func (m EventReplayMode) RetriesTransient() bool {
	return m == EventReplayRetryTransient || m == EventReplayRecover
}

// EventReplayRequest is the complete idempotent identity of one operator run.
type EventReplayRequest struct {
	EventID    uuid.UUID
	WorkflowID string
	Operation  string
	Mode       EventReplayMode
}

// EventReplayCandidate is one terminal failure eligible for the v1 retry policy.
type EventReplayCandidate struct {
	ID           uuid.UUID `json:"id"`
	TweetURL     string    `json:"tweet_url"`
	RejectReason string    `json:"reject_reason"`
	FailureStage string    `json:"failure_stage"`
	FailureClass string    `json:"failure_class"`
	DiscoveredAt time.Time `json:"discovered_at"`
}

// EventReplayPlan is the dry-run and post-prepare projection shown to operators.
type EventReplayPlan struct {
	Input              discoverycontract.EventWorkflowInput `json:"input"`
	FixtureState       string                               `json:"fixture_state"`
	WorkflowID         string                               `json:"workflow_id"`
	Operation          string                               `json:"operation"`
	Mode               EventReplayMode                      `json:"mode"`
	ExistingCandidates int                                  `json:"existing_candidates"`
	SelectedCandidates int                                  `json:"selected_candidates"`
	RetryCandidates    []EventReplayCandidate               `json:"retry_candidates"`
	AlreadyPrepared    bool                                 `json:"already_prepared"`
	Completed          bool                                 `json:"completed"`
}

// EventReplayResult is the durable state checked after Temporal terminates.
type EventReplayResult struct {
	ChecklistCompleted bool   `json:"checklist_completed"`
	OutcomeClass       string `json:"outcome_class"`
	PendingCandidates  int    `json:"pending_candidates"`
	ReplayCandidates   int    `json:"replay_candidates"`
}

// EventReplayStore separates durable preparation from external Firefox and
// Temporal effects. A committed row always precedes those effects.
type EventReplayStore struct {
	db candidateReplayDB
}

// NewEventReplayStore constructs the guarded operator replay store.
func NewEventReplayStore(db candidateReplayDB) *EventReplayStore {
	return &EventReplayStore{db: db}
}

// PlanEventReplay validates current state without changing it.
func (s *EventReplayStore) PlanEventReplay(
	ctx context.Context,
	in EventReplayRequest,
) (EventReplayPlan, error) {
	if err := validateEventReplayRequest(in); err != nil {
		return EventReplayPlan{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return EventReplayPlan{}, fmt.Errorf("pg.EventReplayStore.Plan: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	plan, err := loadEventReplayPlan(ctx, tx, in, false)
	if err != nil {
		return EventReplayPlan{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return EventReplayPlan{}, fmt.Errorf("pg.EventReplayStore.Plan: commit: %w", err)
	}
	return plan, nil
}

// PrepareEventReplay atomically locks the event, registers one new discovery
// identity and optionally moves only v1-classified transient failures to
// pending. Repeating the same request never resets completed work.
func (s *EventReplayStore) PrepareEventReplay(
	ctx context.Context,
	in EventReplayRequest,
) (EventReplayPlan, error) {
	if err := validateEventReplayRequest(in); err != nil {
		return EventReplayPlan{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return EventReplayPlan{}, fmt.Errorf("pg.EventReplayStore.Prepare: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	plan, err := loadEventReplayPlan(ctx, tx, in, true)
	if err != nil {
		return EventReplayPlan{}, err
	}
	if plan.AlreadyPrepared {
		if err := tx.Commit(ctx); err != nil {
			return EventReplayPlan{}, fmt.Errorf("pg.EventReplayStore.Prepare: commit existing: %w", err)
		}
		return plan, nil
	}

	attemptsCompleted := eventReplaySearchSuppressedAttempts
	if in.Mode.Searches() {
		attemptsCompleted = 0
	}
	metadata, err := json.Marshal(map[string]any{
		"attempts_completed":  attemptsCompleted,
		"replay_kind":         eventReplayKind,
		"replay_operation":    in.Operation,
		"replay_mode":         in.Mode,
		"replay_selector":     "transient-failure-v1",
		"selected_candidates": plan.SelectedCandidates,
	})
	if err != nil {
		return EventReplayPlan{}, fmt.Errorf("pg.EventReplayStore.Prepare: encode metadata: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO event_downstream_workflows (
			event_id, workflow_type, workflow_id, metadata
		) VALUES ($1, 'discovery', $2, $3::jsonb)
	`, in.EventID, in.WorkflowID, metadata); err != nil {
		return EventReplayPlan{}, fmt.Errorf("pg.EventReplayStore.Prepare: register: %w", err)
	}

	if in.Mode.RetriesTransient() && len(plan.RetryCandidates) > 0 {
		ids := make([]uuid.UUID, len(plan.RetryCandidates))
		for i, candidate := range plan.RetryCandidates {
			ids[i] = candidate.ID
		}
		tag, err := tx.Exec(ctx, `
			UPDATE event_search_candidates
			SET outcome_class = 'pending',
			    reject_reason = NULL,
			    outcome_detail = jsonb_build_object(
			        'replay', jsonb_build_object(
			            'run_id', $2::text,
			            'kind', $3::text,
			            'operation', $4::text,
			            'mode', $5::text,
			            'previous_outcome_class', outcome_class,
			            'previous_reject_reason', reject_reason,
			            'previous_outcome_detail', outcome_detail,
			            'previous_outcome_at', outcome_at,
			            'queued_at', $6::timestamptz
			        )
			    ),
			    outcome_at = NULL
			WHERE event_id = $1 AND id = ANY($7::uuid[])
			  AND `+transientReplayPredicate+`
		`, in.EventID, in.WorkflowID, eventReplayKind, in.Operation, string(in.Mode),
			time.Now().UTC(), ids)
		if err != nil {
			return EventReplayPlan{}, fmt.Errorf("pg.EventReplayStore.Prepare: queue candidates: %w", err)
		}
		if int(tag.RowsAffected()) != len(ids) {
			return EventReplayPlan{}, fmt.Errorf(
				"pg.EventReplayStore.Prepare: transient selector changed: planned=%d updated=%d",
				len(ids), tag.RowsAffected(),
			)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return EventReplayPlan{}, fmt.Errorf("pg.EventReplayStore.Prepare: commit: %w", err)
	}
	plan.AlreadyPrepared = true
	return plan, nil
}

// ReadEventReplayResult checks the exact checklist plus event-wide pending
// candidates after Temporal reports completion.
func (s *EventReplayStore) ReadEventReplayResult(
	ctx context.Context,
	eventID uuid.UUID,
	workflowID string,
) (EventReplayResult, error) {
	var out EventReplayResult
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return out, fmt.Errorf("pg.EventReplayStore.ReadResult: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := tx.QueryRow(ctx, `
		SELECT d.completed_at IS NOT NULL,
		       COALESCE(d.outcome_class, ''),
		       COUNT(c.id) FILTER (WHERE c.outcome_class = 'pending')::int,
		       COUNT(c.id) FILTER (
		           WHERE c.outcome_detail#>>'{replay,run_id}' = $2
		       )::int
		FROM event_downstream_workflows d
		LEFT JOIN event_search_candidates c ON c.event_id = d.event_id
		WHERE d.event_id = $1 AND d.workflow_type = 'discovery' AND d.workflow_id = $2
		GROUP BY d.event_id, d.workflow_type, d.workflow_id
	`, eventID, workflowID).Scan(
		&out.ChecklistCompleted,
		&out.OutcomeClass,
		&out.PendingCandidates,
		&out.ReplayCandidates,
	); err != nil {
		return out, fmt.Errorf("pg.EventReplayStore.ReadResult: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return out, fmt.Errorf("pg.EventReplayStore.ReadResult: commit: %w", err)
	}
	return out, nil
}

// validateEventReplayRequest rejects incomplete or caller-invented identities.
func validateEventReplayRequest(in EventReplayRequest) error {
	switch {
	case in.EventID == uuid.Nil:
		return fmt.Errorf("pg.EventReplayStore: event ID is required")
	case in.WorkflowID == "":
		return fmt.Errorf("pg.EventReplayStore: workflow ID is required")
	case !eventReplayOperationPattern.MatchString(in.Operation):
		return fmt.Errorf("pg.EventReplayStore: operation must match %s", eventReplayOperationPattern)
	case !in.Mode.Valid():
		return fmt.Errorf("pg.EventReplayStore: invalid mode %q", in.Mode)
	}
	wantWorkflowID, err := StableEventReplayWorkflowID(in.EventID, in.Operation)
	if err != nil {
		return fmt.Errorf("pg.EventReplayStore: %w", err)
	}
	if in.WorkflowID != wantWorkflowID {
		return fmt.Errorf("pg.EventReplayStore: workflow ID does not match event and operation")
	}
	return nil
}

// loadEventReplayPlan reads one stable preflight projection under an optional event lock.
func loadEventReplayPlan(
	ctx context.Context,
	tx pgx.Tx,
	in EventReplayRequest,
	lock bool,
) (EventReplayPlan, error) {
	plan := EventReplayPlan{WorkflowID: in.WorkflowID, Operation: in.Operation, Mode: in.Mode}
	lockClause := ""
	if lock {
		lockClause = " FOR UPDATE OF e"
	}
	var removed, triggered bool
	err := tx.QueryRow(ctx, `
		SELECT e.id, e.fixture_id, COALESCE(e.player_name, ''), e.team_name,
		       e.team_id, e.minute, e.extra, e.first_seen_at,
		       f.state::text, e.removed, e.downstream_triggered
		FROM events e JOIN fixtures f ON f.id = e.fixture_id
		WHERE e.id = $1`+lockClause,
		in.EventID,
	).Scan(
		&plan.Input.EventID,
		&plan.Input.FixtureID,
		&plan.Input.PlayerName,
		&plan.Input.TeamName,
		&plan.Input.TeamID,
		&plan.Input.Minute,
		&plan.Input.Extra,
		&plan.Input.FirstSeenAt,
		&plan.FixtureState,
		&removed,
		&triggered,
	)
	if err != nil {
		return plan, fmt.Errorf("pg.EventReplayStore: load event: %w", err)
	}
	if removed {
		return plan, fmt.Errorf("pg.EventReplayStore: event %s is removed", in.EventID)
	}
	if plan.FixtureState != "completed" {
		return plan, fmt.Errorf("pg.EventReplayStore: fixture %d is %s; replay requires completed", plan.Input.FixtureID, plan.FixtureState)
	}
	if !triggered || plan.Input.PlayerName == "" {
		return plan, fmt.Errorf("pg.EventReplayStore: event %s has no completed named discovery", in.EventID)
	}

	var originalComplete bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM event_downstream_workflows
			WHERE event_id = $1 AND workflow_type = 'discovery'
			  AND workflow_id = $2
			  AND completed_at IS NOT NULL
		)
	`, in.EventID, "event-"+in.EventID.String()).Scan(&originalComplete); err != nil {
		return plan, fmt.Errorf("pg.EventReplayStore: inspect original workflow: %w", err)
	}
	if !originalComplete {
		return plan, fmt.Errorf("pg.EventReplayStore: original discovery is not complete")
	}

	var conflicting string
	err = tx.QueryRow(ctx, `
		SELECT workflow_id FROM event_downstream_workflows
		WHERE event_id = $1 AND workflow_type = 'discovery'
		  AND completed_at IS NULL AND workflow_id <> $2
		ORDER BY started_at LIMIT 1
	`, in.EventID, in.WorkflowID).Scan(&conflicting)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return plan, fmt.Errorf("pg.EventReplayStore: inspect pending workflow: %w", err)
	}
	if conflicting != "" {
		return plan, fmt.Errorf("pg.EventReplayStore: pending discovery %q already owns event", conflicting)
	}

	var metadata []byte
	err = tx.QueryRow(ctx, `
		SELECT completed_at IS NOT NULL, metadata
		FROM event_downstream_workflows
		WHERE event_id = $1 AND workflow_type = 'discovery' AND workflow_id = $2
	`, in.EventID, in.WorkflowID).Scan(&plan.Completed, &metadata)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return plan, fmt.Errorf("pg.EventReplayStore: inspect replay identity: %w", err)
	}
	if err == nil {
		plan.AlreadyPrepared = true
		selected, err := validateStoredReplayMetadata(metadata, in)
		if err != nil {
			return plan, err
		}
		plan.SelectedCandidates = selected
	}

	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM event_search_candidates WHERE event_id = $1
	`, in.EventID).Scan(&plan.ExistingCandidates); err != nil {
		return plan, fmt.Errorf("pg.EventReplayStore: count candidates: %w", err)
	}
	if !plan.AlreadyPrepared {
		var pending int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*)::int FROM event_search_candidates
			WHERE event_id = $1 AND outcome_class = 'pending'
		`, in.EventID).Scan(&pending); err != nil {
			return plan, fmt.Errorf("pg.EventReplayStore: count pending candidates: %w", err)
		}
		if pending != 0 {
			return plan, fmt.Errorf("pg.EventReplayStore: event has %d pending candidates", pending)
		}
	}

	if !in.Mode.RetriesTransient() || plan.AlreadyPrepared {
		return plan, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id, tweet_url, reject_reason,
		       outcome_detail#>>'{failure,stage}',
		       outcome_detail#>>'{failure,class}',
		       discovered_at
		FROM event_search_candidates
		WHERE event_id = $1 AND `+transientReplayPredicate+`
		ORDER BY discovered_at, id
	`, in.EventID)
	if err != nil {
		return plan, fmt.Errorf("pg.EventReplayStore: list transient candidates: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var candidate EventReplayCandidate
		if err := rows.Scan(
			&candidate.ID,
			&candidate.TweetURL,
			&candidate.RejectReason,
			&candidate.FailureStage,
			&candidate.FailureClass,
			&candidate.DiscoveredAt,
		); err != nil {
			return plan, fmt.Errorf("pg.EventReplayStore: scan transient candidate: %w", err)
		}
		plan.RetryCandidates = append(plan.RetryCandidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return plan, fmt.Errorf("pg.EventReplayStore: transient candidate rows: %w", err)
	}
	plan.SelectedCandidates = len(plan.RetryCandidates)
	if in.Mode == EventReplayRetryTransient && len(plan.RetryCandidates) == 0 {
		return plan, fmt.Errorf("pg.EventReplayStore: no retryable transient candidates")
	}
	return plan, nil
}

// validateStoredReplayMetadata prevents an operation slug from changing meaning.
func validateStoredReplayMetadata(raw []byte, in EventReplayRequest) (int, error) {
	var metadata struct {
		Kind               string          `json:"replay_kind"`
		Operation          string          `json:"replay_operation"`
		Mode               EventReplayMode `json:"replay_mode"`
		Attempts           int             `json:"attempts_completed"`
		SelectedCandidates int             `json:"selected_candidates"`
		Selector           string          `json:"replay_selector"`
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return 0, fmt.Errorf("pg.EventReplayStore: decode existing replay metadata: %w", err)
	}
	wantAttempts := eventReplaySearchSuppressedAttempts
	if in.Mode.Searches() {
		wantAttempts = 0
	}
	if metadata.Kind != eventReplayKind || metadata.Operation != in.Operation ||
		metadata.Mode != in.Mode || metadata.Attempts != wantAttempts ||
		metadata.Selector != "transient-failure-v1" {
		return 0, fmt.Errorf(
			"pg.EventReplayStore: workflow identity belongs to kind=%q operation=%q mode=%q attempts=%d selector=%q",
			metadata.Kind, metadata.Operation, metadata.Mode, metadata.Attempts, metadata.Selector,
		)
	}
	if metadata.SelectedCandidates < 0 {
		return 0, fmt.Errorf("pg.EventReplayStore: stored selected candidate count is negative")
	}
	return metadata.SelectedCandidates, nil
}

// transientReplayPredicate is deliberately narrow. It excludes deterministic
// vision verdicts, malformed input, local filesystem failures and untyped old
// rows. Expanding it is a reviewed policy change, not an operator flag.
const transientReplayPredicate = `
	outcome_class = 'failed'
	AND observed_asset_id IS NULL
	AND credited_asset_id IS NULL
	AND (
		(reject_reason = 'vision_error' AND (
			(outcome_detail#>>'{failure,stage}' = 'model_admission'
			 AND outcome_detail#>>'{failure,class}' IN ('timeout','concurrency','capacity','unavailable'))
			OR
			(outcome_detail#>>'{failure,stage}' = 'model_request'
			 AND outcome_detail#>>'{failure,class}' IN ('timeout','rate_limited','capacity','unavailable','transport'))
			OR
			(outcome_detail#>>'{failure,stage}' = 'activity'
			 AND outcome_detail#>>'{failure,class}' = 'timeout'
			 AND outcome_detail#>>'{failure,timeout_type}' IN
			     ('start_to_close','schedule_to_start','schedule_to_close','heartbeat','unknown'))
		))
		OR
		(reject_reason = 'download_error'
		 AND outcome_detail#>>'{failure,stage}' IN ('resolve','cdn_download')
		 AND outcome_detail#>>'{failure,class}' IN ('forbidden','rate_limited','timeout','transport','invalid_response','stream'))
	)
`

// StableEventReplayWorkflowID constructs the Temporal/checklist identity. It
// accepts only the same bounded operation slug stored by the repository.
func StableEventReplayWorkflowID(eventID uuid.UUID, operation string) (string, error) {
	if eventID == uuid.Nil {
		return "", fmt.Errorf("event ID is required")
	}
	if !eventReplayOperationPattern.MatchString(operation) {
		return "", fmt.Errorf("operation must match %s", eventReplayOperationPattern)
	}
	return "event-replay-" + eventID.String() + "-" + operation, nil
}
