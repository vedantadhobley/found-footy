// release_contract_test.go verifies immutable identity propagation through the
// production Compose model without building or touching production services.
package test_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type releaseCompose struct {
	Services map[string]releaseService `yaml:"services"`
}

// TestProductionDeployGuardsFleetByOwnership prevents the release gate from
// coupling itself to one generation of event-container names. It also pins a
// second check after image construction, immediately before service mutation.
func TestProductionDeployGuardsFleetByOwnership(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	scriptPath := filepath.Join(filepath.Dir(filename), "..", "scripts", "deploy-prod.sh")
	contents, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read production release script: %v", err)
	}
	script := string(contents)

	for _, required := range []string{
		`--filter "network=$PROD_NETWORK"`,
		`--filter "label=found-footy.fleet=firefox"`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("production release script missing ownership selector %q", required)
		}
	}
	if strings.Contains(script, "awk '/^ff-firefox-ev-/") {
		t.Error("production release script still depends on the legacy container-name prefix")
	}

	const guardCall = "\nassert_no_active_fleet\n"
	if got := strings.Count(script, guardCall); got != 2 {
		t.Fatalf("assert_no_active_fleet call count = %d, want 2", got)
	}
	finalGuard := strings.LastIndex(script, guardCall)
	build := strings.Index(script, `"${compose[@]}" build`)
	mutation := strings.Index(script, `"${compose[@]}" up -d`)
	if build < 0 || mutation < 0 || !(build < finalGuard && finalGuard < mutation) {
		t.Fatalf("final fleet guard must run after build and before mutation (build=%d guard=%d mutation=%d)", build, finalGuard, mutation)
	}
}

type releaseService struct {
	Build struct {
		Args map[string]string `yaml:"args"`
	} `yaml:"build"`
	Image       string            `yaml:"image"`
	Environment map[string]string `yaml:"environment"`
}

// TestProductionComposePropagatesReleaseIdentity prevents any application
// image from silently falling back to unknown build metadata or a mutable tag.
func TestProductionComposePropagatesReleaseIdentity(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	composePath := filepath.Join(filepath.Dir(filename), "..", "docker-compose.prod.yml")
	contents, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("read production Compose file: %v", err)
	}

	var compose releaseCompose
	if err := yaml.Unmarshal(contents, &compose); err != nil {
		t.Fatalf("parse production Compose file: %v", err)
	}

	const (
		gitSHAArg  = "${GIT_SHA:-unknown}"
		builtAtArg = "${BUILT_AT:-unknown}"
		imageTag   = "${IMAGE_TAG:-latest}"
	)
	wantImages := map[string]string{
		"worker":      "found-footy-worker:" + imageTag,
		"api":         "found-footy-api:" + imageTag,
		"twitter":     "found-footy-prod-twitter:" + imageTag,
		"twitter-vnc": "found-footy-prod-twitter-vnc:" + imageTag,
	}

	for name, wantImage := range wantImages {
		service, ok := compose.Services[name]
		if !ok {
			t.Errorf("service %q is missing", name)
			continue
		}
		if got := service.Build.Args["GIT_SHA"]; got != gitSHAArg {
			t.Errorf("%s build GIT_SHA = %q, want %q", name, got, gitSHAArg)
		}
		if got := service.Build.Args["BUILT_AT"]; got != builtAtArg {
			t.Errorf("%s build BUILT_AT = %q, want %q", name, got, builtAtArg)
		}
		if service.Image != wantImage {
			t.Errorf("%s image = %q, want %q", name, service.Image, wantImage)
		}
		if got := service.Environment["IMAGE_TAG"]; got != imageTag {
			t.Errorf("%s IMAGE_TAG = %q, want %q", name, got, imageTag)
		}
	}

	worker := compose.Services["worker"]
	wantFleetImage := "found-footy-prod-twitter:" + imageTag
	if got := worker.Environment["FIREFOXFLEET_IMAGE"]; got != wantFleetImage {
		t.Errorf("worker FIREFOXFLEET_IMAGE = %q, want %q", got, wantFleetImage)
	}
}
