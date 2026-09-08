// Vision failure decoding keeps final activity evidence bounded and replay-safe.
package workflow

import (
	"errors"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"

	visionactivity "github.com/vedantadhobley/found-footy/internal/activity/vision"
)

// visionFailureDetail prefers a Temporal-owned timeout over its previous retry's
// application-error cause. An expired attempt cannot prove which stage it reached.
func visionFailureDetail(err error) visionactivity.FailureDetail {
	var timeoutErr *temporal.TimeoutError
	if errors.As(err, &timeoutErr) {
		subtype := visionactivity.TimeoutUnknown
		switch timeoutErr.TimeoutType() {
		case enumspb.TIMEOUT_TYPE_START_TO_CLOSE:
			subtype = visionactivity.TimeoutStartToClose
		case enumspb.TIMEOUT_TYPE_SCHEDULE_TO_START:
			subtype = visionactivity.TimeoutScheduleToStart
		case enumspb.TIMEOUT_TYPE_SCHEDULE_TO_CLOSE:
			subtype = visionactivity.TimeoutScheduleToClose
		case enumspb.TIMEOUT_TYPE_HEARTBEAT:
			subtype = visionactivity.TimeoutHeartbeat
		}
		return visionactivity.FailureDetail{Stage: visionactivity.FailureActivity, Class: visionactivity.FailureTimeout, TimeoutType: subtype}
	}
	var applicationErr *temporal.ApplicationError
	if errors.As(err, &applicationErr) &&
		(applicationErr.Type() == visionactivity.FailureErrorType || applicationErr.Type() == visionactivity.PermanentLLMErrorType) &&
		applicationErr.HasDetails() {
		var detail visionactivity.FailureDetail
		if applicationErr.Details(&detail) == nil && detail.Valid() {
			return detail
		}
	}
	return visionactivity.FailureDetail{Stage: visionactivity.FailureActivity, Class: visionactivity.FailureUnknown}
}
