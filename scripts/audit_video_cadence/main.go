// main.go — Offline native-frame cadence diagnostics; never a production quality selector.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

// main emits evidence only after every requested local file has been measured.
func main() {
	flag.Parse()
	if err := run(flag.Args()); err != nil {
		fmt.Fprintf(os.Stderr, "audit_video_cadence: %v\n", err)
		os.Exit(1)
	}
}

// run bounds each decode independently and never opens a network or storage client.
func run(paths []string) error {
	if len(paths) == 0 || len(paths) > 64 {
		return fmt.Errorf("provide 1–64 local video paths; output is diagnostic JSON, not native-FPS certification")
	}
	results := make([]report, 0, len(paths))
	for _, path := range paths {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		result, err := inspect(ctx, path)
		cancel()
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		results = append(results, result)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(results)
}
