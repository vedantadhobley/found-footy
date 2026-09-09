// twitter_storage_contract_test.go prevents implicit headless profile volumes.
package test_test

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestHeadlessImageDoesNotDeclareVolumes pins disposable storage at the image
// boundary; explicit Compose mounts do not require Dockerfile VOLUME entries.
func TestHeadlessImageDoesNotDeclareVolumes(t *testing.T) {
	dockerfile := readToolingFile(t, repositoryRoot(t), "docker/twitter/Dockerfile")
	for _, line := range strings.Split(dockerfile, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && strings.EqualFold(fields[0], "VOLUME") {
			t.Fatalf("headless image creates implicit persistent storage: %s", line)
		}
	}
}

// TestImageContextExcludesAuditScratch keeps locally downloaded media and
// diagnostic artifacts out of the application build stages.
func TestImageContextExcludesAuditScratch(t *testing.T) {
	ignored := readToolingFile(t, repositoryRoot(t), ".dockerignore")
	if !strings.Contains("\n"+ignored, "\nscratch-audit-*/\n") {
		t.Fatal("image context must exclude scratch-audit directories")
	}
}

// TestTwitterComposeStorageOwnership reads both environment contracts without
// loading secrets or invoking Compose against either live environment.
func TestTwitterComposeStorageOwnership(t *testing.T) {
	for _, name := range []string{"docker-compose.dev.yml", "docker-compose.prod.yml"} {
		t.Run(name, func(t *testing.T) {
			var compose struct {
				Services map[string]struct {
					Volumes []string `yaml:"volumes"`
				} `yaml:"services"`
			}
			if err := yaml.Unmarshal([]byte(readToolingFile(t, repositoryRoot(t), name)), &compose); err != nil {
				t.Fatal(err)
			}
			search := compose.Services["twitter"].Volumes
			if len(search) != 1 || search[0] != "~/.config/found-footy:/config" {
				t.Fatalf("headless storage = %v, want only the cookie-directory bind", search)
			}
			login := compose.Services["twitter-vnc"].Volumes
			want := map[string]bool{"twitter-vnc-profile:/data": true, "~/.config/found-footy:/config": true}
			if len(login) != len(want) {
				t.Fatalf("login storage = %v, want explicit profile and cookie mounts", login)
			}
			for _, mount := range login {
				if !want[mount] {
					t.Fatalf("unexpected login storage: %s", mount)
				}
				delete(want, mount)
			}
		})
	}
}
