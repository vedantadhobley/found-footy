// tooling_contract_test.go keeps local quality gates versioned and consistent
// without running Docker or mutating source files.
package test_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

var semanticVersion = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// TestToolchainVersionsArePinned prevents clean builds from silently resolving
// new Go, golangci-lint, or Air releases. Version updates stay explicit while
// Docker images and module tags remain coordinated.
func TestToolchainVersionsArePinned(t *testing.T) {
	root := repositoryRoot(t)
	makefile := readToolingFile(t, root, "Makefile")
	goVersion := makeVariable(t, makefile, "GO_VERSION")
	lintVersion := makeVariable(t, makefile, "GOLANGCI_LINT_VERSION")
	secretsVersion := makeVariable(t, makefile, "GITLEAKS_VERSION")
	for name, version := range map[string]string{"Go": goVersion, "golangci-lint": lintVersion, "Gitleaks": secretsVersion} {
		if !semanticVersion.MatchString(version) {
			t.Errorf("%s version %q is not an exact semantic version", name, version)
		}
	}

	if !strings.Contains(makefile, "golangci/golangci-lint:v$(GOLANGCI_LINT_VERSION)-alpine") {
		t.Error("Makefile does not derive the linter image from GOLANGCI_LINT_VERSION")
	}
	if strings.Contains(makefile, "golangci/golangci-lint:latest") {
		t.Error("Makefile uses a mutable golangci-lint tag")
	}
	if !strings.Contains(makefile, "ghcr.io/gitleaks/gitleaks:v$(GITLEAKS_VERSION)") {
		t.Error("Makefile does not derive the secret-scanner image from GITLEAKS_VERSION")
	}

	wantGoImage := "FROM golang:" + goVersion + "-bookworm"
	for _, name := range []string{
		"Dockerfile",
		"Dockerfile.dev",
		"docker/twitter/Dockerfile",
		"docker/twitter-auth/Dockerfile",
		"operator-ui/Dockerfile",
	} {
		contents := readToolingFile(t, root, name)
		if !strings.Contains(contents, wantGoImage) {
			t.Errorf("%s does not use pinned builder %q", name, wantGoImage)
		}
	}

	devDockerfile := readToolingFile(t, root, "Dockerfile.dev")
	airVersion := assignmentValue(t, devDockerfile, "ARG AIR_VERSION=")
	if !semanticVersion.MatchString(airVersion) {
		t.Errorf("Air version %q is not an exact semantic version", airVersion)
	}
	if !strings.Contains(devDockerfile, "github.com/air-verse/air@v${AIR_VERSION}") {
		t.Error("Dockerfile.dev does not install the declared Air version")
	}
	if strings.Contains(devDockerfile, "air@latest") {
		t.Error("Dockerfile.dev uses a mutable Air version")
	}
}

// TestGoScopeFollowsGitAndModuleBoundaries keeps ignored forensic programs out
// of product gates while requiring every visible nested module to participate.
func TestGoScopeFollowsGitAndModuleBoundaries(t *testing.T) {
	root := repositoryRoot(t)
	modules := runToolingCommand(t, root, "scripts/go-scope.sh", "modules")
	for _, want := range []string{".", "operator-ui"} {
		if !linePresent(modules, want) {
			t.Errorf("Go module %q missing from scope:\n%s", want, modules)
		}
	}

	files := runToolingCommand(t, root, "scripts/go-scope.sh", "files")
	if strings.Contains(files, "scratch-audit-") {
		t.Errorf("ignored audit file entered Go scope:\n%s", files)
	}
	if !linePresent(files, "operator-ui/cmd/operator-ui/main.go") {
		t.Error("operator UI source is absent from Go scope")
	}

	rootPackages := runToolingCommand(t, root, "scripts/go-scope.sh", "packages", ".")
	if strings.Contains(rootPackages, "operator-ui") {
		t.Errorf("nested module leaked into root packages:\n%s", rootPackages)
	}
	operatorPackages := runToolingCommand(t, root, "scripts/go-scope.sh", "packages", "operator-ui")
	if !linePresent(operatorPackages, "./cmd/operator-ui") {
		t.Errorf("operator UI command package missing:\n%s", operatorPackages)
	}
}

// TestGitHooksRunEngineeringGates protects the distinction between the fast
// pre-commit gate and the integration-bearing pre-push gate.
func TestGitHooksRunEngineeringGates(t *testing.T) {
	root := repositoryRoot(t)
	preCommit := readToolingFile(t, root, ".githooks/pre-commit")
	prePush := readToolingFile(t, root, ".githooks/pre-push")
	if !strings.Contains(preCommit, "make check-short") {
		t.Error("pre-commit does not run make check-short")
	}
	if !strings.Contains(prePush, "make check") {
		t.Error("pre-push does not run make check")
	}
	if !strings.Contains(preCommit, "make secrets-check-staged") {
		t.Error("pre-commit does not inspect the actual staged patch")
	}
	if !strings.Contains(prePush, "make secrets-check-push") || !strings.Contains(prePush, `PUSH_REMOTE="$1"`) {
		t.Error("pre-push does not pass the destination and outgoing refs to secret scanning")
	}
}

// TestSecretScannerBoundary pins the offline container and redaction contract;
// make test-secrets separately verifies actual detection with synthetic inputs.
func TestSecretScannerBoundary(t *testing.T) {
	root := repositoryRoot(t)
	script := readToolingFile(t, root, "scripts/check-secrets.sh")
	for _, required := range []string{
		"--network none", "--read-only", "--memory 512m", "--memory-swap 512m",
		"--cpus 2", "--redact=100", "--ignore-gitleaks-allow", "--gitleaks-ignore-path /dev/null",
		"--diff-merges=first-parent", "--no-ext-diff", "--no-textconv",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("secret scanner boundary is missing %q", required)
		}
	}
	if strings.Contains(script, "/var/run/docker.sock") || strings.Contains(script, "src=$repo_root,dst=") {
		t.Error("secret scanner must not mount Docker access or the full private working tree")
	}
	makefile := readToolingFile(t, root, "Makefile")
	for _, gate := range []string{"check-short: secrets-check", "check: secrets-check"} {
		if !strings.Contains(makefile, gate) {
			t.Errorf("engineering gate missing %q", gate)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Join(filepath.Dir(filename), "..")
}

func readToolingFile(t *testing.T, root, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(contents)
}

func runToolingCommand(t *testing.T, root string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(filepath.Join(root, filepath.FromSlash(name)), args...) // #nosec G204 -- fixed repository script under test.
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run %s %v: %v\n%s", name, args, err, output)
	}
	return string(output)
}

func linePresent(contents, want string) bool {
	for _, line := range strings.Split(strings.TrimSpace(contents), "\n") {
		if line == want {
			return true
		}
	}
	return false
}

func makeVariable(t *testing.T, makefile, name string) string {
	t.Helper()
	for _, line := range strings.Split(makefile, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == name && fields[1] == ":=" {
			return fields[2]
		}
	}
	t.Fatalf("Makefile variable %s not found", name)
	return ""
}

func assignmentValue(t *testing.T, contents, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(contents, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
			return value
		}
	}
	t.Fatalf("assignment %s not found", prefix)
	return ""
}
