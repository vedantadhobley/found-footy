// scripts/probe_vertical/main.go — #182 vertical-recovery experiment: do the
// aspect-rejected VERTICAL clips actually contain goal footage, or is the
// [1.75,1.82] 16:9 hard-filter correctly discarding reaction cams / phone spam?
//
// The production pipeline rejects any clip outside the aspect band at
// PRE-DOWNLOAD (prefilter), so a vertical clip (9:16≈0.56, 3:4=0.75) never
// reaches the vision model — we have zero data on whether it was a real goal.
// This script replays the SAME path as probe_vision (syndication resolve →
// download → ffmpeg 3-frame extract → structured vision call → domain
// Evaluate) but SKIPS every format gate, so vertical clips reach the model. It
// tallies the verdicts so we can measure the recall the aspect gate costs us.
//
// Reads a TSV from stdin: <tweet_url>\t<elapsed>\t<extra>\t<label>
// Runs SEQUENTIALLY (one model call in flight) to respect joi's slot cap even
// while a live match is validating.
//
// Verdict buckets (internal/domain/vision):
//
//	verified   — soccer, not a screen-recording, clock matches the API minute
//	unverified — soccer, not a screen-recording, clock NOT readable (exactly
//	             what a vertical reframe with a graphic header produces)
//	rejected   — not soccer / phone-of-TV screen recording / clock contradicts
//
// verified+unverified = "would surface" = real footage the aspect gate killed.
//
// Run (dev-worker has .env for LLM + syndication; source bind-mounted at /src):
//
//	docker exec -i found-footy-dev-worker sh -c 'cd /src && go run ./scripts/probe_vertical' < sample.tsv
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vedantadhobley/found-footy/internal/config"
	dvision "github.com/vedantadhobley/found-footy/internal/domain/vision"
	"github.com/vedantadhobley/found-footy/internal/infra/ffmpeg"
	"github.com/vedantadhobley/found-footy/internal/infra/llm"
	"github.com/vedantadhobley/found-footy/internal/infra/syndication"
	"github.com/vedantadhobley/found-footy/internal/observability/logging"
	"github.com/vedantadhobley/found-footy/internal/observability/metrics"
)

// framePositions — the same 25/50/75% sampling ValidateClip uses.
var framePositions = []float64{0.25, 0.50, 0.75}

// sample is one clip to replay: its tweet URL + the API clock to validate
// against (the scoring event's minute + stoppage) + a human label.
type sample struct {
	url            string
	elapsed, extra int
	label          string
}

// result is the per-clip outcome. status is "ok" when the clip reached the
// vision model; anything else records where it fell out (unresolvable, deleted,
// geo-blocked, un-probeable) so the tally separates "model said no" from
// "never got there".
type result struct {
	label   string
	url     string
	aspect  float64
	status  string
	outcome dvision.Outcome
	reason  string
	soccer  int
	screen  int
	frames  int
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	samples, err := readSamples(os.Stdin)
	if err != nil {
		return fmt.Errorf("read samples: %w", err)
	}
	if len(samples) == 0 {
		return fmt.Errorf("no samples on stdin (expect TSV: url<TAB>elapsed<TAB>extra<TAB>label)")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config load: %w", err)
	}
	reg := metrics.New()
	log := logging.New(cfg.Observability, reg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	synd, err := syndication.NewClient(cfg.Syndication, syndication.RegisterMetrics(reg, log))
	if err != nil {
		return fmt.Errorf("syndication client: %w", err)
	}
	ff, err := ffmpeg.NewClient(cfg.FFmpeg, ffmpeg.RegisterMetrics(reg, log))
	if err != nil {
		return fmt.Errorf("ffmpeg client: %w", err)
	}
	llmc, err := llm.NewClient(ctx, cfg.LLM, llm.RegisterMetrics(reg, log))
	if err != nil {
		return fmt.Errorf("llm client: %w", err)
	}

	prompt := cfg.Vision.Prompt
	if prompt == "" {
		prompt = dvision.DefaultPrompt
	}

	fmt.Printf("Model: %s | %d vertical clips | ASPECT GATE BYPASSED | tol=±%d'\n\n",
		llmc.ChatModel(), len(samples), cfg.Vision.ToleranceMinutes)

	results := make([]result, 0, len(samples))
	for i, s := range samples {
		fmt.Printf("──── %d/%d [%s] %s\n", i+1, len(samples), s.label, s.url)
		r := probeOne(ctx, synd, ff, llmc, prompt, cfg, s)
		results = append(results, r)
		if r.status == "ok" {
			fmt.Printf("   aspect=%.3f  soccer=%d/%d screen=%d  ► %s %q\n",
				r.aspect, r.soccer, r.frames, r.screen, r.outcome, r.reason)
		} else {
			fmt.Printf("   aspect=%.3f  ✗ %s\n", r.aspect, r.status)
		}
	}

	printTally(results)
	return nil
}

// probeOne replays one clip through the real V-phase path with NO format gate.
// Any fall-out before the model is recorded in result.status; the aspect is
// still measured + reported (that's the variable under test), just never used
// to reject.
func probeOne(ctx context.Context, synd *syndication.Client, ff *ffmpeg.Client, llmc *llm.Client,
	prompt string, cfg *config.Config, s sample) result {

	r := result{label: s.label, url: s.url, status: "ok"}

	rv, err := synd.ResolveVideo(ctx, s.url)
	if err != nil {
		r.status = "resolve-fail"
		return r
	}
	if rv.Height > 0 {
		r.aspect = float64(rv.Width) / float64(rv.Height)
	}

	tmp, err := os.MkdirTemp("", "probe-vertical-*")
	if err != nil {
		r.status = "tmp-fail"
		return r
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	vidPath := filepath.Join(tmp, "clip.mp4")
	f, err := os.Create(vidPath)
	if err != nil {
		r.status = "tmp-fail"
		return r
	}
	_, derr := synd.Download(ctx, rv.VariantURL, f)
	_ = f.Close()
	if derr != nil {
		r.status = "download-fail"
		return r
	}

	// NO PreFilter / HardFilter here — bypassing the aspect gate is the point.
	meta, err := ff.ProbeMetadata(ctx, vidPath)
	if err != nil {
		r.status = "probe-fail"
		return r
	}

	images := make([]llm.ChatImage, 0, len(framePositions))
	for _, frac := range framePositions {
		jpeg, err := ff.ExtractFrame(ctx, vidPath, frac*meta.DurationSecs, cfg.Vision.FrameQuality)
		if err != nil {
			r.status = "extract-fail"
			return r
		}
		images = append(images, llm.ChatImage{Data: jpeg, MimeType: "image/jpeg"})
	}

	temp := cfg.Vision.Temperature
	resp, err := llmc.Chat(ctx, llm.ChatRequest{
		Messages:        []llm.ChatMessage{{Role: llm.RoleUser, Content: prompt, Images: images}},
		Temperature:     &temp,
		DisableThinking: cfg.Vision.DisableThinking,
		ResponseFormat: &llm.ResponseFormat{
			JSONSchema: &llm.JSONSchema{Name: "frame_validation", Schema: dvision.ResponseSchema, Strict: true},
		},
	})
	if err != nil {
		r.status = "vision-fail"
		return r
	}
	var vr dvision.VisionResponse
	if err := json.Unmarshal([]byte(resp.Content), &vr); err != nil {
		r.status = "parse-fail"
		return r
	}

	ev := dvision.Evaluate(vr.Frames, dvision.Expected{Elapsed: s.elapsed, Extra: s.extra}, cfg.Vision.ToleranceMinutes)
	r.outcome = ev.Outcome
	r.reason = ev.Reason
	r.soccer = ev.SoccerVotes
	r.screen = ev.ScreenVotes
	r.frames = ev.FrameCount
	return r
}

// printTally aggregates the verdicts. The headline number is WOULD SURFACE
// (verified+unverified) as a share of clips that reached the model — the recall
// the aspect gate is currently costing us.
func printTally(results []result) {
	var reached, verified, unverified, rejected int
	statusCount := map[string]int{}
	rejReasons := map[string]int{}
	for _, r := range results {
		statusCount[r.status]++
		if r.status != "ok" {
			continue
		}
		reached++
		switch r.outcome {
		case dvision.OutcomeVerified:
			verified++
		case dvision.OutcomeUnverified:
			unverified++
		case dvision.OutcomeRejected:
			rejected++
			rejReasons[r.reason]++
		}
	}
	total := len(results)
	surface := verified + unverified

	fmt.Printf("\n══════════════════ TALLY ══════════════════\n")
	fmt.Printf("sample:          %d vertical clips\n", total)
	fmt.Printf("reached vision:  %d  (rest unresolvable / deleted / geo-blocked)\n", reached)
	if reached > 0 {
		fmt.Printf("  verified:      %2d  (%3.0f%% of reached) — soccer + clock matched\n", verified, pct(verified, reached))
		fmt.Printf("  unverified:    %2d  (%3.0f%%) — soccer, clock hidden (vertical reframe)\n", unverified, pct(unverified, reached))
		fmt.Printf("  rejected:      %2d  (%3.0f%%) — not soccer / screen-of-TV / clock wrong\n", rejected, pct(rejected, reached))
		fmt.Printf("  ─────\n")
		fmt.Printf("  WOULD SURFACE: %2d  (%3.0f%% of reached) ← recall the aspect gate costs\n", surface, pct(surface, reached))
	}
	fmt.Printf("\nstatus breakdown:\n")
	for _, st := range sortedKeys(statusCount) {
		fmt.Printf("  %-14s %d\n", st, statusCount[st])
	}
	if len(rejReasons) > 0 {
		fmt.Printf("\nreject reasons (the model's own words):\n")
		for _, rs := range sortedKeys(rejReasons) {
			fmt.Printf("  %-44s %d\n", rs, rejReasons[rs])
		}
	}
}

func pct(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return 100 * float64(n) / float64(d)
}

// readSamples parses the stdin TSV. Blank lines + '#' comments are skipped;
// rows with fewer than 3 columns are ignored (label is optional).
func readSamples(r io.Reader) ([]sample, error) {
	var out []sample
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		el, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
		ex, _ := strconv.Atoi(strings.TrimSpace(parts[2]))
		lbl := ""
		if len(parts) >= 4 {
			lbl = strings.TrimSpace(parts[3])
		}
		out = append(out, sample{url: strings.TrimSpace(parts[0]), elapsed: el, extra: ex, label: lbl})
	}
	return out, sc.Err()
}

func sortedKeys(m map[string]int) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
