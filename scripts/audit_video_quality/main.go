// main.go — read-only FF-081 video-quality graph and keeper-policy audit.
//
// The command consumes query.sql's CSV on stdin. It has no database client or
// credentials and cannot mutate production. Example from the repository root:
//
//	docker exec -i found-footy-prod-postgres psql -qAt -v ON_ERROR_STOP=1 \
//	  -U ffuser -d found_footy < scripts/audit_video_quality/query.sql | \
//	docker run --rm -i -v "$PWD:/src" -w /src golang:1.25.11-bookworm \
//	  go run ./scripts/audit_video_quality
package main

import (
	"flag"
	"fmt"
	"os"
)

// main validates flags, reads the corpus, and emits a bounded report.
func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "audit_video_quality: %v\n", err)
		os.Exit(1)
	}
}

// run owns the command's testable stdin-to-report boundary.
func run() error {
	maxPermutations := flag.Int("max-permutations", 100_000,
		"maximum exhaustive or deterministic sampled arrival orders per component")
	detailLimit := flag.Int("details", 30, "maximum prioritized components to print; -1 prints all")
	reviewCSV := flag.Bool("review-csv", false,
		"emit a stable direct-pair human-review manifest instead of the diagnostic report")
	overlapJSON := flag.Bool("overlap-json", false,
		"emit offline aligned-section NDJSON beside existing quality baselines; no keeper changes")
	pairCorpus := flag.Bool("pair-corpus", false,
		"read a reviewed-pair JSON corpus from stdin; requires -overlap-json")
	flag.Parse()
	if err := validateOverlapFlags(*reviewCSV, *overlapJSON, *pairCorpus); err != nil {
		return err
	}
	if *pairCorpus {
		return writePairCorpusOverlapJSON(os.Stdout, os.Stdin)
	}
	if *maxPermutations < 1 {
		return fmt.Errorf("max-permutations must be positive")
	}

	assets, err := readAssets(os.Stdin)
	if err != nil {
		return err
	}
	if len(assets) == 0 {
		return fmt.Errorf("empty asset corpus")
	}
	if *overlapJSON {
		return writeOverlapJSON(os.Stdout, assets)
	}
	result := analyze(assets, *maxPermutations)
	if *reviewCSV {
		return writeReviewCSV(os.Stdout, result)
	}
	printReport(os.Stdout, result, *detailLimit)
	return nil
}

// validateOverlapFlags prevents ambiguous output modes or accidental JSON-as-CSV reads.
func validateOverlapFlags(reviewCSV, overlapJSON, pairCorpus bool) error {
	if reviewCSV && overlapJSON {
		return fmt.Errorf("review-csv and overlap-json are mutually exclusive")
	}
	if pairCorpus && !overlapJSON {
		return fmt.Errorf("pair-corpus requires overlap-json")
	}
	return nil
}
