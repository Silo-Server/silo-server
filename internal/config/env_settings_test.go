package config

import (
	"strings"
	"testing"
)

func TestReadEnvSettings(t *testing.T) {
	env := map[string]string{
		"SILO_LOG_LEVEL":          " WARN ",
		"SILO_SERVER_NAME":        "   ",
		"SILO_DB_MAX_CONNECTIONS": "40",
	}
	got, err := ReadEnvSettings(func(name string) string { return env[name] })
	if err != nil {
		t.Fatal(err)
	}
	if stored := got.Values(EnvScopeStored); len(stored) != 1 || stored["server.log_level"] != "warn" {
		t.Errorf("stored = %v, want only server.log_level normalized to warn (a blank variable sets nothing)", stored)
	}
	if process := got.Values(EnvScopeProcess); len(process) != 1 || process["database.max_connections"] != "40" {
		t.Errorf("process = %v, want database.max_connections 40", process)
	}

	env = map[string]string{"SILO_DB_MAX_CONNECTIONS": "0"}
	if _, err := ReadEnvSettings(func(name string) string { return env[name] }); err == nil || !strings.Contains(err.Error(), "SILO_DB_MAX_CONNECTIONS") {
		t.Errorf("invalid value: error = %v, want one naming SILO_DB_MAX_CONNECTIONS", err)
	}
}

// Two rows for one key or one variable would leave one of them silently
// ignored, or a key locked under the wrong name.
func TestEnvSettingVarsAreUnique(t *testing.T) {
	vars, keys := map[string]bool{}, map[string]bool{}
	for _, v := range envSettingVars {
		if vars[v.Var] || keys[v.Key] {
			t.Errorf("%s (%s) is listed twice", v.Var, v.Key)
		}
		vars[v.Var], keys[v.Key] = true, true
	}
}
