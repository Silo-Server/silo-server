package backup

import (
	"fmt"
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/backup/backuptest"
)

// TestMain drops the template database the dump and restore tests copy, and
// points TMPDIR at a directory it removes afterwards: every restore set read
// writes its account documents under os.TempDir().
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "silo-backup-test-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "backup tests: %v\n", err)
		os.Exit(1)
	}
	if err := os.Setenv("TMPDIR", dir); err != nil {
		fmt.Fprintf(os.Stderr, "backup tests: %v\n", err)
		os.Exit(1)
	}
	backuptest.Main(m, func() { _ = os.RemoveAll(dir) })
}
