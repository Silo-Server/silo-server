package config

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/joho/godotenv"
)

// scripts/init-dev-env.sh writes the .env that LoadBootstrap reads with
// godotenv. Its single-quoted values must come back byte for byte, and a
// password neither godotenv nor Compose can represent must be refused before
// any file is written.
func TestInitDevEnvOutputLoadsWithGodotenv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("init-dev-env.sh is a POSIX shell script")
	}
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("init-dev-env.sh needs openssl")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "init-dev-env.sh"))
	if err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, password string) (string, string, error) {
		t.Helper()
		envFile := filepath.Join(t.TempDir(), ".env")
		cmd := exec.Command("sh", script, envFile)
		cmd.Env = append(os.Environ(), "POSTGRES_PASSWORD="+password)
		output, err := cmd.CombinedOutput()
		return envFile, string(output), err
	}

	for name, password := range map[string]string{
		"literal dollar":     "compose-test$literal",
		"inner backslashes":  `a\b\\c`,
		"url reserved bytes": `a@b#c/d%e?f:g "h\i ü`,
	} {
		t.Run(name, func(t *testing.T) {
			envFile, output, err := run(t, password)
			if err != nil {
				t.Fatalf("init-dev-env.sh: %v\n%s", err, output)
			}
			values, err := godotenv.Read(envFile)
			if err != nil {
				t.Fatalf("godotenv rejected the generated file: %v", err)
			}
			if got := values["POSTGRES_PASSWORD"]; got != password {
				t.Fatalf("POSTGRES_PASSWORD = %q, want %q", got, password)
			}
			databaseURL, err := url.Parse(values["DATABASE_URL"])
			if err != nil {
				t.Fatalf("DATABASE_URL: %v", err)
			}
			if got, _ := databaseURL.User.Password(); got != password {
				t.Fatalf("DATABASE_URL password = %q, want %q", got, password)
			}
		})
	}

	for name, password := range map[string]string{
		"trailing backslash":        `abc\`,
		"trailing double backslash": `abc\\`,
		"single quote":              "it's",
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			envFile, output, err := run(t, password)
			if err == nil {
				t.Fatalf("init-dev-env.sh accepted %q:\n%s", password, output)
			}
			if !strings.Contains(output, "POSTGRES_PASSWORD cannot") {
				t.Fatalf("output = %q, want a POSTGRES_PASSWORD error", output)
			}
			if _, err := os.Stat(envFile); !os.IsNotExist(err) {
				t.Fatalf("rejected password still wrote %s (stat err %v)", envFile, err)
			}
		})
	}
}
