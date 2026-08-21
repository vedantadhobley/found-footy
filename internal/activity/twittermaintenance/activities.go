// Package twittermaintenance holds the fixture-independent Twitter session
// and DOM canary activity.
package twittermaintenance

import (
	"context"
	"fmt"
	"net/url"
	"strings"

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
	StopReason      string
	InitialArticles int
	TweetsParsed    int
	VideoTweets     int
	VideosFound     int
}

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
		return RunTwitterMaintenanceOutput{}, fmt.Errorf("twitter maintenance: canary search: %w", err)
	}
	if resp == nil {
		return RunTwitterMaintenanceOutput{}, fmt.Errorf("twitter maintenance: canary search returned no response")
	}
	out := RunTwitterMaintenanceOutput{
		StopReason:      resp.StopReason,
		InitialArticles: resp.InitialArticles,
		TweetsParsed:    resp.TweetsParsed,
		VideoTweets:     resp.VideoTweets,
		VideosFound:     resp.Count,
	}
	if resp.StopReason == "feed_timeout" || resp.InitialArticles == 0 {
		return out, fmt.Errorf("twitter maintenance: no search feed rendered (stop_reason=%s)", resp.StopReason)
	}
	if resp.TweetsParsed < in.MinTweets {
		return out, fmt.Errorf("twitter maintenance: parsed %d tweets, require at least %d", resp.TweetsParsed, in.MinTweets)
	}
	if resp.VideoTweets < in.MinVideos || len(resp.Videos) < in.MinVideos {
		return out, fmt.Errorf(
			"twitter maintenance: found %d video tweets and returned %d videos, require at least %d",
			resp.VideoTweets, len(resp.Videos), in.MinVideos,
		)
	}
	for _, video := range resp.Videos {
		if !validStatusURL(video.TweetURL) {
			return out, fmt.Errorf("twitter maintenance: malformed status URL %q", video.TweetURL)
		}
	}
	return out, nil
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
