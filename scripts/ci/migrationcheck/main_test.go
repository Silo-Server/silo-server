package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitTest(t *testing.T, args ...string) string {
	t.Helper()
	// Keep background writers out of fixtures, including the checker's Git calls.
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("GIT_CONFIG_COUNT", "3")
	t.Setenv("GIT_CONFIG_KEY_0", "maintenance.auto")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")
	t.Setenv("GIT_CONFIG_KEY_1", "gc.auto")
	t.Setenv("GIT_CONFIG_VALUE_1", "0")
	t.Setenv("GIT_CONFIG_KEY_2", "core.fsmonitor")
	t.Setenv("GIT_CONFIG_VALUE_2", "false")
	cmd := exec.CommandContext(context.Background(), "git", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeTest(t *testing.T, file, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGitDoesNotStartMaintenance(t *testing.T) {
	t.Chdir(t.TempDir())
	// Keep injected maintenance synchronous so a regression cannot race cleanup.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "maintenance.autoDetach")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")
	t.Setenv("GIT_CONFIG_PARAMETERS", "'maintenance.auto=true'")
	t.Setenv("GIT_TRACE", "1")
	gitTest(t, "init", "-q")
	writeTest(t, "README", "fixture\n")
	gitTest(t, "add", ".")
	out := gitTest(t, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "base")
	if strings.Contains(out, "maintenance run") || strings.Contains(out, "gc --auto") {
		t.Fatalf("git commit started maintenance; want no maintenance subprocess:\n%s", out)
	}
}

func baseTest(t *testing.T) string {
	t.Helper()
	t.Chdir(t.TempDir())
	gitTest(t, "init", "-q")
	writeTest(t, "migrations/sql/00001_existing.sql", "-- +goose Up\nSELECT 1;\n")
	gitTest(t, "add", ".")
	gitTest(t, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "base")
	return gitTest(t, "rev-parse", "HEAD")
}

func TestMigrationChanges(t *testing.T) {
	for _, change := range []string{"unchanged", "add", "modify", "delete", "rename", "setting-key", "multiline-key", "value-update", "missing-base"} {
		t.Run(change, func(t *testing.T) {
			base := baseTest(t)
			file := "migrations/sql/00001_existing.sql"
			switch change {
			case "add":
				writeTest(t, "migrations/sql/20261015000000_new.sql", "-- +goose Up\nSELECT 2;\n")
			case "modify":
				writeTest(t, file, "-- +goose Up\nSELECT 2;\n")
			case "delete":
				gitTest(t, "rm", file)
			case "rename":
				gitTest(t, "mv", file, "migrations/sql/00002_renamed.sql")
			case "setting-key":
				writeTest(t, "migrations/sql/new.sql", "UPDATE server_settings SET key = 'new' WHERE key = 'old';")
			case "multiline-key":
				writeTest(t, "migrations/sql/new.sql", "UPDATE public.\"server_settings\"\n SET value = 'value',\n \"key\" = 'new' WHERE key = 'old';")
			case "value-update":
				writeTest(t, "migrations/sql/new.sql", "UPDATE server_settings SET value = 'value' WHERE key = 'old';")
			case "missing-base":
				base = "missing-base"
			}
			err := run([]string{"-base", base})
			wantFailure := change != "unchanged" && change != "add" && change != "value-update"
			if (err != nil) != wantFailure {
				t.Fatalf("%s: run = %v; want failure %t", change, err, wantFailure)
			}
			if change == "add" || change == "setting-key" {
				gitTest(t, "add", ".")
				gitTest(t, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "head")
				err = run([]string{"-base", base})
				if (err != nil) != wantFailure {
					t.Fatalf("committed %s: run = %v; want failure %t", change, err, wantFailure)
				}
			}
		})
	}
}

func TestSettingKeyWrites(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		fail      bool
	}{
		{"single quoted DO", "DO 'BEGIN UPDATE server_settings SET key = ''new'' WHERE key = ''old''; END';", true},
		{"single quoted EXECUTE", "EXECUTE 'UPDATE server_settings SET key = ''new'' WHERE key = ''old'';';", true},
		{"single quoted function", "CREATE FUNCTION rename_setting() RETURNS void LANGUAGE plpgsql AS 'BEGIN UPDATE server_settings SET key = ''new'' WHERE key = ''old''; END';", true},
		{"escape quoted DO", "DO E'BEGIN UPDATE SERVER_SETTINGS SET key = ''new'' WHERE key = ''old''; END';", true},
		{"literal protected name", "SELECT 'server_settings';", true},
		{"plain value literal", "UPDATE server_settings SET value = 'DO UPDATE unrelated SET key = ''new'';' WHERE key = 'old';", false},
		{"comment before key", "UPDATE server_settings SET /* rename */ key = 'new' WHERE key = 'old';", true},
		{"comment after key", "UPDATE server_settings SET key /* rename */ = 'new' WHERE key = 'old';", true},
		{"line comments", "UPDATE server_settings SET -- rename\n key -- target\n = 'new' WHERE key = 'old';", true},
		{"nested comment", "UPDATE server_settings SET /* outer /* inner */ outer */ key = 'new' WHERE key = 'old';", true},
		{"only", "UPDATE ONLY server_settings SET key = 'new' WHERE key = 'old';", true},
		{"alias", "UPDATE server_settings AS s SET key = 'new' WHERE s.key = 'old';", true},
		{"bare alias", "UPDATE server_settings s SET key = 'new' WHERE s.key = 'old';", true},
		{"quoted where", "UPDATE server_settings SET value = 'WHERE key = ''old'';', key = 'new' WHERE key = 'old';", true},
		{"escaped quote", `UPDATE server_settings SET value = E'quote\';', key = 'new' WHERE key = 'old';`, true},
		{"dollar quote", "UPDATE server_settings SET value = $text$WHERE;$text$, key = 'new' WHERE key = 'old';", true},
		{"tuple assignment", "UPDATE server_settings SET (value, key) = ('value', 'new') WHERE key = 'old';", true},
		{"insert select", "INSERT INTO server_settings (key, value) SELECT 'new', value FROM server_settings WHERE key = 'old';", true},
		{"insert table", "INSERT INTO server_settings TABLE server_settings;", true},
		{"086 secret copies", `INSERT INTO server_settings (key, value)
SELECT 's3.public_secret_key', value FROM server_settings WHERE key = 's3.operational_secret_key'
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;

INSERT INTO server_settings (key, value)
SELECT 's3.private_secret_key', value FROM server_settings WHERE key = 's3.operational_secret_key'
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;`, true},
		{"quoted schema copy", `INSERT INTO "public"."server_settings" (key, value) SELECT 'new', value FROM "public"."server_settings" WHERE key = 'old';`, true},
		{"conflict key assignment", "INSERT INTO server_settings (key, value) VALUES ('new', 'value') ON CONFLICT (key) DO UPDATE SET key = 'other';", true},
		{"value update", "UPDATE server_settings SET value = 'value' WHERE key = 'old';", false},
		{"commented value update", "UPDATE ONLY public.server_settings AS s SET /* value */ value = 'value' WHERE s.key = 'old';", false},
		{"quoted SQL value", "UPDATE server_settings SET value = 'WHERE key = ''new''; -- /*' WHERE key = 'old';", false},
		{"escaped value update", `UPDATE server_settings SET value = E'quote\';' WHERE key = 'old';`, false},
		{"literal insert", "INSERT INTO server_settings (key, value) VALUES ('new', 'value');", false},
		{"literal upsert", "INSERT INTO server_settings (key, value) VALUES ('new', 'value') ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;", false},
		{"commented rename", "/* UPDATE server_settings SET key = 'new'; */ SELECT 1; -- UPDATE server_settings SET key = 'new';", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := baseTest(t)
			writeTest(t, "migrations/sql/new.sql", "-- +goose Up\n"+tc.sql)
			err := run([]string{"-base", base})
			if (err != nil) != tc.fail {
				t.Fatalf("run = %v; want failure %t", err, tc.fail)
			}
			if tc.fail && !strings.Contains(err.Error(), "server_settings key rename") {
				t.Fatalf("wanted key rename rejection, got %v", err)
			}
		})
	}
}

func TestV1FreezeTarget(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"unchanged", "reorder", "v2", "v1-add", "v1-method", "v1-delete", "missing-base", "invalid-json"} {
		t.Run(change, func(t *testing.T) {
			baseTest(t)
			path := "contracts/api/v2/route-inventory.json"
			before := `{"routes":[{"listener":"api","namespace":"api_v1","method":"GET","path":"/api/v1/items"},{"listener":"api","namespace":"api_v2","method":"GET","path":"/api/v2/items"}]}`
			writeTest(t, path, before)
			gitTest(t, "add", ".")
			gitTest(t, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "inventory")
			base := gitTest(t, "rev-parse", "HEAD")
			switch change {
			case "reorder":
				writeTest(t, path, `{"routes":[{"listener":"api","namespace":"api_v2","method":"GET","path":"/api/v2/items"},{"listener":"api","namespace":"api_v1","method":"GET","path":"/api/v1/items"}]}`)
			case "v2":
				writeTest(t, path, strings.ReplaceAll(before, "/api/v2/items", "/api/v2/other"))
			case "v1-add":
				writeTest(t, path, strings.ReplaceAll(before, `"routes":[`, `"routes":[{"listener":"api","namespace":"api_v1","method":"POST","path":"/api/v1/items"},`))
			case "v1-method":
				writeTest(t, path, strings.Replace(before, "GET", "POST", 1))
			case "v1-delete":
				writeTest(t, path, `{"routes":[{"listener":"api","namespace":"api_v2","method":"GET","path":"/api/v2/items"}]}`)
			case "missing-base":
				base = "missing-base"
			case "invalid-json":
				writeTest(t, path, "{")
			}
			cmd := exec.CommandContext(context.Background(), "make", "-f", filepath.Join(root, "Makefile"), "verify-v1-route-freeze", "BASE_REF="+base)
			out, err := cmd.CombinedOutput()
			wantFailure := change != "unchanged" && change != "reorder" && change != "v2"
			if (err != nil) != wantFailure {
				t.Fatalf("%s: make = %v, %s; want failure %t", change, err, out, wantFailure)
			}
			if wantFailure && change != "missing-base" && change != "invalid-json" && !strings.Contains(string(out), "v1 routes changed") {
				t.Fatalf("%s: wanted v1 rejection, got %s", change, out)
			}
		})
	}
}
