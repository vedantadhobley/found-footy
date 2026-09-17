// Offline comparison of recorded vision observations; no model or production I/O.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
)

// main reports only after all inputs have passed identity and baseline checks.
func main() {
	replayCurrent := flag.Bool("replay-current", false, "retain recorded verdicts and report evaluator changes separately")
	flag.Parse()
	if err := runPolicy(flag.Args(), os.Stdout, *replayCurrent); err != nil {
		fmt.Fprintf(os.Stderr, "audit_clock_tolerance: %v\n", err)
		os.Exit(1)
	}
}

// run consumes explicit local Temporal JSON exports, never fetching missing data.
func run(paths []string, output io.Writer) error {
	return runPolicy(paths, output, false)
}

// runPolicy makes any departure from strict historical reproduction an explicit choice.
func runPolicy(paths []string, output io.Writer, replayCurrent bool) error {
	if len(paths) == 0 || len(paths) > 128 {
		return fmt.Errorf("provide 1–128 local EventWorkflow history JSON paths")
	}
	var inputs []historyInput
	for _, path := range paths {
		input, err := readHistory(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		inputs = append(inputs, input)
	}
	result, err := comparePolicy(inputs, replayCurrent)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}
