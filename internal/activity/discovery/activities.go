// Activities for DiscoveryWorkflow. Four activities cover the
// production shape:
//
//   1. FetchTeamAliases — pull team_aliases row (canonical_name +
//      curated aliases) for the scoring team so the query builder
//      has real inputs.
//   2. SearchTweets — call the Twitter service's POST /search with
//      the query builder's output + accumulated exclude_urls.
//   3. StoreCandidate — persist one candidate tweet to
//      event_search_candidates. Idempotent via
//      ON CONFLICT DO NOTHING on (event_id, tweet_url).
//   4. MarkDownstreamComplete — updates event_downstream_workflows
//      so FixtureReadyToComplete stops treating this workflow as
//      pending.
package discovery

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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

	// DiscoveryWorkflow tuning knobs, mirrored from
	// config.DiscoveryConfig at worker init. Zero values are
	// treated as "use hardcoded fallback" inside GetDiscoveryConfig
	// so tests that leave these unset get a valid workflow run.
	MaxAttempts    int
	AttemptSpacing time.Duration
	MaxAgeMinutes  int
	QueryTimeout   time.Duration
}

// ── GetDiscoveryConfig ─────────────────────────────────────────

// GetDiscoveryConfigInput has no fields.
type GetDiscoveryConfigInput struct{}

// GetDiscoveryConfigOutput exposes env-driven config to
// DiscoveryWorkflow. Workflows can't touch env / files directly
// (Temporal determinism), so a trivial activity is the standard
// idiom — matches the ingest.GetIngestConfig pattern.
type GetDiscoveryConfigOutput struct {
	MaxAttempts    int
	AttemptSpacing time.Duration
	MaxAgeMinutes  int
	QueryTimeout   time.Duration
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
)

// GetDiscoveryConfig — trivial config accessor for DiscoveryWorkflow.
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
	return out, nil
}

// twitterClient narrows the *twitter.Client surface Discovery uses to
// exactly the verbs SearchTweets needs. Tests inject fakes; prod
// wires the concrete *twitter.Client from S7.
type twitterClient interface {
	Search(ctx context.Context, req twitter.SearchRequest) (*twitter.SearchResponse, error)
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
// DiscoveryWorkflowInput (TeamName from api-football) as a canonical
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
	// Query is the pre-assembled search string. Discovery-workflow
	// builds it from (player, team, "goal") for the MVP. Team-alias
	// OR-syntax expansion via Wikidata RAG lands in a follow-up.
	Query string
	// ExcludeURLs — tweet URLs Discovery has already processed in
	// prior attempts. Empty on the first attempt. Feeds Python
	// twitter service's per-tweet skip + (future) consecutive-
	// already-seen scroll early-stop.
	ExcludeURLs []string
	// MaxAgeMinutes bounds how far back Twitter scrolls. Default 5
	// (Python's default) if zero.
	MaxAgeMinutes int
}

// SearchTweetsOutput reports what came back. Videos is the list of
// tweet + CDN + duration triples for downstream Video pipeline. Empty
// list is a valid outcome (no candidates found — Discovery just
// completes with count=0). StopReason is the T/c scroll-loop
// termination class ("age" / "max_scrolls" / "empty" /
// "consecutive_seen") — kept as observability metadata for Discovery
// loop logging.
type SearchTweetsOutput struct {
	Videos     []twitter.VideoRef
	Count      int
	StopReason string
}

// SearchTweets calls the Twitter service (currently Python's
// twitter/ container via S7's HTTP client; will point at the Go
// service once T ships) and returns the discovered video tweets.
// Errors from the Twitter service surface here — Temporal retries
// with backoff per the activity registration in DiscoveryWorkflow.
func (a *Activities) SearchTweets(ctx context.Context, in SearchTweetsInput) (SearchTweetsOutput, error) {
	if a.Twitter == nil {
		return SearchTweetsOutput{}, fmt.Errorf("discovery.SearchTweets: Twitter client not wired")
	}
	if in.Query == "" {
		return SearchTweetsOutput{}, fmt.Errorf("discovery.SearchTweets: empty query")
	}
	maxAge := in.MaxAgeMinutes
	if maxAge == 0 {
		maxAge = 5
	}
	resp, err := a.Twitter.Search(ctx, twitter.SearchRequest{
		Query:         in.Query,
		ExcludeURLs:   in.ExcludeURLs,
		MaxAgeMinutes: maxAge,
	})
	if err != nil {
		return SearchTweetsOutput{}, fmt.Errorf("discovery.SearchTweets: %w", err)
	}
	return SearchTweetsOutput{
		Videos:     resp.Videos,
		Count:      resp.Count,
		StopReason: resp.StopReason,
	}, nil
}

// StoreCandidateInput is one candidate tweet Discovery persists to
// event_search_candidates. Fields mirror the schema — see schema.sql
// for the intent.
type StoreCandidateInput struct {
	EventID              uuid.UUID
	FixtureID            int64
	SearchAttempt        int
	Query                string
	TweetURL             string
	TweetText            string
	VideoPageURL         string
	DurationSeconds      float64
	Username             string
	AgeMinutesAtDiscovery float64 // 0 = not extracted; NULL in the DB
}

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
