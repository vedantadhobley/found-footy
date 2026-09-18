// Experimental request tests preserve runtime schema/content and explicit inference controls.
package main

import (
	"reflect"
	"strings"
	"testing"

	vision "github.com/vedantadhobley/found-footy/internal/domain/vision"
	"github.com/vedantadhobley/found-footy/internal/infra/llm"
)

// TestRequestControls preserves old defaults and rejects unsupported experiment inputs.
func TestRequestControls(t *testing.T) {
	for _, reasoning := range []string{"none", "high"} {
		for _, tokens := range []int{0, 2048} {
			effort, budget, err := requestControls(reasoning, tokens)
			if err != nil || string(effort) != reasoning || (tokens == 0) != (budget == nil) {
				t.Fatalf("unexpected controls: effort=%q budget=%v err=%v", effort, budget, err)
			}
			if tokens > 0 && *budget != tokens {
				t.Fatalf("budget=%d want=%d", *budget, tokens)
			}
		}
	}
	for _, reasoning := range []string{"", "HIGH", "low", "medium", "auto"} {
		if _, _, err := requestControls(reasoning, 0); err == nil {
			t.Fatalf("accepted unsupported reasoning %q", reasoning)
		}
	}
	if _, _, err := requestControls("none", -1); err == nil {
		t.Fatal("accepted negative output budget")
	}
}

// TestBenchmarkRequest preserves production image/schema semantics for each effort.
func TestBenchmarkRequest(t *testing.T) {
	images := []llm.ChatImage{{Data: []byte("test-image"), MimeType: "image/png"}}
	for _, effort := range []llm.ReasoningEffort{llm.ReasoningEffortNone, llm.ReasoningEffortHigh} {
		budget := 2048
		r := benchmarkRequest(vision.DefaultPrompt, images, effort, &budget)
		if r.ReasoningEffort != effort || r.MaxTokens == nil || *r.MaxTokens != budget {
			t.Fatalf("request controls lost: %+v", r)
		}
		if len(r.Messages) != 1 || r.Messages[0].Role != llm.RoleUser ||
			r.Messages[0].Content != vision.DefaultPrompt || !reflect.DeepEqual(r.Messages[0].Images, images) {
			t.Fatal("request content or image ordering changed")
		}
		if r.ResponseFormat == nil || r.ResponseFormat.JSONSchema == nil ||
			!r.ResponseFormat.JSONSchema.Strict || string(r.ResponseFormat.JSONSchema.Schema) != string(vision.ResponseSchema) {
			t.Fatal("structured response schema changed")
		}
	}
}

// TestTranscriptionComparison preserves soccer/screen policy and literal timing guards.
func TestTranscriptionComparison(t *testing.T) {
	names, variants, err := comparisonPrompts("transcription")
	if err != nil || !reflect.DeepEqual(names, []string{"default", "transcription_first"}) ||
		len(variants) != 2 || variants[0] != vision.DefaultPrompt {
		t.Fatalf("bad comparison names=%v err=%v", names, err)
	}
	content, _, _ := strings.Cut(vision.DefaultPrompt, "- clock:")
	if !strings.HasPrefix(variants[1], content) {
		t.Fatal("soccer/screen rubric changed")
	}
	for _, text := range []string{"Never invent seconds", "Do NOT infer", "Never copy missing digits", "SEPARATE", "null", "Return exactly 3 frame objects", "1P", "2P"} {
		if !strings.Contains(variants[1], text) {
			t.Fatalf("missing experimental guard %q", text)
		}
	}
	for _, leaked := range []string{"95:04", "46:35", "77:27", "Gueye", "Yamal"} {
		if strings.Contains(variants[1], leaked) {
			t.Fatalf("sample-specific or numeric clock example leaked: %q", leaked)
		}
	}
}

// TestExplicitPeriodComparison isolates printed-label evidence from every other policy instruction.
func TestExplicitPeriodComparison(t *testing.T) {
	names, variants, err := comparisonPrompts("period-explicit")
	if err != nil || !reflect.DeepEqual(names, []string{"period_explicit"}) || len(variants) != 1 {
		t.Fatalf("bad explicit-period comparison: names=%v err=%v", names, err)
	}
	before, after := strings.Split(vision.DefaultPrompt, "\n"), strings.Split(variants[0], "\n")
	if len(before) != len(after) {
		t.Fatal("changed prompt structure")
	}
	changes := 0
	for i := range before {
		if before[i] != after[i] {
			changes++
			if !strings.HasPrefix(before[i], "- period_label:") || !strings.HasPrefix(after[i], "- period:") {
				t.Fatalf("changed non-period instruction at %d", i)
			}
		}
	}
	if changes != 1 {
		t.Fatalf("changed %d instructions, want one", changes)
	}
	for _, guard := range []string{"default to null", "ALWAYS null", "HALF 1", "HALF 2", "1P", "2P", "superscript", "Do NOT infer"} {
		if !strings.Contains(variants[0], guard) {
			t.Fatalf("missing explicit-label guard %q", guard)
		}
	}
}
