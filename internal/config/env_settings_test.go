package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadEnvSettings(t *testing.T) {
	env := map[string]string{
		"SILO_LOG_LEVEL":          " WARN ",
		"SILO_SERVER_NAME":        "   ",
		"SILO_DB_MAX_CONNECTIONS": "40",
	}
	got, err := ReadEnvSettings(func(name string) string { return env[name] }, nil)
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
	if _, err := ReadEnvSettings(func(name string) string { return env[name] }, nil); err == nil || !strings.Contains(err.Error(), "SILO_DB_MAX_CONNECTIONS") {
		t.Errorf("invalid value: error = %v, want one naming SILO_DB_MAX_CONNECTIONS", err)
	}
}

// A secret setting can be read from the file its _FILE variant names, and
// then reports that variable as the one setting it. Other settings have no
// file variant.
func TestReadEnvSettingsReadsSecretFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "smtp-password")
	// Only one trailing line ending is removed; edge spaces and any second
	// newline stay part of the secret.
	if err := os.WriteFile(path, []byte(" hunter2 \n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"SILO_SMTP_PASSWORD_FILE": path, "SILO_PUBLIC_URL_FILE": path}
	secret := map[string]bool{"email.smtp_password": true}
	got, err := ReadEnvSettings(func(name string) string { return env[name] }, secret)
	if err != nil {
		t.Fatal(err)
	}
	if values := got.All(); len(values) != 1 || values["email.smtp_password"] != " hunter2 \n" {
		t.Fatalf("values = %v, want only email.smtp_password with one line ending removed", values)
	}
	if vars := got.Vars(); vars["email.smtp_password"] != "SILO_SMTP_PASSWORD_FILE" {
		t.Fatalf("vars = %v, want email.smtp_password set by SILO_SMTP_PASSWORD_FILE", vars)
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
