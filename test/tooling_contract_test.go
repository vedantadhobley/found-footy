// tooling_contract_test.go keeps local quality gates versioned and consistent
// without running Docker or mutating source files.
package test_test

import (
	"os"
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
	for name, version := range map[string]string{"Go": goVersion, "golangci-lint": lintVersion} {
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

	wantGoImage := "FROM golang:" + goVersion + "-bookworm"
	for _, name := range []string{
		"Dockerfile",
		"Dockerfile.dev",
		"docker/twitter/Dockerfile",
		"docker/twitter-auth/Dockerfile",
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
