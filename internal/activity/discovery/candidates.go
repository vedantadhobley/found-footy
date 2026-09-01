// Durable candidate observation, recovery, progress, and terminal-outcome activities.
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	twittercontract "github.com/vedantadhobley/found-footy/internal/contract/twittersearch"
	ddiscovery "github.com/vedantadhobley/found-footy/internal/domain/discovery"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
)

// StoreCandidateInput is the workflow-owned evidence persisted when a
// candidate is first observed. The alias preserves the registered activity's
// historical payload shape while keeping one canonical evidence contract.
type StoreCandidateInput = discoverycontract.CandidateEvidence

// StoreCandidateOutput reports whether the row was inserted or
// deduplicated. Inserted=false on ON CONFLICT hit — expected during
// retry of the SAME attempt after a partial-success crash, but on
// happy path each candidate hits Inserted=true.
type StoreCandidateOutput struct {
	Inserted bool
}

// StoreCandidate inserts one candidate into event_search_candidates. The
// repository serializes this observation with event removal so a late
// activity commits directly as rejected/event_removed instead of pending.
func (a *Activities) StoreCandidate(ctx context.Context, in StoreCandidateInput) (StoreCandidateOutput, error) {
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	inserted, err := pg.NewCandidateRepo(a.Pool).Observe(callCtx, in)
	if err != nil {
		return StoreCandidateOutput{}, fmt.Errorf("discovery.StoreCandidate: event=%s tweet=%s: %w", in.EventID, in.TweetURL, err)
	}
	return StoreCandidateOutput{Inserted: inserted}, nil
}

// RecoveryCandidate is one candidate already owned by the event. Evidence and
// State are the FF-034 contract. TweetURL and Pending remain populated for
// replay of histories recorded before that contract existed.
type RecoveryCandidate struct {
	Evidence discoverycontract.CandidateEvidence
	State    ddiscovery.CandidateState

	TweetURL string
	Pending  bool
}

// LoadEventRecoveryStateInput identifies the EventWorkflow checklist row.
type LoadEventRecoveryStateInput struct {
	EventID      uuid.UUID
	WorkflowType string
	WorkflowID   string
}

// LoadEventRecoveryStateOutput is the durable progress a replacement
// EventWorkflow execution restores before it starts children or searches.
type LoadEventRecoveryStateOutput struct {
	AttemptsCompleted   int
	UnavailableAttempts int
	LastSearchState     twittercontract.ResultState
	LastSearchEvidence  twittercontract.SearchEvidence
	Candidates          []RecoveryCandidate
}

// LoadEventRecoveryState reads the monotonic attempt checkpoint and every
// candidate URL already owned by the event. The checklist row must exist before
// spawn; failing closed here prevents an untracked recovery run from repeating
// side effects without durable ownership state.
func (a *Activities) LoadEventRecoveryState(
	ctx context.Context,
	in LoadEventRecoveryStateInput,
) (LoadEventRecoveryStateOutput, error) {
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var out LoadEventRecoveryStateOutput
	var evidenceJSON []byte
	if err := a.Pool.QueryRow(callCtx, `
		SELECT COALESCE((metadata->>'attempts_completed')::int, 0),
		       COALESCE((metadata->>'unavailable_attempts')::int, 0),
		       COALESCE(metadata->>'last_search_state', ''),
		       COALESCE(metadata->'last_search_evidence', '{}'::jsonb)
		FROM event_downstream_workflows
		WHERE event_id = $1 AND workflow_type = $2 AND workflow_id = $3
	`, in.EventID, in.WorkflowType, in.WorkflowID).Scan(
		&out.AttemptsCompleted,
		&out.UnavailableAttempts,
		&out.LastSearchState,
		&evidenceJSON,
	); err != nil {
		return out, fmt.Errorf("discovery.LoadEventRecoveryState: checklist event=%s workflow=%s: %w",
			in.EventID, in.WorkflowID, err)
	}
	if err := json.Unmarshal(evidenceJSON, &out.LastSearchEvidence); err != nil {
		return out, fmt.Errorf("discovery.LoadEventRecoveryState: search evidence event=%s: %w",
			in.EventID, err)
	}

	rows, err := a.Pool.Query(callCtx, `
		SELECT fixture_id, search_attempt, query,
		       tweet_url, tweet_text, video_page_url, duration_seconds,
		       username, age_minutes_at_discovery, outcome_class
		FROM event_search_candidates
		WHERE event_id = $1
		ORDER BY discovered_at, tweet_url
	`, in.EventID)
	if err != nil {
		return out, fmt.Errorf("discovery.LoadEventRecoveryState: candidates event=%s: %w", in.EventID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var candidate RecoveryCandidate
		var age *float64
		var outcome CandidateOutcome
		candidate.Evidence.EventID = in.EventID
		if err := rows.Scan(
			&candidate.Evidence.FixtureID,
			&candidate.Evidence.SearchAttempt,
			&candidate.Evidence.Query,
			&candidate.Evidence.TweetURL,
			&candidate.Evidence.TweetText,
			&candidate.Evidence.VideoPageURL,
			&candidate.Evidence.DurationSeconds,
			&candidate.Evidence.Username,
			&age,
			&outcome,
		); err != nil {
			return out, fmt.Errorf("discovery.LoadEventRecoveryState: scan event=%s: %w", in.EventID, err)
		}
		if age != nil {
			candidate.Evidence.AgeMinutesAtDiscovery = *age
		}
		candidate.TweetURL = candidate.Evidence.TweetURL
		candidate.Pending = outcome == "pending"
		if candidate.Pending {
			candidate.State = ddiscovery.CandidateObserved
		} else {
			candidate.State = ddiscovery.CandidateTerminal
		}
		out.Candidates = append(out.Candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("discovery.LoadEventRecoveryState: rows event=%s: %w", in.EventID, err)
	}
	return out, nil
}

// RecordDiscoveryProgressInput checkpoints usable-search and unavailable-probe
// progress plus the latest bounded search evidence.
type RecordDiscoveryProgressInput struct {
	EventID             uuid.UUID
	WorkflowType        string
	WorkflowID          string
	Attempt             int
	UnavailableAttempts int                             `json:"unavailable_attempts,omitempty"`
	LastSearchState     twittercontract.ResultState     `json:"last_search_state,omitempty"`
	LastSearchEvidence  *twittercontract.SearchEvidence `json:"last_search_evidence,omitempty"`
}

// RecordDiscoveryProgress monotonically advances both counters. Older replayed
// progress cannot replace the latest state/evidence. A missing checklist row is
// an invariant failure because monitor must register before spawning.
func (a *Activities) RecordDiscoveryProgress(ctx context.Context, in RecordDiscoveryProgressInput) error {
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	evidence := twittercontract.SearchEvidence{}
	if in.LastSearchEvidence != nil {
		evidence = *in.LastSearchEvidence
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return fmt.Errorf("discovery.RecordDiscoveryProgress: encode evidence: %w", err)
	}
	tag, err := a.Pool.Exec(callCtx, `
		UPDATE event_downstream_workflows
		SET metadata = jsonb_set(
			jsonb_set(
				jsonb_set(
					jsonb_set(
						COALESCE(metadata, '{}'::jsonb),
						'{attempts_completed}',
						to_jsonb(GREATEST(COALESCE((metadata->>'attempts_completed')::int, 0), $4::int)),
						true
					),
					'{unavailable_attempts}',
					to_jsonb(GREATEST(COALESCE((metadata->>'unavailable_attempts')::int, 0), $5::int)),
					true
				),
				'{last_search_state}',
				to_jsonb(
					CASE
						WHEN $4::int + $5::int >=
						     COALESCE((metadata->>'attempts_completed')::int, 0) +
						     COALESCE((metadata->>'unavailable_attempts')::int, 0)
						THEN $6::text
						ELSE COALESCE(metadata->>'last_search_state', '')
					END
				),
				true
			),
			'{last_search_evidence}',
			CASE
				WHEN $4::int + $5::int >=
				     COALESCE((metadata->>'attempts_completed')::int, 0) +
				     COALESCE((metadata->>'unavailable_attempts')::int, 0)
				THEN $7::jsonb
				ELSE COALESCE(metadata->'last_search_evidence', '{}'::jsonb)
			END,
			true
		)
		WHERE event_id = $1 AND workflow_type = $2 AND workflow_id = $3
	`, in.EventID, in.WorkflowType, in.WorkflowID, in.Attempt,
		in.UnavailableAttempts, in.LastSearchState, evidenceJSON)
	if err != nil {
		return fmt.Errorf("discovery.RecordDiscoveryProgress: event=%s attempt=%d: %w", in.EventID, in.Attempt, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("discovery.RecordDiscoveryProgress: checklist missing for event=%s workflow=%s",
			in.EventID, in.WorkflowID)
	}
	return nil
}

// CandidateOutcome is the shared terminal candidate class. The aliases remain
// here so existing activity and workflow callers keep their package surface.
type CandidateOutcome = discoverycontract.CandidateOutcome

const (
	OutcomePromoted   = discoverycontract.OutcomePromoted   // surfaced as an asset/share
	OutcomeDuplicate  = discoverycontract.OutcomeDuplicate  // collapsed onto a durable asset winner
	OutcomeSuperseded = discoverycontract.OutcomeSuperseded // promoted, then replaced by a better clip
	OutcomeRejected   = discoverycontract.OutcomeRejected   // deterministic content rejection
	OutcomeFailed     = discoverycontract.OutcomeFailed     // infrastructure failure without a clean verdict
)

// RecordCandidateOutcomeInput carries a candidate's terminal fate. Detail is a
// pre-marshaled JSON object (nil = SQL NULL); RejectReason is a stable slug
// when Outcome is rejected/failed, empty otherwise.
type RecordCandidateOutcomeInput struct {
	EventID      uuid.UUID
	TweetURL     string
	Outcome      CandidateOutcome
	RejectReason string
	Detail       json.RawMessage
}

// UpsertCandidateOutcomeInput joins the immutable observation evidence to one
// terminal result. A single idempotent activity can therefore create a missing
// candidate row or finish an existing pending row without a cross-call race.
type UpsertCandidateOutcomeInput struct {
	Evidence     discoverycontract.CandidateEvidence
	Outcome      CandidateOutcome
	RejectReason string
	Detail       json.RawMessage
}

// UpsertCandidateOutcome durably records one terminal candidate state. The
// repository preserves first-observation evidence and serializes the write
// with event removal so event_removed cannot be overwritten by late work.
func (a *Activities) UpsertCandidateOutcome(ctx context.Context, in UpsertCandidateOutcomeInput) error {
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if in.Evidence.EventID == uuid.Nil || in.Evidence.FixtureID <= 0 ||
		in.Evidence.SearchAttempt <= 0 || in.Evidence.Query == "" || in.Evidence.TweetURL == "" {
		return fmt.Errorf("discovery.UpsertCandidateOutcome: incomplete evidence for event=%s tweet=%s",
			in.Evidence.EventID, in.Evidence.TweetURL)
	}
	if !in.Outcome.Terminal() {
		return fmt.Errorf("discovery.UpsertCandidateOutcome: non-terminal outcome %q", in.Outcome)
	}

	if err := pg.NewCandidateRepo(a.Pool).Complete(
		callCtx, in.Evidence, in.Outcome, in.RejectReason, in.Detail,
	); err != nil {
		return fmt.Errorf("discovery.UpsertCandidateOutcome: event=%s tweet=%s: %w",
			in.Evidence.EventID, in.Evidence.TweetURL, err)
	}
	return nil
}

// RecordCandidateOutcome stamps a candidate's terminal outcome onto its
// event_search_candidates row for histories created before FF-034. New
// histories use UpsertCandidateOutcome; this compatibility activity retains
// the old zero-row behavior until those histories have expired.
func (a *Activities) RecordCandidateOutcome(ctx context.Context, in RecordCandidateOutcomeInput) error {
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := pg.NewCandidateRepo(a.Pool).CompleteLegacy(
		callCtx, in.EventID, in.TweetURL, in.Outcome, in.RejectReason, in.Detail,
	); err != nil {
		return fmt.Errorf("discovery.RecordCandidateOutcome: event=%s tweet=%s: %w", in.EventID, in.TweetURL, err)
	}
	return nil
}
