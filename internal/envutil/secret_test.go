package envutil

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSecretFileEdgeCases(t *testing.T) {
	dir := t.TempDir()

	// Only one trailing line ending is removed; a second stays in the value.
	doubleLF := filepath.Join(dir, "double-lf")
	if err := os.WriteFile(doubleLF, []byte("secret\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSecretFile(doubleLF)
	if err != nil || got != "secret\n" {
		t.Fatalf("double LF = %q, %v; want %q", got, err, "secret\n")
	}

	// Non-regular files (directories) are refused.
	if _, err := ReadSecretFile(dir); err == nil {
		t.Fatal("ReadSecretFile accepted a directory")
	}

	// Non-UTF-8 content is refused.
	badUTF8 := filepath.Join(dir, "bad-utf8")
	if err := os.WriteFile(badUTF8, []byte{0xff, 0xfe, '\n'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSecretFile(badUTF8); err == nil {
		t.Fatal("ReadSecretFile accepted invalid UTF-8")
	}

	// Files larger than 1 MiB are refused without quoting content.
	oversized := filepath.Join(dir, "oversized")
	if err := os.WriteFile(oversized, bytes.Repeat([]byte("a"), maxFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSecretFile(oversized); err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("oversized file = %v, want 1 MiB limit error", err)
	}
}
