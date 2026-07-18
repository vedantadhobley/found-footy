// Activities for DiscoveryWorkflow. Currently just one:
// MarkDownstreamComplete updates the event_downstream_workflows row
// that Monitor inserted pre-spawn so FixtureReadyToComplete stops
// treating the workflow as pending. When real Discovery work lands
// (Twitter search, candidate extraction, downstream Video spawn),
// those activities will land in this package.
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
type Activities struct {
	Pool    *pg.Pool
	Twitter twitterClient
}

// twitterClient narrows the *twitter.Client surface Discovery uses to
// exactly the verbs SearchTweets needs. Tests inject fakes; prod
// wires the concrete *twitter.Client from S7.
type twitterClient interface {
	Search(ctx context.Context, req twitter.SearchRequest) (*twitter.SearchResponse, error)
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
// completes with count=0).
type SearchTweetsOutput struct {
	Videos []twitter.VideoRef
	Count  int
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
		Videos: resp.Videos,
		Count:  resp.Count,
	}, nil
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
