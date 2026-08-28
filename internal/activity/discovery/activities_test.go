// Search activity tests for classified browser results and transport errors.
package discovery

import (
	"context"
	"errors"
	"testing"

	"go.temporal.io/sdk/temporal"

	twittercontract "github.com/vedantadhobley/found-footy/internal/contract/twittersearch"
	"github.com/vedantadhobley/found-footy/internal/infra/twitter"
)

type fakeSearchClient struct {
	out *twitter.SearchResponse
	err error
}

func (f fakeSearchClient) Search(
	context.Context, string, twitter.SearchRequest,
) (*twitter.SearchResponse, error) {
	return f.out, f.err
}

func TestSearchTweetsReturnsClassifiedServiceErrorAsRetryableDetails(t *testing.T) {
	activities := &Activities{Twitter: fakeSearchClient{err: &twitter.SearchError{
		StatusCode: 503, ErrorClass: "auth_expired",
		ResultState: twittercontract.ResultLogin,
		Evidence: twittercontract.SearchEvidence{
			FinalURL: "https://x.com/i/flow/login",
		},
	}}}
	_, err := activities.SearchTweets(context.Background(), SearchTweetsInput{Query: "goal"})
	var applicationErr *temporal.ApplicationError
	if !errors.As(err, &applicationErr) {
		t.Fatalf("SearchTweets error = %T %v, want ApplicationError", err, err)
	}
	if applicationErr.Type() != SearchUnavailableErrorType || applicationErr.NonRetryable() {
		t.Fatalf("application error = type %q nonretryable=%t",
			applicationErr.Type(), applicationErr.NonRetryable())
	}
	var out SearchTweetsOutput
	if err := applicationErr.Details(&out); err != nil {
		t.Fatalf("decode details: %v", err)
	}
	if out.ResultState != twittercontract.ResultLogin ||
		out.Evidence.FinalURL != "https://x.com/i/flow/login" {
		t.Fatalf("output = %+v", out)
	}
}

func TestSearchTweetsReturnsTransportError(t *testing.T) {
	activities := &Activities{Twitter: fakeSearchClient{err: errors.New("connection refused")}}
	if _, err := activities.SearchTweets(
		context.Background(), SearchTweetsInput{Query: "goal"},
	); err == nil {
		t.Fatal("SearchTweets transport error = nil")
	}
}

func TestGetDiscoveryConfigDefaultsUnavailableBudgetToSearchBudget(t *testing.T) {
	out, err := (&Activities{MaxAttempts: 7}).GetDiscoveryConfig(
		context.Background(), GetDiscoveryConfigInput{},
	)
	if err != nil {
		t.Fatalf("GetDiscoveryConfig: %v", err)
	}
	if out.MaxUnavailableAttempts != 7 {
		t.Fatalf("MaxUnavailableAttempts = %d, want 7", out.MaxUnavailableAttempts)
	}
	if out.MaxHamming != 12 || out.MinRunFrames != 30 || out.MaxGapFrames != 3 {
		t.Fatalf("primary dedup = %d/%d/%d, want 12/30/3",
			out.MaxHamming, out.MinRunFrames, out.MaxGapFrames)
	}
	if out.LongMaxHamming != 16 || out.LongMinRunFrames != 50 || out.LongMaxGapFrames != 5 {
		t.Fatalf("long dedup = %d/%d/%d, want 16/50/5",
			out.LongMaxHamming, out.LongMinRunFrames, out.LongMaxGapFrames)
	}
}
