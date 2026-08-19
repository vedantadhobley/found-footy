// EventWorkflow vision-verdict routing and diagnostic-persistence tests.
package workflow_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	visionactivity "github.com/vedantadhobley/found-footy/internal/activity/vision"
	dvision "github.com/vedantadhobley/found-footy/internal/domain/vision"
	"github.com/vedantadhobley/found-footy/internal/infra/twitter"
	"github.com/vedantadhobley/found-footy/internal/workflow"
)

func TestEventWorkflow_ClockRejectPersistsAllFrameEvidence(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := baseEventEnv(&s)
	const tweetURL = "https://x.com/u/status/1111111111111111111"
	const stagingKey = "staging/rejected.mp4"

	env.OnActivity("SearchTweets", mock.Anything, mock.Anything).
		Return(discoveryactivity.SearchTweetsOutput{
			Videos: []twitter.VideoRef{{TweetURL: tweetURL, VideoPageURL: "vp", DurationSeconds: 7}},
			Count:  1, StopReason: "age",
		}, nil)
	env.OnActivity("StoreCandidate", mock.Anything, mock.Anything).
		Return(discoveryactivity.StoreCandidateOutput{Inserted: true}, nil)
	env.OnActivity("DownloadAndStage", mock.Anything, mock.Anything).
		Return(videoactivity.DownloadAndStageOutput{
			Outcome: videoactivity.OutcomePassed, MD5: "md5-rejected", StagingKey: stagingKey,
			Width: 1280, Height: 720, DurationMS: 7000, SizeBytes: 900_000,
		}, nil)
	env.OnActivity("HashVideo", mock.Anything, mock.Anything).
		Return(videoactivity.HashVideoOutput{FrameHashes: []uint64{1, 2, 4, 8, 16, 32}}, nil)

	clock, period := "05:25", dvision.PeriodFirstHalf
	frames := []dvision.FrameObservation{
		{Soccer: true, Clock: &clock, Period: &period},
		{Soccer: true, Clock: &clock, Period: &period},
		{Soccer: true, Clock: &clock, Period: &period},
	}
	readings := []dvision.ClockReading{
		{FrameIndex: 0, Minute: 5, Period: period, PeriodPinned: true, ExactMinute: true},
		{FrameIndex: 1, Minute: 5, Period: period, PeriodPinned: true, ExactMinute: true},
		{FrameIndex: 2, Minute: 5, Period: period, PeriodPinned: true, ExactMinute: true},
	}
	detected := 5
	env.OnActivity("ValidateClip", mock.Anything, mock.Anything).
		Return(visionactivity.ValidateClipOutput{
			Outcome: "rejected", Reason: "clock present but does not match expected (wrong minute or wrong half)",
			SoccerVotes: 3, Frames: frames, ClockReadings: readings,
			DetectedMinute: &detected, DetectedPeriod: "1H", ExpectedMinute: 50, ExpectedPeriod: "2H",
		}, nil)
	env.OnActivity("DeleteStaging", mock.Anything,
		videoactivity.DeleteStagingInput{StagingKey: stagingKey}).Return(nil).Once()
	env.OnActivity("UpsertCandidateOutcome", mock.Anything,
		mock.MatchedBy(func(in discoveryactivity.UpsertCandidateOutcomeInput) bool {
			if in.Outcome != discoveryactivity.OutcomeRejected {
				return false
			}
			var detail struct {
				FrameObservations []dvision.FrameObservation `json:"frame_observations"`
				ClockReadings     []dvision.ClockReading     `json:"clock_readings"`
			}
			if err := json.Unmarshal(in.Detail, &detail); err != nil {
				return false
			}
			return len(detail.FrameObservations) == 3 && len(detail.ClockReadings) == 3 &&
				detail.FrameObservations[0].Period != nil &&
				*detail.FrameObservations[0].Period == dvision.PeriodFirstHalf &&
				detail.ClockReadings[2].FrameIndex == 2
		})).Return(nil).Once()

	env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
	requireDone(t, env)
	env.AssertNotCalled(t, "PromoteAndPersist", mock.Anything, mock.Anything)
}
