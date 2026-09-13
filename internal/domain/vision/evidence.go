// Accepted evaluation evidence outlives Temporal history without retaining images or prompts.
package vision

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

// EvaluatorVersion identifies the clock/content interpretation applied to raw
// observations. Bump it when Evaluate or its clock interpretation changes.
const EvaluatorVersion = "vision-evaluate-v1"

// MaxEvidenceBytes bounds one stored evaluation independently of model output.
const MaxEvidenceBytes = 16 * 1024

// EvaluationOrigin identifies the successful activity attempt, not every model
// call that may have been lost before Temporal acknowledged an activity result.
type EvaluationOrigin struct {
	WorkflowID string `json:"workflow_id"`
	RunID      string `json:"run_id"`
	ActivityID string `json:"activity_id"`
	Attempt    int32  `json:"attempt"`
}

// Evidence is the immutable result of one acknowledged accepted evaluation.
// Model may be empty when the server omitted its identity; never invent one.
// Its ID is an evaluation identity, not the asset identity or a selection key.
type Evidence struct {
	ID               uuid.UUID          `json:"id"`
	EventID          uuid.UUID          `json:"event_id"`
	FixtureID        int64              `json:"fixture_id"`
	MD5              string             `json:"md5"`
	Version          int                `json:"version"`
	EvaluatedAt      time.Time          `json:"evaluated_at"`
	Evaluator        string             `json:"evaluator"`
	Model            string             `json:"model"`
	PromptSHA256     string             `json:"prompt_sha256"`
	SchemaSHA256     string             `json:"schema_sha256"`
	Origin           EvaluationOrigin   `json:"origin"`
	Expected         Expected           `json:"expected"`
	ToleranceMinutes int                `json:"tolerance_minutes"`
	FramePositions   []float64          `json:"frame_positions_seconds"`
	FrameQuality     int                `json:"frame_quality"`
	Frames           []FrameObservation `json:"frames"`
	Evaluation       Evaluation         `json:"evaluation"`
}

// Validate rejects incomplete or oversized acceptance evidence. It does not
// re-evaluate observations with today's policy or manufacture historical data.
func (e *Evidence) Validate() error {
	if e == nil || e.ID == uuid.Nil || e.Version != 1 || e.EvaluatedAt.IsZero() || e.Evaluator == "" {
		return fmt.Errorf("missing validation identity, version or evaluation time")
	}
	md5, err := hex.DecodeString(e.MD5)
	if e.EventID == uuid.Nil || e.FixtureID <= 0 || err != nil || len(md5) != 16 {
		return fmt.Errorf("invalid validation event or exact content identity")
	}
	if len(e.Frames) == 0 || len(e.Frames) > 3 || len(e.FramePositions) != 3 ||
		e.Evaluation.FrameCount != len(e.Frames) || e.ToleranceMinutes < 0 || e.Expected.Extra < 0 {
		return fmt.Errorf("invalid validation sampling or clock context")
	}
	for i, position := range e.FramePositions {
		if math.IsNaN(position) || math.IsInf(position, 0) || position < 0 || i > 0 && position <= e.FramePositions[i-1] {
			return fmt.Errorf("invalid validation sample positions")
		}
	}
	if e.Evaluation.Outcome != OutcomeVerified && e.Evaluation.Outcome != OutcomeUnverified ||
		(e.Evaluation.Outcome == OutcomeVerified) != (e.Evaluation.MatchedMinute != nil) {
		return fmt.Errorf("validation evidence must describe its own accepted verdict")
	}
	for _, digest := range []string{e.PromptSHA256, e.SchemaSHA256} {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != 32 {
			return fmt.Errorf("invalid validation contract digest")
		}
	}
	data, err := json.Marshal(e)
	if err != nil || len(data) > MaxEvidenceBytes {
		return fmt.Errorf("validation evidence is not bounded JSON")
	}
	return nil
}
