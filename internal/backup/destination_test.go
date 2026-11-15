package backup

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

// A backup on a local destination is readable by this account only.
func TestStoreDestinationWritesOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	store, err := blobstore.NewFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	dest := NewStoreDestination(store)
	key := objectKey("server-1", "2026-11-01T040000Z-nightly", DumpName)
	if err := dest.Put(t.Context(), key, strings.NewReader("dump")); err != nil {
		t.Fatal(err)
	}
	want := map[string]fs.FileMode{
		"backups":          0o700,
		"backups/server-1": 0o700,
		"backups/server-1/2026-11-01T040000Z-nightly": 0o700,
		key: 0o600,
	}
	for name, mode := range want {
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != mode {
			t.Errorf("%s mode = %o, want %o", name, got, mode)
		}
	}
}
