// scenarios_test.go — the corpus runner. Iterates every YAML under
// test/scenarios/**/*.yaml, treats each as a distinct subtest, runs
// it against the shared testcontainer Postgres + mock apifootball
// server.
//
// A failure in one scenario does not affect others (each subtest
// truncates pg on entry). Test names include the suite subdirectory
// (basic / debounce / faults / edge_cases / regression) for easy
// filtering: `go test -run TestScenarios/basic ./test`.
package test_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vedantadhobley/found-footy/test/harness"
)

func TestScenarios(t *testing.T) {
	if testing.Short() {
		t.Skip("scenario corpus skipped in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// One testcontainer pg for the whole run — scenarios share it via
	// TRUNCATE between subtests.
	pool, _ := harness.SetupPG(ctx, t)
	// One mock apifootball server — reused by every scenario since
	// each scenario reconfigures its responses via SetResponses.
	mockAPI := harness.NewMockAPI(t)

	scenariosDir := findScenariosDir(t)
	scenarios := discoverScenarios(t, scenariosDir)
	if len(scenarios) == 0 {
		t.Fatal("no scenario YAML files discovered under test/scenarios/")
	}

	for _, path := range scenarios {
		rel, _ := filepath.Rel(scenariosDir, path)
		name := strings.TrimSuffix(rel, filepath.Ext(rel))
		// Convert path separators to slashes for cross-platform test names.
		name = filepath.ToSlash(name)
		t.Run(name, func(t *testing.T) {
			s, err := harness.LoadScenario(path)
			if err != nil {
				t.Fatalf("LoadScenario: %v", err)
			}
			harness.RunScenario(ctx, t, pool, mockAPI, s)
		})
	}
}

// discoverScenarios walks the scenarios directory tree and returns
// every *.yaml file. Sorted for stable subtest ordering.
func discoverScenarios(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("discoverScenarios walk: %v", err)
	}
	return out
}

// findScenariosDir locates test/scenarios/ by walking up from the
// test binary's cwd.
func findScenariosDir(t *testing.T) string {
	t.Helper()
	cwd, _ := os.Getwd()
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(cwd, "test", "scenarios")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		// Also try 'scenarios' if we're already inside test/
		candidate2 := filepath.Join(cwd, "scenarios")
		if info, err := os.Stat(candidate2); err == nil && info.IsDir() {
			return candidate2
		}
		cwd = filepath.Dir(cwd)
	}
	t.Fatal("scenarios directory not found within 8 parents of cwd")
	return ""
}
