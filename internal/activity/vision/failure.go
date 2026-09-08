// Bounded vision failure evidence survives Temporal retries without persisting raw errors.
package vision

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"

	"go.temporal.io/sdk/temporal"

	"github.com/vedantadhobley/found-footy/internal/infra/ffmpeg"
	"github.com/vedantadhobley/found-footy/internal/infra/llm"
)

// FailureErrorType identifies retryable vision failures carrying FailureDetail.
const FailureErrorType = "vision_failure"

// PermanentLLMErrorType retains the existing non-retryable model error identity.
const PermanentLLMErrorType = "vision_llm_permanent"

// FailureStage identifies the failed operation, not the presumed upstream cause.
type FailureStage string

const (
	FailureScratch   FailureStage = "scratch"
	FailureFetch     FailureStage = "staging_fetch"
	FailureProbe     FailureStage = "probe"
	FailureExtract   FailureStage = "frame_extract"
	FailureAdmission FailureStage = "model_admission"
	FailureRequest   FailureStage = "model_request"
	FailureParse     FailureStage = "response_parse"
	FailureActivity  FailureStage = "activity"
)

// FailureClass is a bounded diagnostic taxonomy; it does not set retry policy.
type FailureClass string

const (
	FailureTimeout        FailureClass = "timeout"
	FailureCanceled       FailureClass = "canceled"
	FailureFilesystem     FailureClass = "filesystem"
	FailureStorage        FailureClass = "storage"
	FailureBinaryMissing  FailureClass = "binary_missing"
	FailureInputMissing   FailureClass = "input_missing"
	FailureInputCorrupted FailureClass = "input_corrupted"
	FailureConcurrency    FailureClass = "concurrency"
	FailureProbeFailed    FailureClass = "probe_failed"
	FailureExtractFailed  FailureClass = "extract_failed"
	FailureRateLimited    FailureClass = "rate_limited"
	FailureCapacity       FailureClass = "capacity"
	FailureUnavailable    FailureClass = "unavailable"
	FailureTransport      FailureClass = "transport"
	FailureInvalidJSON    FailureClass = "invalid_json"
	FailureModelNotFound  FailureClass = "model_not_found"
	FailureInvalidRequest FailureClass = "invalid_request"
	FailureAuth           FailureClass = "auth"
	FailureUnknown        FailureClass = "unknown"
)

// FailureTimeoutType describes only a Temporal-owned timeout. A local request
// deadline has class=timeout but no inferred Temporal subtype.
type FailureTimeoutType string

const (
	TimeoutStartToClose    FailureTimeoutType = "start_to_close"
	TimeoutScheduleToStart FailureTimeoutType = "schedule_to_start"
	TimeoutScheduleToClose FailureTimeoutType = "schedule_to_close"
	TimeoutHeartbeat       FailureTimeoutType = "heartbeat"
	TimeoutUnknown         FailureTimeoutType = "unknown"
)

// FailureDetail is the complete allowlisted payload stored under outcome_detail.failure.
type FailureDetail struct {
	Stage       FailureStage       `json:"stage"`
	Class       FailureClass       `json:"class"`
	TimeoutType FailureTimeoutType `json:"timeout_type,omitempty"`
}

// Valid rejects unregistered strings, including errors accidentally put in detail fields.
func (d FailureDetail) Valid() bool {
	switch d.Stage {
	case FailureScratch, FailureFetch, FailureProbe, FailureExtract, FailureAdmission, FailureRequest, FailureParse, FailureActivity:
	default:
		return false
	}
	switch d.Class {
	case FailureTimeout, FailureCanceled, FailureFilesystem, FailureStorage,
		FailureBinaryMissing, FailureInputMissing, FailureInputCorrupted, FailureConcurrency,
		FailureProbeFailed, FailureExtractFailed, FailureRateLimited, FailureCapacity,
		FailureUnavailable, FailureTransport, FailureInvalidJSON, FailureModelNotFound,
		FailureInvalidRequest, FailureAuth, FailureUnknown:
	default:
		return false
	}
	if d.TimeoutType == "" {
		return true
	}
	if d.Stage != FailureActivity || d.Class != FailureTimeout {
		return false
	}
	switch d.TimeoutType {
	case TimeoutStartToClose, TimeoutScheduleToStart, TimeoutScheduleToClose, TimeoutHeartbeat, TimeoutUnknown:
		return true
	default:
		return false
	}
}

// visionFailure attaches safe details while preserving the pre-existing retry rules.
func visionFailure(stage FailureStage, err error) error {
	detail := FailureDetail{Stage: stage, Class: classifyFailure(stage, err)}
	permanent := (stage == FailureRequest || stage == FailureParse) &&
		(errors.Is(err, llm.ErrInvalidJSON) || errors.Is(err, llm.ErrModelNotFound) ||
			errors.Is(err, llm.ErrInvalidRequest) || errors.Is(err, llm.ErrAuthFailed))
	errorType := FailureErrorType
	if permanent {
		errorType = PermanentLLMErrorType
	}
	return temporal.NewApplicationErrorWithOptions("vision stage failed", errorType,
		temporal.ApplicationErrorOptions{
			NonRetryable: permanent, Cause: fmt.Errorf("vision.ValidateClip: %s: %w", stage, err),
			Details: []any{detail},
		})
}

// classifyFailure uses typed causes, never model bodies or message substring parsing.
func classifyFailure(stage FailureStage, err error) FailureClass {
	var networkError net.Error
	var pathError *os.PathError
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, ffmpeg.ErrExtractionTimeout):
		return FailureTimeout
	case errors.Is(err, context.Canceled):
		return FailureCanceled
	case errors.Is(err, llm.ErrRateLimited):
		return FailureRateLimited
	case errors.Is(err, llm.ErrCapExceeded):
		return FailureCapacity
	case errors.Is(err, llm.ErrInvalidJSON):
		return FailureInvalidJSON
	case errors.Is(err, llm.ErrModelNotFound):
		return FailureModelNotFound
	case errors.Is(err, llm.ErrInvalidRequest):
		return FailureInvalidRequest
	case errors.Is(err, llm.ErrAuthFailed):
		return FailureAuth
	case errors.Is(err, ffmpeg.ErrBinaryNotFound):
		return FailureBinaryMissing
	case errors.Is(err, ffmpeg.ErrInputNotFound):
		return FailureInputMissing
	case errors.Is(err, ffmpeg.ErrInputCorrupted):
		return FailureInputCorrupted
	case errors.Is(err, ffmpeg.ErrConcurrencyExhausted):
		return FailureConcurrency
	case errors.Is(err, ffmpeg.ErrOutputWriteFailed), errors.As(err, &pathError):
		return FailureFilesystem
	case errors.Is(err, ffmpeg.ErrProbeFailed):
		return FailureProbeFailed
	case errors.Is(err, ffmpeg.ErrExtractionFailed):
		return FailureExtractFailed
	case errors.As(err, &networkError):
		if networkError.Timeout() {
			return FailureTimeout
		}
		return FailureTransport
	case errors.Is(err, llm.ErrUnavailable):
		return FailureUnavailable
	}
	switch stage {
	case FailureScratch:
		return FailureFilesystem
	case FailureFetch:
		return FailureStorage
	default:
		return FailureUnknown
	}
}
