// Capture validation provenance at the actual model/evaluator boundary, not at promotion.
package vision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"

	dvision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// acceptanceEvidence records the actual model, sampling and evaluator inputs.
// The optional capture flag keeps old activity outputs/payloads unchanged.
func (a *Activities) acceptanceEvidence(ctx context.Context, in ValidateClipInput, duration float64,
	model string, frames []dvision.FrameObservation, evaluation dvision.Evaluation,
) *dvision.Evidence {
	if !in.CaptureEvidence || evaluation.Outcome == dvision.OutcomeRejected {
		return nil
	}
	positions := make([]float64, len(framePositions))
	for i, fraction := range framePositions {
		positions[i] = fraction * duration
	}
	var origin dvision.EvaluationOrigin
	if activity.IsActivity(ctx) {
		info := activity.GetInfo(ctx)
		origin = dvision.EvaluationOrigin{WorkflowID: info.WorkflowExecution.ID,
			RunID: info.WorkflowExecution.RunID, ActivityID: info.ActivityID, Attempt: info.Attempt}
	}
	return &dvision.Evidence{
		ID: uuid.New(), Version: 1, EvaluatedAt: time.Now().UTC(), Evaluator: dvision.EvaluatorVersion,
		EventID: in.EventID, FixtureID: in.FixtureID, MD5: in.MD5,
		Model: model, PromptSHA256: contractDigest(a.prompt()), SchemaSHA256: contractDigest(string(dvision.ResponseSchema)),
		Origin: origin, Expected: dvision.Expected{Elapsed: in.APIElapsed, Extra: in.APIExtra},
		ToleranceMinutes: a.Cfg.ToleranceMinutes, FramePositions: positions, FrameQuality: a.Cfg.FrameQuality,
		Frames: frames, Evaluation: evaluation,
	}
}

// contractDigest identifies the effective prompt/schema without duplicating them or images.
func contractDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// prompt resolves the effective prompt once for both the request and its provenance.
func (a *Activities) prompt() string {
	if a.Cfg.Prompt != "" {
		return a.Cfg.Prompt
	}
	return dvision.DefaultPrompt
}
