package transcodenode

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/playback"
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

// The plan records the subtitle source version from the API server's stat,
// and playback compares it with a stat on the node. On a node whose mount
// dates the file differently, the granted version is restated at the node's
// time, or every on-demand subtitle segment would come back empty; a plan
// that read another version matches no file.
func TestRenditionSourceOnNode(t *testing.T) {
	granted := time.Unix(1_700_000_000, 123_456_100)
	input := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	source := datedSource{size: 10, granted: granted, input: input}

	// The plan's stat and the grant's may differ below a microsecond.
	planned := playback.SubtitleRenditionSource{Size: 10, ModTimeUnixNano: granted.Add(500 * time.Nanosecond).UnixNano()}
	if got := renditionSourceOnNode(planned, source); got.Size != 10 || got.ModTimeUnixNano != input.UnixNano() {
		t.Fatalf("granted version = %+v, want it at the node's %d", got, input.UnixNano())
	}

	// A plan that read another version must not match, even when its time is
	// the one this node's mount gives the granted file.
	other := playback.SubtitleRenditionSource{Size: 10, ModTimeUnixNano: input.UnixNano()}
	if got := renditionSourceOnNode(other, source); got != unmatchedRenditionSource {
		t.Fatalf("another version = %+v, want a version no file has", got)
	}
	resized := playback.SubtitleRenditionSource{Size: 11, ModTimeUnixNano: granted.UnixNano()}
	if got := renditionSourceOnNode(resized, source); got != unmatchedRenditionSource {
		t.Fatalf("another size = %+v, want a version no file has", got)
	}
	if got := renditionSourceOnNode(playback.SubtitleRenditionSource{}, source); !got.IsZero() {
		t.Fatalf("unrecorded version = %+v, want it unrecorded", got)
	}
}

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
