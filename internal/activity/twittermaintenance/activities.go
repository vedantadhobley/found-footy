// Package twittermaintenance holds the fixture-independent Twitter session
// and DOM canary activity.
package twittermaintenance

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"go.temporal.io/sdk/temporal"

	twittercontract "github.com/vedantadhobley/found-footy/internal/contract/twittersearch"
	twitterinfra "github.com/vedantadhobley/found-footy/internal/infra/twitter"
)

// twitterClient is the static Twitter service surface maintenance needs.
// Production provides *twitter.Client; tests use an in-memory fake.
type twitterClient interface {
	Verify(context.Context) error
	Search(context.Context, string, twitterinfra.SearchRequest) (*twitterinfra.SearchResponse, error)
}

// Activities bundles the static Twitter client.
type Activities struct {
	Twitter twitterClient
}

// RunTwitterMaintenanceInput configures one authentication and DOM probe.
type RunTwitterMaintenanceInput struct {
	Query         string
	MaxAgeMinutes int
	MinTweets     int
	MinVideos     int
}

// RunTwitterMaintenanceOutput records the canary evidence retained in Temporal.
type RunTwitterMaintenanceOutput struct {
	ResultState     twittercontract.ResultState
	Evidence        twittercontract.SearchEvidence
	StopReason      string
	InitialArticles int
	TweetsParsed    int
	VideoTweets     int
	VideosFound     int
}

// CanaryErrorType marks a non-retrying maintenance failure whose details
// retain the classified search output in Temporal history.
const CanaryErrorType = "twitter_maintenance_canary_failed"

// RunTwitterMaintenance forces a live auth verification and cookie sync, then
// searches the static fallback browser with a broad evergreen query. It fails
// on absent feed evidence or malformed result URLs so the Temporal schedule is
// an actionable canary rather than a green HTTP-only probe.
func (a *Activities) RunTwitterMaintenance(
	ctx context.Context,
	in RunTwitterMaintenanceInput,
) (RunTwitterMaintenanceOutput, error) {
	if a.Twitter == nil {
		return RunTwitterMaintenanceOutput{}, fmt.Errorf("twitter maintenance: client not wired")
	}
	if err := a.Twitter.Verify(ctx); err != nil {
		return RunTwitterMaintenanceOutput{}, fmt.Errorf("twitter maintenance: verify: %w", err)
	}
	resp, err := a.Twitter.Search(ctx, "", twitterinfra.SearchRequest{
		Query:         in.Query,
		MaxAgeMinutes: in.MaxAgeMinutes,
	})
	if err != nil {
		var searchErr *twitterinfra.SearchError
		if errors.As(err, &searchErr) && searchErr.ResultState.Known() {
			out := RunTwitterMaintenanceOutput{
				ResultState: searchErr.ResultState,
				Evidence:    searchErr.Evidence,
			}
			return out, canaryFailure(out, "twitter maintenance: canary search: "+err.Error(), err)
		}
		return RunTwitterMaintenanceOutput{}, fmt.Errorf("twitter maintenance: canary search: %w", err)
	}
	if resp == nil {
		return RunTwitterMaintenanceOutput{}, fmt.Errorf("twitter maintenance: canary search returned no response")
	}
	out := RunTwitterMaintenanceOutput{
		ResultState:     resp.ResultState,
		Evidence:        resp.Evidence,
		StopReason:      resp.StopReason,
		InitialArticles: resp.InitialArticles,
		TweetsParsed:    resp.TweetsParsed,
		VideoTweets:     resp.VideoTweets,
		VideosFound:     resp.Count,
	}
	if resp.ResultState.Known() && resp.ResultState != twittercontract.ResultRendered {
		return out, canaryFailure(out, fmt.Sprintf(
			"twitter maintenance: search feed state=%s (stop_reason=%s)",
			resp.ResultState, resp.StopReason), nil)
	}
	if resp.StopReason == "feed_timeout" || resp.InitialArticles == 0 {
		return out, canaryFailure(out, fmt.Sprintf(
			"twitter maintenance: no search feed rendered (state=%s stop_reason=%s)",
			resp.ResultState, resp.StopReason), nil)
	}
	if resp.TweetsParsed < in.MinTweets {
		return out, canaryFailure(out, fmt.Sprintf(
			"twitter maintenance: parsed %d tweets, require at least %d",
			resp.TweetsParsed, in.MinTweets), nil)
	}
	if resp.VideoTweets < in.MinVideos || len(resp.Videos) < in.MinVideos {
		return out, canaryFailure(out, fmt.Sprintf(
			"twitter maintenance: found %d video tweets and returned %d videos, require at least %d",
			resp.VideoTweets, len(resp.Videos), in.MinVideos,
		), nil)
	}
	for _, video := range resp.Videos {
		if !validStatusURL(video.TweetURL) {
			return out, canaryFailure(out, fmt.Sprintf(
				"twitter maintenance: malformed status URL %q", video.TweetURL), nil)
		}
	}
	return out, nil
}

func canaryFailure(out RunTwitterMaintenanceOutput, message string, cause error) error {
	return temporal.NewApplicationErrorWithOptions(message, CanaryErrorType,
		temporal.ApplicationErrorOptions{
			NonRetryable: true,
			Cause:        cause,
			Details:      []any{out},
		})
}

func validStatusURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || !strings.Contains(parsed.Path, "/status/") {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "x.com" || strings.HasSuffix(host, ".x.com") ||
		host == "twitter.com" || strings.HasSuffix(host, ".twitter.com")
}
