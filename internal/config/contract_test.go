// contract_test.go verifies that Go environment tags, the canonical dotenv
// template, and both Compose application-service routes stay synchronized.
package config

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var composeInterpolation = regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*)([^}]*)\}`)

var composeOnlyVariables = map[string]struct{}{
	"PG_USER":              {},
	"PG_PASSWORD":          {},
	"TEMPORAL_PG_USER":     {},
	"TEMPORAL_PG_PASSWORD": {},
	"GARAGE_RPC_SECRET":    {},
	"GARAGE_ADMIN_TOKEN":   {},
	"GARAGE_METRICS_TOKEN": {},
}

func TestEnvironmentTemplateMatchesConsumedContract(t *testing.T) {
	root := configRepositoryRoot(t)
	example := parseDotenvTemplate(t, filepath.Join(root, ".env.example"))
	all, required := configVariableMetadata(t)

	for name := range required {
		if _, ok := example[name]; !ok {
			t.Errorf(".env.example is missing required Go variable %s", name)
		}
	}
	for name := range example {
		if _, ok := all[name]; ok {
			continue
		}
		if _, ok := composeOnlyVariables[name]; !ok {
			t.Errorf(".env.example contains unconsumed variable %s", name)
		}
	}

	for _, name := range []string{"docker-compose.dev.yml", "docker-compose.prod.yml"} {
		contents := readConfigContractFile(t, filepath.Join(root, name))
		for _, match := range composeInterpolation.FindAllStringSubmatch(contents, -1) {
			if strings.HasPrefix(match[2], ":-") {
				continue
			}
			if _, ok := example[match[1]]; !ok {
				t.Errorf("%s requires %s but .env.example does not declare it", name, match[1])
			}
		}
	}
}

func TestComposeRoutesOnlyOwnedApplicationEnvironment(t *testing.T) {
	root := configRepositoryRoot(t)
	profiles := map[string]Binary{
		"worker":      BinaryWorker,
		"api":         BinaryAPI,
		"twitter":     BinaryTwitter,
		"twitter-vnc": BinaryTwitter,
	}
	allowedMetadata := map[string]struct{}{"IMAGE_TAG": {}}

	for _, name := range []string{"docker-compose.dev.yml", "docker-compose.prod.yml"} {
		compose := parseComposeContract(t, filepath.Join(root, name))
		for serviceName, binary := range profiles {
			service, ok := compose.Services[serviceName]
			if !ok {
				t.Errorf("%s is missing application service %s", name, serviceName)
				continue
			}
			if !containsDotenv(service.EnvFile) {
				t.Errorf("%s service %s does not load .env", name, serviceName)
			}
			owned, err := variablesFor(binary)
			if err != nil {
				t.Fatalf("variablesFor(%s): %v", binary, err)
			}
			ownedSet := make(map[string]struct{}, len(owned))
			for _, variable := range owned {
				ownedSet[variable] = struct{}{}
			}
			for variable := range service.Environment {
				if _, ok := ownedSet[variable]; ok {
					continue
				}
				if _, ok := allowedMetadata[variable]; !ok {
					t.Errorf("%s routes unowned variable %s to %s", name, variable, serviceName)
				}
			}
		}
	}
}

func TestComposePreservesEnvironmentAndCookieOwnership(t *testing.T) {
	root := configRepositoryRoot(t)
	dev := parseComposeContract(t, filepath.Join(root, "docker-compose.dev.yml"))
	prod := parseComposeContract(t, filepath.Join(root, "docker-compose.prod.yml"))

	requireEnvironmentValue(t, dev, "worker", "FIREFOXFLEET_NETWORK", "found-footy-dev")
	requireEnvironmentValue(t, prod, "worker", "FIREFOXFLEET_NETWORK", "found-footy-prod")
	requireEnvironmentValue(t, prod, "worker", "EVENT_ENV", "prod")
	for _, compose := range []composeContract{dev, prod} {
		if _, ok := compose.Services["api"].Environment["EVENT_ENV"]; ok {
			t.Errorf("%s routes worker-only EVENT_ENV to api", compose.Name)
		}
		for _, serviceName := range []string{"twitter", "twitter-vnc"} {
			if !hasParentCookieMount(compose.Services[serviceName].Volumes) {
				t.Errorf("%s service %s must mount the cookie parent directory at /config", compose.Name, serviceName)
			}
		}
	}
}

type composeContract struct {
	Name     string                            `yaml:"name"`
	Services map[string]composeServiceContract `yaml:"services"`
}

type composeServiceContract struct {
	EnvFile     any            `yaml:"env_file"`
	Environment map[string]any `yaml:"environment"`
	Volumes     []string       `yaml:"volumes"`
}

func parseComposeContract(t *testing.T, name string) composeContract {
	t.Helper()
	var out composeContract
	if err := yaml.Unmarshal([]byte(readConfigContractFile(t, name)), &out); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return out
}

func parseDotenvTemplate(t *testing.T, name string) map[string]string {
	t.Helper()
	variables := make(map[string]string)
	for lineNumber, line := range strings.Split(readConfigContractFile(t, name), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			t.Fatalf("%s:%d is not a dotenv assignment", name, lineNumber+1)
		}
		if _, exists := variables[key]; exists {
			t.Fatalf("%s declares %s more than once", name, key)
		}
		variables[key] = value
	}
	return variables
}

func configVariableMetadata(t *testing.T) (all, required map[string]struct{}) {
	t.Helper()
	all = make(map[string]struct{})
	required = make(map[string]struct{})
	typ := reflect.TypeOf(Config{})
	seenSections := make(map[string]struct{})
	for _, sections := range binarySections {
		for _, section := range sections {
			if _, ok := seenSections[section]; ok {
				continue
			}
			seenSections[section] = struct{}{}
			field, ok := typ.FieldByName(section)
			if !ok {
				t.Fatalf("unknown config section %s", section)
			}
			collectVariableMetadata(field.Type, all, required)
		}
	}
	return all, required
}

func collectVariableMetadata(typ reflect.Type, all, required map[string]struct{}) {
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if name := field.Tag.Get("env"); name != "" {
			all[name] = struct{}{}
			if _, hasDefault := field.Tag.Lookup("envDefault"); !hasDefault {
				required[name] = struct{}{}
			}
			continue
		}
		if field.Type.Kind() == reflect.Struct {
			collectVariableMetadata(field.Type, all, required)
		}
	}
}

func containsDotenv(value any) bool {
	switch typed := value.(type) {
	case string:
		return typed == ".env"
	case []any:
		for _, item := range typed {
			if item == ".env" {
				return true
			}
		}
	}
	return false
}

func hasParentCookieMount(volumes []string) bool {
	for _, volume := range volumes {
		source, target, ok := strings.Cut(volume, ":")
		if ok && target == "/config" && !strings.HasSuffix(source, "twitter_cookies.json") {
			return true
		}
	}
	return false
}

func requireEnvironmentValue(t *testing.T, compose composeContract, service, variable, want string) {
	t.Helper()
	got := compose.Services[service].Environment[variable]
	if got != want {
		t.Errorf("%s %s.%s = %v, want %q", compose.Name, service, variable, got, want)
	}
}

func configRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve config contract source path")
	}
	return filepath.Join(filepath.Dir(filename), "..", "..")
}

func readConfigContractFile(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(contents)
}
