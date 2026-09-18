// Benchmark current or paired clock-transcription prompts on explicit local images, without production storage access.
package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	vision "github.com/vedantadhobley/found-footy/internal/domain/vision"
	"github.com/vedantadhobley/found-footy/internal/infra/llm"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
)

// beforePeriodRoles retains the exact old baseline so historical comparisons do not drift.
//
//go:embed testdata/prompt-before-period-roles-20260918.txt
var beforePeriodRoles string

// sample fixes three image paths and provenance before any inference request.
type sample struct {
	Name       string
	Provenance string
	Images     []string
	Expected   vision.Expected           // evaluation only; never sent to the model
	Gold       []vision.FrameObservation // optional transcription controls; never sent to the model
}

// result keeps failed requests alongside successful observations, never retrying for a preferred answer.
type result struct {
	Name              string
	Variant           string
	Provenance        string
	PromptSHA256      string
	SchemaSHA256      string
	ImageSHA256       []string
	Model             string
	ReasoningEffort   llm.ReasoningEffort
	MaxTokens         *int
	Temperature       *float32
	Usage             llm.Usage
	ElapsedMS         int64
	Error             string `json:",omitempty"`
	Response          vision.VisionResponse
	RawResponse       json.RawMessage `json:",omitempty"` // literal experiment's unmodified wire observation
	Evaluation        vision.Evaluation
	TimingDifferences []string `json:",omitempty"`
}

// digest binds results to literal input bytes without storing inline image payloads.
func digest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

// prompts isolates cross-frame field identification from the common literal-transcription rules.
func prompts() ([]string, error) {
	literal, _, ok := strings.Cut(vision.DefaultPrompt, " You may compare")
	if !ok {
		return nil, fmt.Errorf("prompt comparison boundary changed; review benchmark")
	}
	literal += " Never copy missing digits or period labels from another frame, interpolate time, or calculate an absolute match minute. Return exactly 3 frame objects, in order."
	return []string{literal, vision.DefaultPrompt}, nil
}

// comparisonPrompts keeps experimental timing wording outside the runtime prompt.
func comparisonPrompts(comparison string) ([]string, []string, error) {
	if comparison == "current" {
		return []string{"current"}, []string{vision.DefaultPrompt}, nil
	}
	if comparison == "period-explicit" {
		prompt, err := explicitPeriodPrompt()
		return []string{"period_explicit"}, []string{prompt}, err
	}
	if comparison == "period-literal" {
		prompt, err := literalPeriodPrompt()
		return []string{"period_literal"}, []string{prompt}, err
	}
	if comparison == "transcription" {
		prompt, err := transcriptionPrompt()
		return []string{"default", "transcription_first"}, []string{vision.DefaultPrompt, prompt}, err
	}
	if comparison == "context" {
		p, err := prompts()
		return []string{"literal", "context"}, p, err
	}
	if comparison != "period-labels" {
		return nil, nil, fmt.Errorf("unknown comparison %q", comparison)
	}
	baseline := strings.TrimSuffix(beforePeriodRoles, "\n")
	lines := strings.Split(baseline, "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(line, "- period:") {
			lines[i] = `- period: identify a VISIBLE match-half or extra-time-period label by its role and surrounding scorebug layout, not a digit alone. Normalize first-half labels ("1H", "1st", "1re", "1er", including superscript ordinal suffixes) to "1H"; second-half labels ("2H", "2nd", "2e", including superscript ordinal suffixes) to "2H". A bare "1" or "2" counts only when visible labeling clearly establishes that it denotes the HALF; an unexplained isolated number is not enough. Use "ET1" or "ET2" only for an explicitly identified extra-time period. Score digits, channel logos, and added-time allowances such as "+1", "+2", "1′", "2′", or a number labeled ADDED TIME are NOT period labels. Do NOT infer the period from the clock value, expected event, or another frame. null when no unambiguous period label is visible.`
			found = true
		}
	}
	if !found {
		return nil, nil, fmt.Errorf("period instruction missing; review benchmark")
	}
	return []string{"default", "period_roles"}, []string{baseline, strings.Join(lines, "\n")}, nil
}

// timingDifferences compares literal timing fields, not football/content correctness.
// Annotation spelling differences remain visible instead of silently normalizing gold.
func timingDifferences(gold, actual []vision.FrameObservation) []string {
	if len(gold) == 0 {
		return nil
	}
	if len(gold) != len(actual) {
		return []string{"frame count differs"}
	}
	var differences []string
	for i, want := range gold {
		got := actual[i]
		gotPeriod := got.Period
		if gotPeriod == nil && got.PeriodLabel != nil {
			if period, ok := vision.PeriodFromLabel(*got.PeriodLabel); ok {
				gotPeriod = &period
			}
		}
		for _, field := range []struct {
			name      string
			want, got any
		}{
			{"clock", want.Clock, got.Clock}, {"period", want.Period, gotPeriod},
			{"added", want.Added, got.Added}, {"stoppage_clock", want.StoppageClock, got.StoppageClock},
		} {
			wantJSON, _ := json.Marshal(field.want)
			gotJSON, _ := json.Marshal(field.got)
			if string(wantJSON) != string(gotJSON) {
				differences = append(differences, fmt.Sprintf("frame %d %s: want %s, got %s", i+1, field.name, wantJSON, gotJSON))
			}
		}
	}
	return differences
}

// run requires an explicit inference opt-in and preloads every image before contacting the gateway.
func run() error {
	manifest := flag.String("manifest", "", "local JSON sample manifest")
	outPath := flag.String("output", "", "new report path (must not exist)")
	endpoint := flag.String("endpoint", "", "explicit published model gateway")
	model := flag.String("model", "", "real ID verified from /v1/models")
	approved := flag.Bool("run-model", false, "make bounded sequential inference requests")
	comparison := flag.String("comparison", "context", "current, context, period-labels (frozen September 18 pair), period-explicit, period-literal, or transcription")
	reasoning := flag.String("reasoning", "none", "none or high; verify model support in its catalog")
	maxTokens := flag.Int("max-tokens", 0, "output budget; zero preserves the model default")
	temperature := flag.String("temperature", "", "optional [0,2] sampling override, including explicit zero")
	flag.Parse()
	if !*approved || *manifest == "" || *outPath == "" || *endpoint == "" || *model == "" {
		return fmt.Errorf("require --run-model, --manifest, --output, --endpoint and --model")
	}
	effort, budget, err := requestControls(*reasoning, *maxTokens)
	if err != nil {
		return err
	}
	sampling, err := temperatureOverride(*temperature)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(*manifest)
	if err != nil {
		return err
	}
	var samples []sample
	if len(data) > 64<<10 {
		return fmt.Errorf("oversized manifest")
	}
	if err := json.Unmarshal(data, &samples); err != nil {
		return err
	}
	if len(samples) == 0 || len(samples) > 10 {
		return fmt.Errorf("require 1–10 preselected samples")
	}
	variantNames, variants, err := comparisonPrompts(*comparison)
	if err != nil {
		return err
	}
	schema := vision.ResponseSchema
	switch *comparison {
	case "period-literal":
		schema, err = literalPeriodSchema()
		if err != nil {
			return err
		}
	case "period-labels", "period-explicit":
		schema, err = legacyPeriodSchema()
		if err != nil {
			return err
		}
	}
	images := make([][]llm.ChatImage, len(samples))
	hashes := make([][]string, len(samples))
	names := map[string]bool{}
	for i, s := range samples {
		if s.Name == "" || s.Provenance == "" || names[s.Name] || len(s.Images) != 3 || (len(s.Gold) != 0 && len(s.Gold) != 3) {
			return fmt.Errorf("invalid or duplicate sample %q", s.Name)
		}
		names[s.Name] = true
		for _, path := range s.Images {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			mime := http.DetectContentType(b)
			if len(b) > 16<<20 || (mime != "image/jpeg" && mime != "image/png") {
				return fmt.Errorf("invalid/oversized image %s", path)
			}
			images[i] = append(images[i], llm.ChatImage{Data: b, MimeType: mime})
			hashes[i] = append(hashes[i], digest(b))
		}
	}
	file, err := os.OpenFile(*outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	reg := metrics.New()
	log := logging.New(config.ObservabilityConfig{LogLevel: "ERROR"}, reg)
	client, err := llm.NewClient(context.Background(), config.LLMConfig{Endpoint: *endpoint, APIVersionPath: "/v1", APIKey: "not-required", ChatModel: *model,
		ChatConcurrencyCap: 1, ConnectTimeout: 10 * time.Second, RequestTimeout: 60 * time.Second}, llm.RegisterMetrics(reg, log))
	if err != nil {
		return err
	}
	enc := json.NewEncoder(file)
	if err := enc.Encode(map[string]any{"manifest_sha256": digest(data), "evaluator": vision.EvaluatorVersion, "comparison": *comparison,
		"reasoning_effort": effort, "max_tokens": budget, "temperature": sampling, "requested_model": *model, "planned_calls": len(samples) * len(variants)}); err != nil {
		return err
	}
	for i, s := range samples {
		for variant, prompt := range variants {
			r := result{Name: s.Name, Variant: variantNames[variant], Provenance: s.Provenance,
				PromptSHA256: digest([]byte(prompt)), SchemaSHA256: digest(schema), ImageSHA256: hashes[i],
				ReasoningEffort: effort, MaxTokens: budget, Temperature: sampling}
			start := time.Now()
			req := benchmarkRequest(prompt, images[i], effort, budget)
			req.Temperature = sampling
			req.ResponseFormat.JSONSchema.Schema = schema
			resp, err := client.Chat(context.Background(), req)
			r.ElapsedMS = time.Since(start).Milliseconds()
			if err != nil {
				r.Error = err.Error()
			} else {
				r.Model = resp.Model
				r.Usage = resp.Usage
				parseErr := json.Unmarshal([]byte(resp.Content), &r.Response)
				if *comparison == "period-literal" {
					r.Response, parseErr = decodeLiteralPeriod(resp.Content)
					if json.Valid([]byte(resp.Content)) {
						r.RawResponse = json.RawMessage(resp.Content)
					}
				}
				if parseErr != nil {
					r.Error = "invalid JSON response"
				} else if len(r.Response.Frames) != 3 {
					r.Error = "response did not contain three frames"
				} else {
					r.Evaluation = vision.Evaluate(r.Response.Frames, s.Expected, 1)
					r.TimingDifferences = timingDifferences(s.Gold, r.Response.Frames)
				}
			}
			if err := enc.Encode(r); err != nil {
				return err
			}
			if err := file.Sync(); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "%s/%s reasoning=%s completed; error=%t\n", s.Name, r.Variant, effort, r.Error != "")
		}
	}
	return enc.Encode(map[string]any{"complete": true, "calls": len(samples) * len(variants)})
}

// main signals completion explicitly; process disappearance is not a successful benchmark.
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
