// Activities for EventWorkflow. Six activities cover the
// production shape:
//
//  1. FetchTeamAliases — pull team_aliases row (canonical_name +
//     curated aliases) for the scoring team so the query builder
//     has real inputs.
//  2. SearchTweets — call the Twitter service's POST /search with
//     the query builder's output + accumulated exclude_urls.
//  3. StoreCandidate — persist one observed candidate tweet to
//     event_search_candidates. Idempotent via ON CONFLICT DO NOTHING on
//     (event_id, tweet_url).
//  4. LoadEventRecoveryState — restore durable search progress and candidate
//     ownership when a failed EventWorkflow execution restarts.
//  5. RecordDiscoveryProgress — monotonically checkpoint completed searches.
//  6. UpsertCandidateOutcome — atomically persist the full candidate evidence
//     and terminal outcome, whether or not observation persistence landed.
//  7. MarkDownstreamComplete — updates event_downstream_workflows
//     so FixtureReadyToComplete stops treating this workflow as
//     pending.
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vedantadhobley/found-footy/internal/activity/heartbeat"
	ddiscovery "github.com/vedantadhobley/found-footy/internal/domain/discovery"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
	"github.com/vedantadhobley/found-footy/internal/infra/twitter"
)

// Activities bundles Discovery's activity implementations. Held on
// *Activities so tests can inject fakes for the pg pool via a
// pool-shaped interface (currently just the concrete pg.Pool).
//
// Config fields (MaxAttempts, AttemptSpacing, MaxAgeMinutes,
// QueryTimeout) mirror config.DiscoveryConfig. Populated at
// cmd/worker startup — see GetDiscoveryConfig below for the
// workflow-side accessor.
type Activities struct {
	Pool    *pg.Pool
	Twitter twitterClient

	// EventWorkflow tuning knobs, mirrored from
	// config.DiscoveryConfig at worker init. Zero values are
	// treated as "use hardcoded fallback" inside GetDiscoveryConfig
	// so tests that leave these unset get a valid workflow run.
	MaxAttempts    int
	AttemptSpacing time.Duration
	MaxAgeMinutes  int
	QueryTimeout   time.Duration

	// Dedup thresholds (config.DedupConfig), surfaced through this same
	// start-of-workflow config read so EventWorkflow's in-code video.Match
	// gets them deterministically (recorded in history → replay-safe) rather
	// than reading env from workflow code.
	MaxHamming   int
	MinRunFrames int
	MaxGapFrames int

	// FleetEnabled mirrors config.FirefoxFleetConfig.Enabled (#160). Set
	// at worker init; surfaced to EventWorkflow via GetDiscoveryConfig so
	// the workflow decides deterministically whether to use a per-event
	// instance address.
	FleetEnabled bool
}

// ── GetDiscoveryConfig ─────────────────────────────────────────

// GetDiscoveryConfigInput has no fields.
type GetDiscoveryConfigInput struct{}

// GetDiscoveryConfigOutput exposes env-driven config to
// EventWorkflow. Workflows can't touch env / files directly
// (Temporal determinism), so a trivial activity is the standard
// idiom — matches the ingest.GetIngestConfig pattern.
type GetDiscoveryConfigOutput struct {
	MaxAttempts    int
	AttemptSpacing time.Duration
	MaxAgeMinutes  int
	QueryTimeout   time.Duration

	// Dedup thresholds for EventWorkflow's in-code video.Match.
	MaxHamming   int
	MinRunFrames int
	MaxGapFrames int

	// FleetEnabled mirrors FirefoxFleetConfig.Enabled (#160). When true,
	// the EventWorkflow derives its per-event instance address and passes
	// it to SearchTweets; when false it leaves InstanceAddr empty and
	// SearchTweets uses the shared twitter service.
	FleetEnabled bool
}

// Fallbacks used when config isn't populated on Activities (test
// environments, forgotten wire-up). Match the pre-#162 hardcoded
// values so nothing gets slower in the accidental-omission case.
// Fallback for MaxAttempts is 10 (not 15) because 10 is the pre-#162
// shipped value; the 15 bump is a config-side default, not a fallback.
const (
	fallbackMaxAttempts    = 10
	fallbackAttemptSpacing = 60 * time.Second
	fallbackMaxAgeMinutes  = 3
	fallbackQueryTimeout   = 2 * time.Minute
	// Dedup fallbacks match config.DedupConfig defaults (decisions.md 2026-07-28).
	fallbackMaxHamming   = 10
	fallbackMinRunFrames = 30
	fallbackMaxGapFrames = 3
)

// GetDiscoveryConfig — trivial config accessor for EventWorkflow.
// Returns values from the Activities struct with per-field fallbacks
// so a zero-value Activities in tests still yields a runnable workflow.
func (a *Activities) GetDiscoveryConfig(
	_ context.Context, _ GetDiscoveryConfigInput,
) (GetDiscoveryConfigOutput, error) {
	out := GetDiscoveryConfigOutput{
		MaxAttempts:    a.MaxAttempts,
		AttemptSpacing: a.AttemptSpacing,
		MaxAgeMinutes:  a.MaxAgeMinutes,
		QueryTimeout:   a.QueryTimeout,
		MaxHamming:     a.MaxHamming,
		MinRunFrames:   a.MinRunFrames,
		MaxGapFrames:   a.MaxGapFrames,
		FleetEnabled:   a.FleetEnabled,
	}
	if out.MaxAttempts == 0 {
		out.MaxAttempts = fallbackMaxAttempts
	}
	if out.AttemptSpacing == 0 {
		out.AttemptSpacing = fallbackAttemptSpacing
	}
	if out.MaxAgeMinutes == 0 {
		out.MaxAgeMinutes = fallbackMaxAgeMinutes
	}
	if out.QueryTimeout == 0 {
		out.QueryTimeout = fallbackQueryTimeout
	}
	if out.MaxHamming == 0 {
		out.MaxHamming = fallbackMaxHamming
	}
	if out.MinRunFrames == 0 {
		out.MinRunFrames = fallbackMinRunFrames
	}
	if out.MaxGapFrames == 0 {
		out.MaxGapFrames = fallbackMaxGapFrames
	}
	return out, nil
}

// twitterClient narrows the *twitter.Client surface Discovery uses to
// exactly the verbs SearchTweets needs. Tests inject fakes; prod
// wires the concrete *twitter.Client from S7.
type twitterClient interface {
	Search(ctx context.Context, addr string, req twitter.SearchRequest) (*twitter.SearchResponse, error)
}

// FetchTeamAliasesInput identifies the team whose alias set we need.
// Discovery calls this once at workflow start to hydrate query-builder
// inputs.
type FetchTeamAliasesInput struct {
	TeamID int64
}

// FetchTeamAliasesOutput carries the row shape Discovery needs for
// query construction. Empty CanonicalName + nil Aliases means "team
// not resolved yet" — Discovery falls back to what it has on the
// EventWorkflowInput (TeamName from api-football) as a canonical
// stand-in.
type FetchTeamAliasesOutput struct {
	CanonicalName string
	Aliases       []string
	Found         bool // false = no row for this team_id (unusual; ingest should have created a placeholder)
}

// FetchTeamAliases reads the team_aliases row for a given team.
// Returns Found=false with empty fields if no row exists — Discovery
// treats that as a fallback signal, not a hard error, because the
// alias-resolution pipeline may lag behind Ingest during startup.
func (a *Activities) FetchTeamAliases(ctx context.Context, in FetchTeamAliasesInput) (FetchTeamAliasesOutput, error) {
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var out FetchTeamAliasesOutput
	err := a.Pool.QueryRow(callCtx, `
		SELECT canonical_name, aliases
		FROM team_aliases
		WHERE team_id = $1
	`, in.TeamID).Scan(&out.CanonicalName, &out.Aliases)
	if err == pgx.ErrNoRows {
		return FetchTeamAliasesOutput{Found: false}, nil
	}
	if err != nil {
		return FetchTeamAliasesOutput{}, fmt.Errorf("discovery.FetchTeamAliases: team_id=%d: %w", in.TeamID, err)
	}
	out.Found = true
	return out, nil
}

// SearchTweetsInput carries what SearchTweets needs to construct a
// query + record the outcome. Kept minimal — the workflow builds the
// query string itself from event data before invoking the activity.
type SearchTweetsInput struct {
	EventID   uuid.UUID
	FixtureID int64
	// Query is the pre-assembled search string. EventWorkflow builds it from
	// deterministic player tokens plus the canonical team name.
	Query string
	// ExcludeURLs — tweet URLs EventWorkflow has already processed in prior
	// attempts. Empty on the first attempt. The Go Twitter service uses it for
	// per-tweet skip and the consecutive-seen early stop.
	ExcludeURLs []string
	// MaxAgeMinutes bounds how far back Twitter scrolls. Default 5
	// (Python's default) if zero.
	MaxAgeMinutes int
	// InstanceAddr targets a per-event Firefox instance (#160), e.g.
	// http://ff-firefox-ev-<id>:8888. Empty → the shared twitter service
	// (fleet disabled, or pre-#160). The EventWorkflow derives it from
	// the event ID when the fleet is enabled.
	InstanceAddr string
}

// SearchTweetsOutput reports what came back. Videos is the list of
// tweet + CDN + duration triples for downstream Video pipeline. Empty
// list is a valid outcome (no candidates found — Discovery just
// completes with count=0). StopReason is the T/c scroll-loop
// termination class. The remaining counters distinguish an absent feed from
// incomplete media hydration and an exhausted rendered feed.
type SearchTweetsOutput struct {
	Videos          []twitter.VideoRef
	Count           int
	StopReason      string
	Scrolls         int
	InitialArticles int
	TweetsParsed    int
	VideoTweets     int
}

// SearchTweets calls the Go Twitter service and returns discovered video tweets.
// Errors from the Twitter service surface here — Temporal retries
// with backoff per the activity registration in EventWorkflow.
func (a *Activities) SearchTweets(ctx context.Context, in SearchTweetsInput) (SearchTweetsOutput, error) {
	// A cold/contended per-event Firefox scroll+scrape legitimately exceeds the
	// 30s HeartbeatTimeout; keep the attempt alive (#184 audit P0-2).
	defer heartbeat.Keepalive(ctx, heartbeat.Interval)()
	if a.Twitter == nil {
		return SearchTweetsOutput{}, fmt.Errorf("discovery.SearchTweets: Twitter client not wired")
	}
	if in.Query == "" {
		return SearchTweetsOutput{}, fmt.Errorf("discovery.SearchTweets: empty query")
	}
	maxAge := in.MaxAgeMinutes
	if maxAge == 0 {
		maxAge = fallbackMaxAgeMinutes
	}
	resp, err := a.Twitter.Search(ctx, in.InstanceAddr, twitter.SearchRequest{
		Query:         in.Query,
		ExcludeURLs:   in.ExcludeURLs,
		MaxAgeMinutes: maxAge,
	})
	if err != nil {
		return SearchTweetsOutput{}, fmt.Errorf("discovery.SearchTweets: %w", err)
	}
	return SearchTweetsOutput{
		Videos:          resp.Videos,
		Count:           resp.Count,
		StopReason:      resp.StopReason,
		Scrolls:         resp.Scrolls,
		InitialArticles: resp.InitialArticles,
		TweetsParsed:    resp.TweetsParsed,
		VideoTweets:     resp.VideoTweets,
	}, nil
}

// StoreCandidateInput is the workflow-owned evidence persisted when a
// candidate is first observed. The alias preserves the registered activity's
// historical payload shape while keeping one canonical evidence contract.
type StoreCandidateInput = ddiscovery.CandidateEvidence

// StoreCandidateOutput reports whether the row was inserted or
// deduplicated. Inserted=false on ON CONFLICT hit — expected during
// retry of the SAME attempt after a partial-success crash, but on
// happy path each candidate hits Inserted=true.
type StoreCandidateOutput struct {
	Inserted bool
}

// StoreCandidate inserts one candidate into event_search_candidates.
// Uses ON CONFLICT (event_id, tweet_url) DO NOTHING so the activity is
// idempotent on retry. Runs one SQL statement per candidate — batch
// insertion is a future optimization; typical Discovery attempts
// surface <20 candidates so per-candidate insert overhead is trivial.
func (a *Activities) StoreCandidate(ctx context.Context, in StoreCandidateInput) (StoreCandidateOutput, error) {
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Null the age field when we didn't extract it (age=0 is used as
	// the sentinel by the search endpoint's decodeExtractResult).
	var agePtr *float64
	if in.AgeMinutesAtDiscovery > 0 {
		agePtr = &in.AgeMinutesAtDiscovery
	}

	tag, err := a.Pool.Exec(callCtx, `
		INSERT INTO event_search_candidates (
			event_id, fixture_id, search_attempt, query,
			tweet_url, tweet_text, video_page_url, duration_seconds,
			username, age_minutes_at_discovery
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10
		)
		ON CONFLICT (event_id, tweet_url) DO NOTHING
	`,
		in.EventID, in.FixtureID, in.SearchAttempt, in.Query,
		in.TweetURL, in.TweetText, in.VideoPageURL, in.DurationSeconds,
		in.Username, agePtr,
	)
	if err != nil {
		return StoreCandidateOutput{}, fmt.Errorf("discovery.StoreCandidate: event=%s tweet=%s: %w", in.EventID, in.TweetURL, err)
	}
	return StoreCandidateOutput{Inserted: tag.RowsAffected() == 1}, nil
}

// RecoveryCandidate is one candidate already owned by the event. Evidence and
// State are the FF-034 contract. TweetURL and Pending remain populated for
// replay of histories recorded before that contract existed.
type RecoveryCandidate struct {
	Evidence ddiscovery.CandidateEvidence
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
	AttemptsCompleted int
	Candidates        []RecoveryCandidate
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
	if err := a.Pool.QueryRow(callCtx, `
		SELECT COALESCE((metadata->>'attempts_completed')::int, 0)
		FROM event_downstream_workflows
		WHERE event_id = $1 AND workflow_type = $2 AND workflow_id = $3
	`, in.EventID, in.WorkflowType, in.WorkflowID).Scan(&out.AttemptsCompleted); err != nil {
		return out, fmt.Errorf("discovery.LoadEventRecoveryState: checklist event=%s workflow=%s: %w",
			in.EventID, in.WorkflowID, err)
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

// RecordDiscoveryProgressInput checkpoints one fully scheduled search attempt.
type RecordDiscoveryProgressInput struct {
	EventID      uuid.UUID
	WorkflowType string
	WorkflowID   string
	Attempt      int
}

// RecordDiscoveryProgress monotonically advances attempts_completed in the
// checklist metadata. A lower replayed value is a no-op; a missing row is an
// invariant failure because monitor must register before spawning.
func (a *Activities) RecordDiscoveryProgress(ctx context.Context, in RecordDiscoveryProgressInput) error {
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	tag, err := a.Pool.Exec(callCtx, `
		UPDATE event_downstream_workflows
		SET metadata = jsonb_set(
			COALESCE(metadata, '{}'::jsonb),
			'{attempts_completed}',
			to_jsonb(GREATEST(COALESCE((metadata->>'attempts_completed')::int, 0), $4::int)),
			true
		)
		WHERE event_id = $1 AND workflow_type = $2 AND workflow_id = $3
	`, in.EventID, in.WorkflowType, in.WorkflowID, in.Attempt)
	if err != nil {
		return fmt.Errorf("discovery.RecordDiscoveryProgress: event=%s attempt=%d: %w", in.EventID, in.Attempt, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("discovery.RecordDiscoveryProgress: checklist missing for event=%s workflow=%s",
			in.EventID, in.WorkflowID)
	}
	return nil
}

// CandidateOutcome is the terminal per-candidate class recorded on
// event_search_candidates.outcome_class (#181). Coarse by design — the
// fine-grained "why" rides reject_reason + outcome_detail. Values mirror the
// schema CHECK constraint.
type CandidateOutcome string

const (
	OutcomePromoted   CandidateOutcome = "promoted"   // surfaced as an asset/share
	OutcomeDuplicate  CandidateOutcome = "duplicate"  // md5/perceptual dup, collapsed onto a winner
	OutcomeSuperseded CandidateOutcome = "superseded" // promoted, then replaced by a better clip
	OutcomeRejected   CandidateOutcome = "rejected"   // download-stage or vision reject (reject_reason says which)
	OutcomeFailed     CandidateOutcome = "failed"     // child/infra error — never got a clean verdict
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
	Evidence     ddiscovery.CandidateEvidence
	Outcome      CandidateOutcome
	RejectReason string
	Detail       json.RawMessage
}

// UpsertCandidateOutcome durably records one terminal candidate state. On a
// conflict it preserves the first-observation evidence and updates only the
// terminal fields. The fixture guard turns an impossible identity mismatch
// into an error instead of attaching an outcome to the wrong event evidence.
func (a *Activities) UpsertCandidateOutcome(ctx context.Context, in UpsertCandidateOutcomeInput) error {
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if in.Evidence.EventID == uuid.Nil || in.Evidence.FixtureID <= 0 ||
		in.Evidence.SearchAttempt <= 0 || in.Evidence.Query == "" || in.Evidence.TweetURL == "" {
		return fmt.Errorf("discovery.UpsertCandidateOutcome: incomplete evidence for event=%s tweet=%s",
			in.Evidence.EventID, in.Evidence.TweetURL)
	}
	switch in.Outcome {
	case OutcomePromoted, OutcomeDuplicate, OutcomeSuperseded, OutcomeRejected, OutcomeFailed:
	default:
		return fmt.Errorf("discovery.UpsertCandidateOutcome: non-terminal outcome %q", in.Outcome)
	}

	var age *float64
	if in.Evidence.AgeMinutesAtDiscovery > 0 {
		age = &in.Evidence.AgeMinutesAtDiscovery
	}
	var reason *string
	if in.RejectReason != "" {
		reason = &in.RejectReason
	}
	var detail []byte
	if len(in.Detail) > 0 {
		detail = in.Detail
	}

	tag, err := a.Pool.Exec(callCtx, `
		INSERT INTO event_search_candidates (
			event_id, fixture_id, search_attempt, query,
			tweet_url, tweet_text, video_page_url, duration_seconds,
			username, age_minutes_at_discovery,
			outcome_class, reject_reason, outcome_detail, outcome_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8,
			$9, $10,
			$11, $12, $13, NOW()
		)
		ON CONFLICT (event_id, tweet_url) DO UPDATE
		SET outcome_class = EXCLUDED.outcome_class,
		    reject_reason = EXCLUDED.reject_reason,
		    outcome_detail = EXCLUDED.outcome_detail,
		    outcome_at = CASE
		        WHEN event_search_candidates.outcome_class = EXCLUDED.outcome_class
		         AND event_search_candidates.reject_reason IS NOT DISTINCT FROM EXCLUDED.reject_reason
		         AND event_search_candidates.outcome_detail IS NOT DISTINCT FROM EXCLUDED.outcome_detail
		        THEN COALESCE(event_search_candidates.outcome_at, EXCLUDED.outcome_at)
		        ELSE EXCLUDED.outcome_at
		    END
		WHERE event_search_candidates.fixture_id = EXCLUDED.fixture_id
	`,
		in.Evidence.EventID,
		in.Evidence.FixtureID,
		in.Evidence.SearchAttempt,
		in.Evidence.Query,
		in.Evidence.TweetURL,
		in.Evidence.TweetText,
		in.Evidence.VideoPageURL,
		in.Evidence.DurationSeconds,
		in.Evidence.Username,
		age,
		string(in.Outcome),
		reason,
		detail,
	)
	if err != nil {
		return fmt.Errorf("discovery.UpsertCandidateOutcome: event=%s tweet=%s: %w",
			in.Evidence.EventID, in.Evidence.TweetURL, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("discovery.UpsertCandidateOutcome: evidence identity mismatch for event=%s tweet=%s",
			in.Evidence.EventID, in.Evidence.TweetURL)
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

	var reason *string
	if in.RejectReason != "" {
		reason = &in.RejectReason
	}
	var detail []byte
	if len(in.Detail) > 0 {
		detail = in.Detail
	}
	if _, err := a.Pool.Exec(callCtx, `
		UPDATE event_search_candidates
		   SET outcome_class = $3, reject_reason = $4, outcome_detail = $5, outcome_at = NOW()
		 WHERE event_id = $1 AND tweet_url = $2
	`, in.EventID, in.TweetURL, string(in.Outcome), reason, detail); err != nil {
		return fmt.Errorf("discovery.RecordCandidateOutcome: event=%s tweet=%s: %w", in.EventID, in.TweetURL, err)
	}
	return nil
}

// MarkDownstreamCompleteInput identifies which row to mark complete.
// event_id + workflow_type + workflow_id uniquely identifies the row
// (the table's PRIMARY KEY).
type MarkDownstreamCompleteInput struct {
	EventID      uuid.UUID
	WorkflowType string
	WorkflowID   string
	// OutcomeClass — free-form short string. "stub_ok" from the
	// current Discovery stub. Later phases: "success", "no_candidates",
	// "twitter_rate_limited", etc.
	OutcomeClass string
}

// MarkDownstreamCompleteOutput reports whether a row was actually
// updated. If not, either the row wasn't inserted (bug in the spawn
// path) or was already completed (retry, expected).
type MarkDownstreamCompleteOutput struct {
	RowsUpdated int64
}

// MarkDownstreamComplete UPDATEs the pending row for the given
// (event_id, workflow_type, workflow_id) triple, setting completed_at
// = NOW() and outcome_class. If completed_at is already set (activity
// retry after the UPDATE landed but the return was lost), leaves it
// alone. RowsUpdated tells callers which case they hit.
func (a *Activities) MarkDownstreamComplete(ctx context.Context, in MarkDownstreamCompleteInput) (MarkDownstreamCompleteOutput, error) {
	// Use a short pg-side timeout on top of Temporal's activity
	// StartToClose — an activity retry is fine but a stuck query is
	// not. 5s covers the round trip comfortably.
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	tag, err := a.Pool.Exec(callCtx, `
		UPDATE event_downstream_workflows
		SET completed_at = NOW(), outcome_class = $4
		WHERE event_id = $1
		  AND workflow_type = $2
		  AND workflow_id = $3
		  AND completed_at IS NULL
	`, in.EventID, in.WorkflowType, in.WorkflowID, in.OutcomeClass)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Not fatal — either the row exists but is already
			// completed, or it never got inserted. Both are recoverable.
			return MarkDownstreamCompleteOutput{RowsUpdated: 0}, nil
		}
		return MarkDownstreamCompleteOutput{}, fmt.Errorf("discovery.MarkDownstreamComplete: %w", err)
	}
	return MarkDownstreamCompleteOutput{RowsUpdated: tag.RowsAffected()}, nil
}
