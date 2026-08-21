// twitter_maintenance.go defines the fixture-independent Twitter authentication
// and search-DOM canary workflow.
package workflow

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	twittermaintenance "github.com/vedantadhobley/found-footy/internal/activity/twittermaintenance"
)

const (
	defaultTwitterCanaryQuery     = "football goal filter:videos"
	defaultTwitterCanaryMaxAge    = 1440
	defaultTwitterCanaryMinTweets = 3
	defaultTwitterCanaryMinVideos = 3
)

// TwitterMaintenanceWorkflowInput carries optional canary overrides. Scheduled
// production runs use the defaults; explicit values make local probes and
// workflow tests deterministic.
type TwitterMaintenanceWorkflowInput struct {
	Query         string
	MaxAgeMinutes int
	MinTweets     int
	MinVideos     int
}

// TwitterMaintenanceWorkflow executes one non-retrying maintenance probe. A
// failure remains visible until the next scheduled run rather than becoming a
// burst of repeated X traffic.
func TwitterMaintenanceWorkflow(
	ctx workflow.Context,
	in TwitterMaintenanceWorkflowInput,
) (twittermaintenance.RunTwitterMaintenanceOutput, error) {
	if in.Query == "" {
		in.Query = defaultTwitterCanaryQuery
	}
	if in.MaxAgeMinutes == 0 {
		in.MaxAgeMinutes = defaultTwitterCanaryMaxAge
	}
	if in.MinTweets == 0 {
		in.MinTweets = defaultTwitterCanaryMinTweets
	}
	if in.MinVideos == 0 {
		in.MinVideos = defaultTwitterCanaryMinVideos
	}

	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 3 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	var out twittermaintenance.RunTwitterMaintenanceOutput
	err := workflow.ExecuteActivity(ctx, "RunTwitterMaintenance", twittermaintenance.RunTwitterMaintenanceInput{
		Query:         in.Query,
		MaxAgeMinutes: in.MaxAgeMinutes,
		MinTweets:     in.MinTweets,
		MinVideos:     in.MinVideos,
	}).Get(ctx, &out)
	if err != nil {
		return out, fmt.Errorf("TwitterMaintenanceWorkflow: %w", err)
	}
	workflow.GetLogger(ctx).Info("Twitter maintenance passed",
		"result_state", out.ResultState,
		"stop_reason", out.StopReason,
		"initial_articles", out.InitialArticles,
		"tweets_parsed", out.TweetsParsed,
		"video_tweets", out.VideoTweets,
		"videos_found", out.VideosFound,
		"timeline_seen", out.Evidence.TimelineSeen,
		"timeline_status", out.Evidence.TimelineStatus,
	)
	return out, nil
}
