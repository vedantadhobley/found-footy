// Request controls and transcription wording are benchmark-only, never runtime defaults.
package main

import (
	"fmt"
	"strings"

	vision "github.com/vedantadhobley/found-footy/internal/domain/vision"
	"github.com/vedantadhobley/found-footy/internal/infra/llm"
)

// requestControls rejects accidental reasoning/budget changes before inference.
// The gateway enforces the selected model's catalog-specific maximum.
func requestControls(reasoning string, maxTokens int) (llm.ReasoningEffort, *int, error) {
	effort := llm.ReasoningEffort(reasoning)
	if effort != llm.ReasoningEffortNone && effort != llm.ReasoningEffortHigh {
		return "", nil, fmt.Errorf("reasoning must be none or high")
	}
	if maxTokens < 0 {
		return "", nil, fmt.Errorf("max-tokens must be zero (model default) or positive")
	}
	if maxTokens == 0 {
		return effort, nil, nil
	}
	return effort, &maxTokens, nil
}

// benchmarkRequest has no access to gold, API expectations or sample names.
func benchmarkRequest(prompt string, images []llm.ChatImage, effort llm.ReasoningEffort, budget *int) llm.ChatRequest {
	return llm.ChatRequest{
		Messages:        []llm.ChatMessage{{Role: llm.RoleUser, Content: prompt, Images: images}},
		ReasoningEffort: effort,
		MaxTokens:       budget,
		ResponseFormat: &llm.ResponseFormat{JSONSchema: &llm.JSONSchema{
			Name: "frame_validation", Schema: vision.ResponseSchema, Strict: true,
		}},
	}
}

// transcriptionPrompt preserves the content rubric but removes numeric clock
// examples and makes absence of a printed label an explicit null observation.
func transcriptionPrompt() (string, error) {
	content, _, found := strings.Cut(vision.DefaultPrompt, "- clock:")
	if !found {
		return "", fmt.Errorf("clock instruction boundary changed; review benchmark")
	}
	return content + `Read the printed scorebug in EACH image separately. These fields describe visible text, not your knowledge of football timing. Missing fields are null; do not fill them just because a value would make sense.
- clock: copy the MAIN match timer's printed text. Preserve the digits and separators that are visible. Never invent seconds or append ":00" to a minute-only number. null if unreadable or absent.
- added: copy the announced added-time allowance, including its visible plus sign or minute mark. This is a separate field from the match timer, not elapsed time. null if absent.
- stoppage_clock: copy a SEPARATE elapsed-stoppage timer only if it visibly contains seconds. Do not split the main timer into two timers, convert an allowance to seconds, or calculate anything. null if absent.
- period_label: copy an actual HALF/PERIOD LABEL printed in that image, or null. A clock value, score digit, allowance, or knowledge that stoppage is being played is NOT a label. Preserve labels such as 1H/1st/1re/1er/1P and 2H/2nd/2e/2P literally, including superscript suffix letters. Do NOT infer a label from the clock value or from the other frames.
You may compare layout across images to locate fields. Never copy missing digits or period labels between frames, interpolate a changing timer, or replace one frame's reading with another's. Repeated text can be a replay or freeze-frame. Return exactly 3 frame objects, in image order.`, nil
}

// explicitPeriodPrompt tests a default-null label rule without altering the runtime contract.
func explicitPeriodPrompt() (string, error) {
	lines := strings.Split(strings.TrimSuffix(beforePeriodRoles, "\n"), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "- period:") {
			lines[i] = `- period: default to null. Report a period ONLY when this frame visibly prints a half/period label with letters or an ordinal suffix: "1H", "1st", "1re", "1er", "1P", "HALF 1" mean "1H"; "2H", "2nd", "2e", "2P", "HALF 2" mean "2H". Lowercase and superscript ordinal suffixes count. An isolated "1" or "2" WITHOUT those printed letters is ALWAYS null, even beside the timer. Use "ET1" or "ET2" only when printed text explicitly identifies the extra-time period. A plus sign, allowance, score digit, clock value or logo is NOT a half label. Stoppage time can be visible while period is null. Do NOT infer the period from football knowledge, expected time, another frame or the scorebug layout. If no qualifying text is readable in THIS frame, return null.`
			return strings.Join(lines, "\n"), nil
		}
	}
	return "", fmt.Errorf("period instruction missing; review benchmark")
}
