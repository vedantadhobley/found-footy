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

// TestOldAcceptedEvidenceIsNotReinterpreted keeps historical v1 acceptance immutable.
func TestOldAcceptedEvidenceIsNotReinterpreted(t *testing.T) {
	e := testEvidence()
	e.Evaluator = "vision-evaluate-v1"
	e.Expected = Expected{Elapsed: 90, Extra: 4}
	e.Frames = rep(frame(true, false, "90:37", "+4", ""), 3)
	minute := 90
	e.Evaluation = Evaluation{Outcome: OutcomeVerified, MatchedMinute: &minute, FrameCount: 3,
		Reason: "frozen-boundary stoppage, period match (minute unpinned)"}
	if current := Evaluate(e.Frames, e.Expected, 1); current.Outcome != OutcomeRejected {
		t.Fatalf("control=%+v", current)
	}
	if err := e.Validate(); err != nil {
		t.Fatalf("historical proof reinterpreted: %v", err)
	}
}

// TestV3StoppageAcceptanceRemainsHistorical keeps old unverified proof unchanged.
func TestV3StoppageAcceptanceRemainsHistorical(t *testing.T) {
	e := testEvidence()
	e.Evaluator = "vision-evaluate-v3"
	e.Expected = Expected{Elapsed: 90, Extra: 6}
	e.Frames = rep(frame(true, false, "95:15", "+5", ""), 3)
	e.Evaluation = Evaluation{Outcome: OutcomeUnverified, FrameCount: 3,
		Reason: "clock interpretation ambiguous — kept, not clock-verified"}
	if current := Evaluate(e.Frames, e.Expected, 1); current.Outcome != OutcomeVerified {
		t.Fatalf("control=%+v", current)
	}
	if err := e.Validate(); err != nil || e.Evaluation.Outcome != OutcomeUnverified || e.Evaluator != "vision-evaluate-v3" {
		t.Fatalf("historical proof changed: %+v, %v", e, err)
	}
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

// TestV2RelativeAcceptanceRemainsHistorical prevents new policy from rewriting
// an older accepted proof when an exact-byte follower loads it.
func TestV2RelativeAcceptanceRemainsHistorical(t *testing.T) {
	e := testEvidence()
	e.Evaluator = "vision-evaluate-v2"
	e.Expected = Expected{Elapsed: 89}
	e.Frames = rep(frame(true, false, "44:51", "", ""), 3)
	e.Evaluation = Evaluation{Outcome: OutcomeUnverified, FrameCount: 3,
		Reason: "clock interpretation ambiguous — kept, not clock-verified"}
	if current := Evaluate(e.Frames, e.Expected, 1); current.Outcome != OutcomeRejected {
		t.Fatalf("control=%+v", current)
	}
	if err := e.Validate(); err != nil {
		t.Fatalf("historical proof reinterpreted: %v", err)
	}
}
