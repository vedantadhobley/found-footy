// twitter_storage_contract_test.go prevents implicit headless profile volumes.
package test_test

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"text/template"

	"gopkg.in/yaml.v3"
)

// TestStorageSmokeVolumeMetadata exercises the script's actual Docker template
// with strict missing-key handling; absent volumes are valid, absent config is not.
func TestStorageSmokeVolumeMetadata(t *testing.T) {
	script := readToolingFile(t, repositoryRoot(t), "scripts/smoke_twitter_storage.sh")
	match := regexp.MustCompile(`docker image inspect [^\n]+ --format '([^']+)'`).FindStringSubmatch(script)
	if len(match) != 2 {
		t.Fatal("storage smoke image-inspection template not found")
	}
	format, err := template.New("image-inspect").Option("missingkey=error").Parse(match[1])
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		image   string
		want    string
		wantErr bool
	}{
		{name: "omitted volumes", image: `{"Config":{}}`, want: "0"},
		{name: "null volumes", image: `{"Config":{"Volumes":null}}`, want: "0"},
		{name: "empty volumes", image: `{"Config":{"Volumes":{}}}`, want: "0"},
		{name: "one implicit volume", image: `{"Config":{"Volumes":{"/data":{}}}}`, want: "1"},
		{name: "multiple implicit volumes", image: `{"Config":{"Volumes":{"/data":{},"/config":{}}}}`, want: "2"},
		{name: "missing config fails closed", image: `{}`, wantErr: true},
		{name: "null config fails closed", image: `{"Config":null}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var image map[string]any
			if err := json.Unmarshal([]byte(tc.image), &image); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			err := format.Execute(&output, image)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("malformed image accepted: %s", output.String())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if output.String() != tc.want {
				t.Fatalf("volume count = %q, want %q", output.String(), tc.want)
			}
		})
	}
}

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
