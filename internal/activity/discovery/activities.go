// Activities for EventWorkflow. Seven activities cover the
// production shape:
//
//  1. FetchTeamAliases — pull the canonical team name from the retained
//     team_aliases compatibility row so the query builder has a stable input.
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
//  7. MarkDownstreamComplete — closes one exact event_downstream_workflows
//     identity so AssessCompletion stops treating it as pending; missing rows
//     fail rather than masquerading as idempotent completion.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.temporal.io/sdk/temporal"

	"github.com/vedantadhobley/found-footy/internal/activity/heartbeat"
	twittercontract "github.com/vedantadhobley/found-footy/internal/contract/twittersearch"
	"github.com/vedantadhobley/found-footy/internal/domain/event"
	"github.com/vedantadhobley/found-footy/internal/infra/pg"
	"github.com/vedantadhobley/found-footy/internal/infra/twitter"
)

// Activities bundles Discovery's activity implementations. Held on
// *Activities so tests can inject fakes for the pg pool via a
// pool-shaped interface (currently just the concrete pg.Pool).
//
// Config fields (MaxAttempts, MaxUnavailableAttempts, AttemptSpacing,
// MaxAgeMinutes, QueryTimeout) mirror config.DiscoveryConfig. Populated at
// cmd/worker startup — see GetDiscoveryConfig below for the
// workflow-side accessor.
type Activities struct {
	Pool       *pg.Pool
	Twitter    twitterClient
	Downstream event.DownstreamCompletionRepo

	// EventWorkflow tuning knobs, mirrored from
	// config.DiscoveryConfig at worker init. Zero values are
	// treated as "use hardcoded fallback" inside GetDiscoveryConfig
	// so tests that leave these unset get a valid workflow run.
	MaxAttempts            int
	MaxUnavailableAttempts int
	AttemptSpacing         time.Duration
	MaxAgeMinutes          int
	QueryTimeout           time.Duration

	// Dedup thresholds (config.DedupConfig), surfaced through this same
	// start-of-workflow config read so EventWorkflow's in-code video.Match
	// gets them deterministically (recorded in history → replay-safe) rather
	// than reading env from workflow code.
	MaxHamming       int
	MinRunFrames     int
	MaxGapFrames     int
	LongMaxHamming   int
	LongMinRunFrames int
	LongMaxGapFrames int

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
	MaxAttempts            int
	MaxUnavailableAttempts int
	AttemptSpacing         time.Duration
	MaxAgeMinutes          int
	QueryTimeout           time.Duration

	// Dedup thresholds for EventWorkflow's in-code video.Match.
	MaxHamming       int
	MinRunFrames     int
	MaxGapFrames     int
	LongMaxHamming   int
	LongMinRunFrames int
	LongMaxGapFrames int

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
	// Dedup fallbacks match config.DedupConfig defaults.
	fallbackMaxHamming       = 12
	fallbackMinRunFrames     = 30
	fallbackMaxGapFrames     = 3
	fallbackLongMaxHamming   = 16
	fallbackLongMinRunFrames = 50
	fallbackLongMaxGapFrames = 5
)

// GetDiscoveryConfig — trivial config accessor for EventWorkflow.
// Returns values from the Activities struct with per-field fallbacks
// so a zero-value Activities in tests still yields a runnable workflow.
func (a *Activities) GetDiscoveryConfig(
	_ context.Context, _ GetDiscoveryConfigInput,
) (GetDiscoveryConfigOutput, error) {
	out := GetDiscoveryConfigOutput{
		MaxAttempts:            a.MaxAttempts,
		MaxUnavailableAttempts: a.MaxUnavailableAttempts,
		AttemptSpacing:         a.AttemptSpacing,
		MaxAgeMinutes:          a.MaxAgeMinutes,
		QueryTimeout:           a.QueryTimeout,
		MaxHamming:             a.MaxHamming,
		MinRunFrames:           a.MinRunFrames,
		MaxGapFrames:           a.MaxGapFrames,
		LongMaxHamming:         a.LongMaxHamming,
		LongMinRunFrames:       a.LongMinRunFrames,
		LongMaxGapFrames:       a.LongMaxGapFrames,
		FleetEnabled:           a.FleetEnabled,
	}
	if out.MaxAttempts == 0 {
		out.MaxAttempts = fallbackMaxAttempts
	}
	if out.MaxUnavailableAttempts == 0 {
		out.MaxUnavailableAttempts = out.MaxAttempts
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
	if out.LongMaxHamming == 0 {
		out.LongMaxHamming = fallbackLongMaxHamming
	}
	if out.LongMinRunFrames == 0 {
		out.LongMinRunFrames = fallbackLongMinRunFrames
	}
	if out.LongMaxGapFrames == 0 {
		out.LongMaxGapFrames = fallbackLongMaxGapFrames
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
	ResultState     twittercontract.ResultState
	Evidence        twittercontract.SearchEvidence
	StopReason      string
	Scrolls         int
	InitialArticles int
	TweetsParsed    int
	VideoTweets     int
}

// SearchUnavailableErrorType identifies retryable classified browser failures.
// Its details carry SearchTweetsOutput so FF-061 workflows can account for the
// outage while older histories retain their original activity retry policy.
const SearchUnavailableErrorType = "twitter_search_unavailable"

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
		var searchErr *twitter.SearchError
		if errors.As(err, &searchErr) && searchErr.ResultState.Known() {
			classified := SearchTweetsOutput{
				ResultState: searchErr.ResultState,
				Evidence:    searchErr.Evidence,
			}
			return SearchTweetsOutput{}, temporal.NewApplicationErrorWithOptions(
				"classified Twitter search unavailable",
				SearchUnavailableErrorType,
				temporal.ApplicationErrorOptions{
					Cause:   err,
					Details: []any{classified},
				},
			)
		}
		return SearchTweetsOutput{}, fmt.Errorf("discovery.SearchTweets: %w", err)
	}
	return SearchTweetsOutput{
		Videos:          resp.Videos,
		Count:           resp.Count,
		ResultState:     resp.ResultState,
		Evidence:        resp.Evidence,
		StopReason:      resp.StopReason,
		Scrolls:         resp.Scrolls,
		InitialArticles: resp.InitialArticles,
		TweetsParsed:    resp.TweetsParsed,
		VideoTweets:     resp.VideoTweets,
	}, nil
}
