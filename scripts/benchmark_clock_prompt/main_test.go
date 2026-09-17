// Prompt-variant tests ensure the experiment changes context permission, not timing/content policy.
package main

import (
	"strings"
	"testing"

	vision "github.com/vedantadhobley/found-footy/internal/domain/vision"
)

// TestPromptVariants keeps the default intact and the literal control independently constrained.
func TestPromptVariants(t *testing.T) {
	variants, err := prompts()
	if err != nil || len(variants) != 2 || variants[1] != vision.DefaultPrompt {
		t.Fatalf("variants=%v err=%v", variants, err)
	}
	if strings.Contains(variants[0], "You may compare") {
		t.Fatal("literal control permits cross-frame identification")
	}
	for _, prompt := range variants {
		for _, required := range []string{"Never invent seconds", "Do NOT infer the period", "Never copy missing digits", "soccer:", "screen:", "Return exactly 3 frame objects"} {
			if !strings.Contains(prompt, required) {
				t.Fatalf("missing shared constraint %q", required)
			}
		}
	}
}

// TestPeriodComparisonChangesOnlyPeriodInstruction protects added-time transcription and runtime defaults.
func TestPeriodComparisonChangesOnlyPeriodInstruction(t *testing.T) {
	names, variants, err := comparisonPrompts("period-labels")
	if err != nil || len(names) != 2 || variants[0] != vision.DefaultPrompt {
		t.Fatalf("period comparison: names=%v err=%v", names, err)
	}
	baseline, proposed := strings.Split(variants[0], "\n"), strings.Split(variants[1], "\n")
	if len(baseline) != len(proposed) {
		t.Fatal("unexpected added/removed prompt lines")
	}
	changes := 0
	for i := range baseline {
		if baseline[i] == proposed[i] {
			continue
		}
		changes++
		if !strings.HasPrefix(baseline[i], "- period:") || !strings.HasPrefix(proposed[i], "- period:") {
			t.Fatalf("changed non-period instruction %d", i)
		}
	}
	if changes != 1 {
		t.Fatalf("changed %d lines, want 1", changes)
	}
	for _, text := range []string{"1re", "2e", "superscript", "bare", "ADDED TIME", "Do NOT infer", "null"} {
		if !strings.Contains(variants[1], text) {
			t.Fatalf("missing role guard %q", text)
		}
	}
	if _, _, err := comparisonPrompts("unreviewed"); err == nil {
		t.Fatal("unknown comparison accepted")
	}
}

// TestTimingDifferencesKeepsFieldRoles checks null/period/allowance separation without content-label assumptions.
func TestTimingDifferencesKeepsFieldRoles(t *testing.T) {
	clock, allowance := "44:51", "+2"
	period := vision.PeriodSecondHalf
	gold := []vision.FrameObservation{{Clock: &clock, Added: &allowance}}
	got := []vision.FrameObservation{{Soccer: true, Clock: &clock, Added: &allowance}}
	if differences := timingDifferences(gold, got); len(differences) != 0 {
		t.Fatal(differences)
	}
	got[0].Period = &period
	if differences := timingDifferences(gold, got); len(differences) != 1 || !strings.Contains(differences[0], "period") {
		t.Fatal(differences)
	}
	if differences := timingDifferences(nil, got); len(differences) != 0 {
		t.Fatal(differences)
	}
	if differences := timingDifferences(gold, nil); len(differences) != 1 {
		t.Fatal(differences)
	}
}

// TestRecordedBarcelonaAdmissions distinguishes the fixed offset fallback from unresolved match identity.
func TestRecordedBarcelonaAdmissions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		clocks   []string
		expected vision.Expected
		want     vision.Outcome
	}{
		{"jesus_wrong_fixture", []string{"77:27", "77:29", "77:31"}, vision.Expected{Elapsed: 79}, vision.OutcomeVerified},
		{"yamal_wrong_half", []string{"44:48", "44:51", "44:53"}, vision.Expected{Elapsed: 89}, vision.OutcomeRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var frames []vision.FrameObservation
			for _, clock := range tc.clocks {
				frames = append(frames, vision.FrameObservation{Soccer: true, Clock: &clock})
			}
			got := vision.Evaluate(frames, tc.expected, 1)
			if got.Outcome != tc.want {
				t.Fatalf("policy changed: %s %s", got.Outcome, got.Reason)
			}
		})
	}
}
