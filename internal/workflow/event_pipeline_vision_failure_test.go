// End-to-end workflow tests pin FF-087 durability, exact followers, and replay compatibility.
package workflow_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	sdkworkflow "go.temporal.io/sdk/workflow"

	discoveryactivity "github.com/vedantadhobley/found-footy/internal/activity/discovery"
	videoactivity "github.com/vedantadhobley/found-footy/internal/activity/video"
	visionactivity "github.com/vedantadhobley/found-footy/internal/activity/vision"
	"github.com/vedantadhobley/found-footy/internal/workflow"
)

// TestEventWorkflow_VisionFailuresPersistForExactFollowers keeps one retry unit and identical terminal evidence.
func TestEventWorkflow_VisionFailuresPersistForExactFollowers(t *testing.T) {
	requestFailure := visionactivity.FailureDetail{Stage: visionactivity.FailureRequest, Class: visionactivity.FailureTimeout}
	permanentFailure := visionactivity.FailureDetail{Stage: visionactivity.FailureRequest, Class: visionactivity.FailureModelNotFound}
	for _, tt := range []struct {
		name     string
		err      error
		want     visionactivity.FailureDetail
		attempts int
		legacy   bool
	}{
		{"request_timeout", temporal.NewApplicationError("raw URL SECRET", visionactivity.FailureErrorType, requestFailure), requestFailure, 3, false},
		{"permanent", temporal.NewNonRetryableApplicationError("raw body SECRET", visionactivity.PermanentLLMErrorType, nil, permanentFailure), permanentFailure, 1, false},
		{"start_to_close", temporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_START_TO_CLOSE, nil), visionactivity.FailureDetail{Stage: visionactivity.FailureActivity, Class: visionactivity.FailureTimeout, TimeoutType: visionactivity.TimeoutStartToClose}, 3, false},
		{"heartbeat", temporal.NewTimeoutError(enumspb.TIMEOUT_TYPE_HEARTBEAT, nil), visionactivity.FailureDetail{Stage: visionactivity.FailureActivity, Class: visionactivity.FailureTimeout, TimeoutType: visionactivity.TimeoutHeartbeat}, 3, false},
		{"pre_ff087", temporal.NewApplicationError("raw URL SECRET", visionactivity.FailureErrorType, requestFailure), visionactivity.FailureDetail{}, 3, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var s testsuite.WorkflowTestSuite
			captureLog := &workflowLogCapture{}
			s.SetLogger(captureLog)
			env, t1, t2 := twoCandidatePreHashEnv(&s)
			if tt.legacy {
				env.OnGetVersion("ff-087-vision-failure-detail", sdkworkflow.DefaultVersion, sdkworkflow.Version(1)).Return(sdkworkflow.DefaultVersion).Once()
			}
			capture := captureCandidateOutcomes(env)
			mockExactDownloads(env, t1, t2)
			env.OnActivity("DeleteStaging", mock.Anything, mock.Anything).Return(nil).Maybe()
			env.OnActivity("HashVideo", mock.Anything, mock.Anything).After(10*time.Millisecond).
				Return(videoactivity.HashVideoOutput{FrameHashes: []uint64{1, 2, 4, 8}}, nil).Once()
			env.OnActivity("ValidateClip", mock.Anything, mock.Anything).Return(visionactivity.ValidateClipOutput{}, tt.err)
			env.ExecuteWorkflow(workflow.EventWorkflow, stdDiscoveryInput())
			requireDone(t, env)
			rows := capture.snapshot()
			requireOutcomeCounts(t, rows, map[discoveryactivity.CandidateOutcome]int{discoveryactivity.OutcomeFailed: 2, discoveryactivity.OutcomeDuplicate: 0})
			for url, row := range rows {
				if row.reason != "vision_error" {
					t.Errorf("%s: reason=%s", url, row.reason)
				}
				if tt.legacy {
					if string(row.detail) != "null" {
						t.Errorf("legacy payload changed: %s", row.detail)
					}
					continue
				}
				want, _ := json.Marshal(map[string]any{"failure": tt.want})
				if string(row.detail) != string(want) {
					t.Errorf("%s detail=%s, want %s", url, row.detail, want)
				}
			}
			env.AssertNumberOfCalls(t, "ValidateClip", tt.attempts)
			env.AssertNumberOfCalls(t, "HashVideo", 1)
			env.AssertNumberOfCalls(t, "DeleteStaging", 2)
			env.AssertNotCalled(t, "PromoteAndPersist", mock.Anything, mock.Anything)
			if !tt.legacy {
				found := false
				captureLog.mu.Lock()
				for _, fields := range captureLog.entries {
					m := make(map[string]interface{})
					for i := 0; i+1 < len(fields); i += 2 {
						if k, ok := fields[i].(string); ok {
							m[k] = fields[i+1]
						}
					}
					if m["phase"] == "vision" && m["outcome"] == "failed" {
						found = true
						if m["failure_stage"] != string(tt.want.Stage) || m["failure_class"] != string(tt.want.Class) {
							t.Errorf("failure log disagrees with durable detail: %v", m)
						}
						if tt.want.TimeoutType != "" && m["failure_timeout_type"] != string(tt.want.TimeoutType) {
							t.Errorf("timeout subtype missing: %v", m)
						}
					}
				}
				captureLog.mu.Unlock()
				if !found {
					t.Error("missing correlated vision failure log")
				}
			}
		})
	}
}
