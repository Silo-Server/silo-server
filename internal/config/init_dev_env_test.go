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

	// A failing openssl must stop the script rather than write an empty
	// SECRET_KEY, and no partial or temporary file may be left behind.
	t.Run("openssl failure writes nothing", func(t *testing.T) {
		dir := t.TempDir()
		bin := filepath.Join(dir, "bin")
		if err := os.Mkdir(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, "openssl"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		envFile := filepath.Join(dir, "out", ".env")
		if err := os.Mkdir(filepath.Dir(envFile), 0o755); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", script, envFile)
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "POSTGRES_PASSWORD=existing")
		if output, err := cmd.CombinedOutput(); err == nil {
			t.Fatalf("init-dev-env.sh succeeded with a failing openssl:\n%s", output)
		}
		entries, err := os.ReadDir(filepath.Dir(envFile))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("failed run left files behind: %v", entries)
		}
	})

	t.Run("refuses an existing file and leaves no temporary file", func(t *testing.T) {
		envFile, output, err := run(t, "existing")
		if err != nil {
			t.Fatalf("init-dev-env.sh: %v\n%s", err, output)
		}
		before, err := os.ReadFile(envFile)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", script, envFile)
		cmd.Env = append(os.Environ(), "POSTGRES_PASSWORD=other")
		if output, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(output), "already exists") {
			t.Fatalf("second run: err %v output %q, want an already-exists refusal", err, output)
		}
		after, err := os.ReadFile(envFile)
		if err != nil || string(after) != string(before) {
			t.Fatalf("second run changed %s (err %v)", envFile, err)
		}
		entries, err := os.ReadDir(filepath.Dir(envFile))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Fatalf("directory = %v, want only .env", entries)
		}
	})
}
