package transcodenode

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// datedSource is an open source the core granted at one modification time
// and this node's mount shows at another.
type datedSource struct {
	nodesource.Source
	size           int64
	granted, input time.Time
}

func (s datedSource) Size() int64             { return s.size }
func (s datedSource) ModTime() time.Time      { return s.granted }
func (s datedSource) InputModTime() time.Time { return s.input }

// A tone-mapped job carries the catalog's file revision, and playback rechecks
// its modification time against the node's file. On a node whose mount dates
// the file differently, the granted revision must pass that check.
func TestToneMapRevisionOnNodePassesTheNodeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(path, []byte("movie"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, input, input); err != nil {
		t.Fatal(err)
	}
	granted := time.Unix(1_700_000_000, 123_456_000)
	source := datedSource{size: 5, granted: granted, input: input}
	catalog := tonemap.SourceRevision{MediaFileID: 7, FileSize: 5, FileModifiedUnixNano: granted.UnixNano()}

	if err := catalog.ValidatePath(t.Context(), path); !errors.Is(err, tonemap.ErrSourceRevisionChanged) {
		t.Fatalf("the catalog's revision against the node's file = %v, want it refused", err)
	}
	if err := toneMapRevisionOnNode(catalog, source).ValidatePath(t.Context(), path); err != nil {
		t.Fatalf("the granted revision on the node = %v, want it accepted", err)
	}

	// A revision the core did not grant is refused, even when its time is the
	// one this node's mount gives the granted file.
	stale := catalog
	stale.FileModifiedUnixNano = input.UnixNano()
	if err := toneMapRevisionOnNode(stale, source).ValidatePath(t.Context(), path); !errors.Is(err, tonemap.ErrSourceRevisionChanged) {
		t.Fatalf("a revision the core did not grant = %v, want it refused", err)
	}
}
