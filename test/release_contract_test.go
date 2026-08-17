// release_contract_test.go verifies immutable identity propagation through the
// production Compose model without building or touching production services.
package test_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"
)

type releaseCompose struct {
	Services map[string]releaseService `yaml:"services"`
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
