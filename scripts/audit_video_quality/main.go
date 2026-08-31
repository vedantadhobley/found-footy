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
	flag.Parse()
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
	result := analyze(assets, *maxPermutations)
	printReport(os.Stdout, result, *detailLimit)
	return nil
}
