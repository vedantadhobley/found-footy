// Tests for the fixture-independent Twitter maintenance activity.
package twittermaintenance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.temporal.io/sdk/temporal"

	twittercontract "github.com/vedantadhobley/found-footy/internal/contract/twittersearch"
	twitterinfra "github.com/vedantadhobley/found-footy/internal/infra/twitter"
)

type fakeTwitter struct {
	verifyErr error
	searchOut *twitterinfra.SearchResponse
	searchErr error
}

func (f *fakeTwitter) Verify(context.Context) error { return f.verifyErr }
func (f *fakeTwitter) Search(
	context.Context, string, twitterinfra.SearchRequest,
) (*twitterinfra.SearchResponse, error) {
	return f.searchOut, f.searchErr
}

func TestRunTwitterMaintenancePassesWithAuthAndDOMEvidence(t *testing.T) {
	fake := &fakeTwitter{searchOut: &twitterinfra.SearchResponse{
		ResultState:     twittercontract.ResultRendered,
		Evidence:        twittercontract.SearchEvidence{TimelineSeen: true, TimelineStatus: 200},
		Count:           3,
		StopReason:      "age",
		InitialArticles: 5,
		TweetsParsed:    4,
		VideoTweets:     3,
		Videos: []twitterinfra.VideoRef{
			{TweetURL: "https://x.com/example/status/1234567890123456789"},
			{TweetURL: "https://x.com/example/status/2234567890123456789"},
			{TweetURL: "https://x.com/example/status/3234567890123456789"},
		},
	}}
	out, err := (&Activities{Twitter: fake}).RunTwitterMaintenance(context.Background(), RunTwitterMaintenanceInput{
		Query: "football goal filter:videos", MaxAgeMinutes: 1440, MinTweets: 3, MinVideos: 3,
	})
	if err != nil {
		t.Fatalf("RunTwitterMaintenance: %v", err)
	}
	if out.ResultState != twittercontract.ResultRendered || !out.Evidence.TimelineSeen ||
		out.TweetsParsed != 4 || out.VideosFound != 3 {
		t.Fatalf("output = %+v", out)
	}
}

func TestRunTwitterMaintenanceFailsBeforeSearchWhenVerifyFails(t *testing.T) {
	fake := &fakeTwitter{verifyErr: errors.New("expired")}
	_, err := (&Activities{Twitter: fake}).RunTwitterMaintenance(context.Background(), RunTwitterMaintenanceInput{})
	if err == nil || !strings.Contains(err.Error(), "verify") {
		t.Fatalf("error = %v, want verify failure", err)
	}
}

func TestRunTwitterMaintenanceRejectsMissingFeedEvidence(t *testing.T) {
	fake := &fakeTwitter{searchOut: &twitterinfra.SearchResponse{
		ResultState: twittercontract.ResultUnknownTimeout,
		StopReason:  "feed_timeout",
	}}
	_, err := (&Activities{Twitter: fake}).RunTwitterMaintenance(context.Background(), RunTwitterMaintenanceInput{MinTweets: 3})
	if err == nil || !strings.Contains(err.Error(), "unknown_timeout") {
		t.Fatalf("error = %v, want classified missing-feed failure", err)
	}
	var applicationErr *temporal.ApplicationError
	if !errors.As(err, &applicationErr) || applicationErr.Type() != CanaryErrorType ||
		!applicationErr.NonRetryable() {
		t.Fatalf("error = %T %v, want non-retrying %s", err, err, CanaryErrorType)
	}
	var out RunTwitterMaintenanceOutput
	if err := applicationErr.Details(&out); err != nil ||
		out.ResultState != twittercontract.ResultUnknownTimeout {
		t.Fatalf("canary details = %+v err=%v", out, err)
	}
}

func TestRunTwitterMaintenanceRejectsNilSearchResponse(t *testing.T) {
	_, err := (&Activities{Twitter: &fakeTwitter{}}).RunTwitterMaintenance(
		context.Background(),
		RunTwitterMaintenanceInput{},
	)
	if err == nil || !strings.Contains(err.Error(), "no response") {
		t.Fatalf("error = %v, want missing-response failure", err)
	}
}

func TestRunTwitterMaintenanceRejectsMalformedStatusURL(t *testing.T) {
	fake := &fakeTwitter{searchOut: &twitterinfra.SearchResponse{
		ResultState: twittercontract.ResultRendered,
		Count:       1, StopReason: "age", InitialArticles: 3, TweetsParsed: 3, VideoTweets: 1,
		Videos: []twitterinfra.VideoRef{{TweetURL: "https://notx.com/user/status/123"}},
	}}
	_, err := (&Activities{Twitter: fake}).RunTwitterMaintenance(context.Background(), RunTwitterMaintenanceInput{MinTweets: 3, MinVideos: 1})
	if err == nil {
		t.Fatal("expected malformed URL failure")
	}
}
