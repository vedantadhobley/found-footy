// Evidence tests keep capture bounded and distinguish missing history from an accepted verdict.
package vision

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// testEvidence builds an actual evaluator result instead of inventing a clock snapshot.
func testEvidence() *Evidence {
	clock := "22:10"
	frames := []FrameObservation{{Soccer: true, Clock: &clock}, {Soccer: true}, {Soccer: true}}
	expected := Expected{Elapsed: 23}
	return &Evidence{ID: uuid.New(), EventID: uuid.New(), FixtureID: 1, MD5: strings.Repeat("ab", 16),
		Version: 1, EvaluatedAt: time.Now().UTC(), Evaluator: EvaluatorVersion,
		PromptSHA256: strings.Repeat("a", 64), SchemaSHA256: strings.Repeat("b", 64),
		Expected: expected, ToleranceMinutes: 1, FramePositions: []float64{2, 4, 6}, FrameQuality: 3,
		Frames: frames, Evaluation: Evaluate(frames, expected, 1)}
}

// TestEvidenceValidation guards required evidence while allowing an honestly unknown model ID.
func TestEvidenceValidation(t *testing.T) {
	if err := testEvidence().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Evidence){
		"identity": func(e *Evidence) { e.ID = uuid.Nil },
		"scope":    func(e *Evidence) { e.EventID = uuid.Nil },
		"bytes":    func(e *Evidence) { e.MD5 = "bad" },
		"version":  func(e *Evidence) { e.Version = 9 },
		"clock":    func(e *Evidence) { e.Evaluation.MatchedMinute = nil },
		"reject":   func(e *Evidence) { e.Evaluation.Outcome = OutcomeRejected },
		"samples":  func(e *Evidence) { e.FramePositions[1] = -1 },
		"frames":   func(e *Evidence) { e.Frames = nil },
		"digest":   func(e *Evidence) { e.PromptSHA256 = "unknown" },
		"oversize": func(e *Evidence) { e.Model = strings.Repeat("x", MaxEvidenceBytes) },
	} {
		t.Run(name, func(t *testing.T) {
			e := testEvidence()
			mutate(e)
			if err := e.Validate(); err == nil {
				t.Fatal("invalid evidence accepted")
			}
		})
	}
	var missing *Evidence
	if err := missing.Validate(); err == nil {
		t.Fatal("missing evidence accepted")
	}
}
