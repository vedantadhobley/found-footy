// matchday_status_contract_test.go pins the operator report's read-only scope.
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

func matchdayStatusPath(t *testing.T, name string) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Join(filepath.Dir(filename), "..", "scripts", name)
}

func TestMatchdayStatusScriptIsScopedAndValid(t *testing.T) {
	path := matchdayStatusPath(t, "matchday-status.sh")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read status script: %v", err)
	}
	script := string(contents)
	for _, required := range []string{
		`dev|prod`,
		`docker-compose.$ENVIRONMENT.yml`,
		`found-footy.fleet.scope=$SCOPE`,
		`ps -q postgres`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("status script missing scope guard %q", required)
		}
	}
	if output, err := exec.Command("bash", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, output)
	}
}

func TestMatchdayStatusSQLIsReadOnly(t *testing.T) {
	contents, err := os.ReadFile(matchdayStatusPath(t, "matchday-status.sql"))
	if err != nil {
		t.Fatalf("read status SQL: %v", err)
	}
	sql := string(contents)
	if !strings.Contains(sql, "BEGIN READ ONLY;") {
		t.Fatal("status SQL must open an explicit read-only transaction")
	}
	mutation := regexp.MustCompile(`(?im)^\s*(insert|update|delete|alter|drop|truncate|create|grant|revoke)\b`)
	if found := mutation.FindString(sql); found != "" {
		t.Fatalf("status SQL contains mutation statement %q", strings.TrimSpace(found))
	}
}
