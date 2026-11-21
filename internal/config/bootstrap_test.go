package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDatabaseURLNeedsNoSecretKey(t *testing.T) {
	t.Setenv("DATABASE_URL_FILE", "")
	t.Setenv("SECRET_KEY_FILE", "")
	t.Setenv("DATABASE_URL", "postgres://example.invalid/silo")
	t.Setenv("SECRET_KEY", "")
	url, err := LoadDatabaseURL("")
	if err != nil || url != "postgres://example.invalid/silo" {
		t.Fatalf("LoadDatabaseURL = %q, %v", url, err)
	}
	// The server itself still refuses to start without the key.
	if _, err := LoadBootstrap(""); err == nil {
		t.Fatal("LoadBootstrap accepted a missing SECRET_KEY")
	}

	t.Setenv("DATABASE_URL", "")
	if _, err := LoadDatabaseURL(""); err == nil {
		t.Fatal("LoadDatabaseURL accepted a missing DATABASE_URL")
	}
}

// Docker and Kubernetes mount secrets as files. Each bootstrap secret can be
// read from the file its _FILE variant names, less one line ending.
func TestLoadBootstrapReadsSecretFiles(t *testing.T) {
	dir := t.TempDir()
	file := func(name, content string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	const key = "0123456789abcdef0123456789abcdef-at-rest"
	for _, name := range []string{"DATABASE_URL", "SECRET_KEY", "REDIS_URL", "MODE"} {
		t.Setenv(name, "")
	}
	t.Setenv("DATABASE_URL_FILE", file("database-url", "postgres://example.invalid/silo\n"))
	t.Setenv("SECRET_KEY_FILE", file("secret-key", key+"\r\n"))
	t.Setenv("REDIS_URL_FILE", file("redis-url", "redis://example.invalid:6379/2"))

	bc, err := LoadBootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	if bc.DatabaseURL != "postgres://example.invalid/silo" || string(bc.SecretKey) != key || bc.RedisURL != "redis://example.invalid:6379/2" || bc.RedisURLVar != "REDIS_URL_FILE" {
		t.Fatalf("bootstrap = %q, %q, %q (%q); want the files' values less one line ending and REDIS_URL_FILE", bc.DatabaseURL, bc.SecretKey, bc.RedisURL, bc.RedisURLVar)
	}

	// A short secret in SECRET_KEY_FILE names SECRET_KEY_FILE.
	t.Setenv("SECRET_KEY_FILE", file("short-key", "too-short\n"))
	if _, err := LoadBootstrap(""); err == nil || !strings.Contains(err.Error(), "SECRET_KEY_FILE") {
		t.Fatalf("short SECRET_KEY_FILE: error = %v, want one naming SECRET_KEY_FILE", err)
	}
	t.Setenv("SECRET_KEY_FILE", file("secret-key", key+"\r\n"))

	// A secret set both ways is refused rather than guessed at.
	t.Setenv("SECRET_KEY", key)
	if _, err := LoadBootstrap(""); err == nil || !strings.Contains(err.Error(), "SECRET_KEY_FILE") {
		t.Fatalf("SECRET_KEY and SECRET_KEY_FILE both set: error = %v, want one naming both", err)
	}
	t.Setenv("SECRET_KEY", "")

	// A file that holds nothing, or is not there, stops startup instead of
	// leaving the secret unset.
	for name, path := range map[string]string{"empty": file("empty", "\n"), "blank": file("blank", "  \n"), "missing": filepath.Join(dir, "missing")} {
		t.Setenv("DATABASE_URL_FILE", path)
		if _, err := LoadDatabaseURL(""); err == nil || !strings.Contains(err.Error(), "DATABASE_URL_FILE") {
			t.Fatalf("%s file: error = %v, want one naming DATABASE_URL_FILE", name, err)
		}
	}
}
