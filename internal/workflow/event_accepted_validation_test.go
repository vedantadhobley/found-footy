// Pipeline tests pin evidence transfer, exact followers and legacy payload compatibility.
package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	sdkworkflow "go.temporal.io/sdk/workflow"

	livefeedactivity "github.com/vedantadhobley/found-footy/internal/activity/livefeed"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	visionactivity "github.com/vedantadhobley/found-footy/internal/activity/vision"
	discoverycontract "github.com/vedantadhobley/found-footy/internal/contract/discovery"
	ddiscovery "github.com/vedantadhobley/found-footy/internal/domain/discovery"
	dvision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// TestAcceptedValidationPipeline carries one proof through retry and exact
// recurrence without rerunning vision or expanding old activity payloads.
func TestAcceptedValidationPipeline(t *testing.T) {
	for _, tc := range []struct {
		name             string
		capture, missing bool
	}{
		{"capture", true, false}, {"legacy", false, false}, {"missing", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			env.RegisterActivity(&visionactivity.Activities{})
			env.RegisterActivity(&videoactivity.PersistActivities{})
			env.RegisterActivity(&livefeedactivity.Activities{})
			eventID := uuid.New()
			md5 := strings.Repeat("ab", 16)
			clock := "22:10"
			frames := []dvision.FrameObservation{{Soccer: true, Clock: &clock}, {Soccer: true}, {Soccer: true}}
			evaluation := dvision.Evaluate(frames, dvision.Expected{Elapsed: 23}, 1)
			proof := &dvision.Evidence{ID: uuid.New(), EventID: eventID, FixtureID: 1, MD5: md5,
				Version: 1, EvaluatedAt: time.Now().UTC(), Evaluator: dvision.EvaluatorVersion,
				PromptSHA256: strings.Repeat("a", 64), SchemaSHA256: strings.Repeat("b", 64),
				Expected: dvision.Expected{Elapsed: 23}, ToleranceMinutes: 1, FramePositions: []float64{2, 4, 6},
				Frames: frames, Evaluation: evaluation}
			env.OnActivity("ValidateClip", mock.Anything, mock.Anything).Return(
				func(_ context.Context, in visionactivity.ValidateClipInput) (visionactivity.ValidateClipOutput, error) {
					require.Equal(t, tc.capture, in.CaptureEvidence)
					if tc.capture {
						require.Equal(t, md5, in.MD5)
					} else {
						require.Empty(t, in.MD5)
					}
					out := visionactivity.ValidateClipOutput{Outcome: "verified", MatchedMinute: evaluation.MatchedMinute}
					if tc.capture && !tc.missing {
						out.Evidence = proof
					}
					return out, nil
				}).Once()
			var calls []videoactivity.CommitClipPlacementInput
			if !tc.missing {
				env.OnActivity("CommitClipPlacement", mock.Anything, mock.Anything).Return(
					func(_ context.Context, in videoactivity.CommitClipPlacementInput) (videoactivity.CommitClipPlacementOutput, error) {
						calls = append(calls, in)
						if len(calls) == 1 {
							return videoactivity.CommitClipPlacementOutput{}, errors.New("retry placement")
						}
						return videoactivity.CommitClipPlacementOutput{WinnerAssetID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(eventID.String()+":"+md5)), Announce: true}, nil
					})
				env.OnActivity("PublishEventUpdate", mock.Anything, mock.Anything).Return(nil)
			}
			env.ExecuteWorkflow(func(ctx sdkworkflow.Context) error {
				p := newPipeline(ctx, EventWorkflowInput{EventID: eventID, FixtureID: 1, Minute: 23},
					pipelineConfig{atomicPlacement: true, variantEvidence: true, durableValidation: tc.capture, eventUpdateContract: true}, sdkworkflow.GetLogger(ctx))
				for _, url := range []string{"first", "follower", "recurrence"} {
					p.candidates[url] = candidateOwnership{state: ddiscovery.CandidateInFlight,
						evidence: discoverycontract.CandidateEvidence{EventID: eventID, FixtureID: 1, SearchAttempt: 1, Query: "goal", TweetURL: url}}
				}
				c := clip{tweetURL: "first", md5: md5, stagingKey: "staging/first", popularity: 2, exactFollowers: []string{"follower"}}
				p.pending = append(p.pending, c)
				p.fireVision(c)
				for p.inFlight > 0 {
					p.selector.Select(ctx)
				}
				if p.terminalErr != nil {
					return p.terminalErr
				}
				// A recurrence invokes placement without an evaluation. It must not
				// attach the earlier model result as if a new call occurred.
				c.tweetURL, c.popularity, c.exactFollowers = "recurrence", 1, nil
				_, _ = p.commitClipPlacement(c, visionactivity.ValidateClipOutput{}, false, p.assets[0].assetID, nil)
				return p.terminalErr
			})
			if tc.missing {
				require.ErrorContains(t, env.GetWorkflowError(), "lacks durable validation")
				require.Empty(t, calls)
				return
			}
			require.NoError(t, env.GetWorkflowError())
			require.Len(t, calls, 3)
			require.Equal(t, calls[0], calls[1], "retry must preserve evaluation identity")
			require.Len(t, calls[1].Candidates, 2)
			if tc.capture {
				require.Equal(t, proof, calls[1].Validation)
			} else {
				require.Nil(t, calls[1].Validation)
			}
			require.Nil(t, calls[2].Validation)
			env.AssertNumberOfCalls(t, "ValidateClip", 1)
			env.AssertNumberOfCalls(t, "PublishEventUpdate", 2)
		})
	}
}
