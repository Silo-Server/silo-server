package rootcheck

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProbeReachableDirectory(t *testing.T) {
	t.Parallel()

	res := Probe(t.TempDir())
	if !res.Reachable {
		t.Fatalf("Probe(temp dir) = %+v, want reachable", res)
	}
	if res.ErrorCode != "" || res.ErrorMessage != "" {
		t.Fatalf("Probe(temp dir) error fields = %q/%q, want empty", res.ErrorCode, res.ErrorMessage)
	}
}

func TestProbeMissingPath(t *testing.T) {
	t.Parallel()

	res := Probe(filepath.Join(t.TempDir(), "does-not-exist"))
	if res.Reachable {
		t.Fatal("Probe(missing path) reported reachable")
	}
	if res.ErrorCode != ErrCodeNotFound {
		t.Fatalf("ErrorCode = %q, want %q", res.ErrorCode, ErrCodeNotFound)
	}
}

func TestProbeRegularFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	res := Probe(path)
	if res.Reachable {
		t.Fatal("Probe(regular file) reported reachable")
	}
	if res.ErrorCode != ErrCodeNotDirectory {
		t.Fatalf("ErrorCode = %q, want %q", res.ErrorCode, ErrCodeNotDirectory)
	}
}

func TestProbeUnreadableDirectory(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("running as root; permission bits are not enforced")
	}

	path := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(path, 0o000); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o755) })

	res := Probe(path)
	if res.Reachable {
		t.Fatal("Probe(unreadable dir) reported reachable")
	}
	if res.ErrorCode != ErrCodePermissionDenied {
		t.Fatalf("ErrorCode = %q, want %q", res.ErrorCode, ErrCodePermissionDenied)
	}
}
