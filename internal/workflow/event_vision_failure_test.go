// Workflow-local failure decoding tests protect final timeout precedence and old payloads.
package workflow

import (
	"errors"
	"fmt"
	"testing"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"

	visionactivity "github.com/vedantadhobley/found-footy/internal/activity/vision"
)

// TestVisionFailureDetailTimeoutPrecedence never reports a previous retry's stage as the final one.
func TestVisionFailureDetailTimeoutPrecedence(t *testing.T) {
	previous := temporal.NewApplicationError("previous model error", visionactivity.FailureErrorType,
		visionactivity.FailureDetail{Stage: visionactivity.FailureRequest, Class: visionactivity.FailureUnavailable})
	for _, tt := range []struct {
		sdk  enumspb.TimeoutType
		want visionactivity.FailureTimeoutType
	}{
		{enumspb.TIMEOUT_TYPE_START_TO_CLOSE, visionactivity.TimeoutStartToClose},
		{enumspb.TIMEOUT_TYPE_SCHEDULE_TO_START, visionactivity.TimeoutScheduleToStart},
		{enumspb.TIMEOUT_TYPE_SCHEDULE_TO_CLOSE, visionactivity.TimeoutScheduleToClose},
		{enumspb.TIMEOUT_TYPE_HEARTBEAT, visionactivity.TimeoutHeartbeat},
		{enumspb.TIMEOUT_TYPE_UNSPECIFIED, visionactivity.TimeoutUnknown},
	} {
		got := visionFailureDetail(fmt.Errorf("activity wrapper: %w", temporal.NewTimeoutError(tt.sdk, previous)))
		want := visionactivity.FailureDetail{Stage: visionactivity.FailureActivity, Class: visionactivity.FailureTimeout, TimeoutType: tt.want}
		if got != want {
			t.Errorf("%s: got %+v, want %+v", tt.sdk, got, want)
		}
	}
}

// TestVisionFailureDetailFallback does not parse old error text or trust another activity's details.
func TestVisionFailureDetailFallback(t *testing.T) {
	for _, err := range []error{
		errors.New("SECRET context deadline exceeded model body"),
		temporal.NewApplicationError("old permanent", visionactivity.PermanentLLMErrorType),
		temporal.NewApplicationError("malformed", visionactivity.FailureErrorType, "SECRET"),
		temporal.NewApplicationError("unbounded", visionactivity.FailureErrorType, visionactivity.FailureDetail{Stage: visionactivity.FailureRequest, Class: "SECRET"}),
		temporal.NewApplicationError("wrong type", "other_activity", visionactivity.FailureDetail{Stage: visionactivity.FailureRequest, Class: visionactivity.FailureTimeout}),
	} {
		fc := temporal.GetDefaultFailureConverter()
		err = fc.FailureToError(fc.ErrorToFailure(err))
		got := visionFailureDetail(err)
		if got != (visionactivity.FailureDetail{Stage: visionactivity.FailureActivity, Class: visionactivity.FailureUnknown}) {
			t.Errorf("unexpected fallback: %+v", got)
		}
	}
}
