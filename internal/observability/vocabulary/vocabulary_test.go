// Tests asserting every declared Module + Action label is registered.
package vocabulary

import "testing"

func TestValidModules_NoDuplicates(t *testing.T) {
	seen := make(map[Module]bool)
	for _, m := range ValidModules {
		if seen[m] {
			t.Errorf("duplicate Module in ValidModules: %q", m)
		}
		seen[m] = true
	}
}

func TestValidActions_NoDuplicates(t *testing.T) {
	seen := make(map[Action]bool)
	for _, a := range ValidActions {
		if seen[a] {
			t.Errorf("duplicate Action in ValidActions: %q", a)
		}
		seen[a] = true
	}
}

func TestModules_StringValuesMatchExpected(t *testing.T) {
	// Spot-check a few strings so a refactor that accidentally changes
	// the wire value gets caught (Loki queries + Grafana dashboards
	// depend on these string values).
	cases := map[Module]string{
		ModuleEventWorkflow: "discovery_workflow",
		ModuleInfraNATS:     "infra_nats",
		ModuleAPI:           "api",
		ModuleDeploy:        "deploy",
	}
	for m, want := range cases {
		if string(m) != want {
			t.Errorf("Module %v: wire value %q, want %q", m, string(m), want)
		}
	}
}

func TestDeployActions_Registered(t *testing.T) {
	// Every action declared in actions_deploy.go must appear in ValidActions
	// (via the init() registerActions call).
	want := []Action{
		ActionStartup,
		ActionShutdown,
		ActionShutdownForced,
		ActionConfigLoaded,
		ActionConfigLoadError,
	}
	set := make(map[Action]bool)
	for _, a := range ValidActions {
		set[a] = true
	}
	for _, a := range want {
		if !set[a] {
			t.Errorf("Action %q not in ValidActions — did actions_deploy.go init() register it?", a)
		}
	}
}
