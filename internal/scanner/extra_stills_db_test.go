package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/imageutil"
)

type fakeExtraStillCacher struct {
	fakeScannerCoverCacher
	stills []string // contents of each cached extra image
}

func (f *fakeExtraStillCacher) CacheEbookCover(context.Context, []byte, string) (string, string, error) {
	return "", "", nil
}

func (f *fakeExtraStillCacher) CacheExtraStill(_ context.Context, data []byte, extraID string) (string, string, error) {
	f.stills = append(f.stills, string(data))
	if string(data) == "not an image" {
		// The error chain imagecache.CacheBytes returns for these bytes.
		_, err := imageutil.Thumbhash(data)
		return "", "", fmt.Errorf("imagecache: thumbhash: %w", err)
	}
	return "local/extras/" + extraID + "/still/original." + string(data) + ".webp", "hash-" + string(data), nil
}

// A sidecar image beside an extra becomes its still on the next scan, is read
// again only when it changes, and is cleared when it is removed.
func TestScanSyncsExtraSidecarStill(t *testing.T) {
	ctx := t.Context()
	fx := seedLateExtrasFixture(ctx, t)
	extra := filepath.Join(fx.extrasDir, "Making Of.mkv")
	writeTestFile(t, extra, "extra")
	image := filepath.Join(fx.extrasDir, "Making Of-thumb.jpg")
	writeTestFile(t, image, "first")
	extraID := contentid.ForLocal(extra)

	cacher := &fakeExtraStillCacher{}
	scanner := NewScanner(NewFileRepository(fx.pool), "", nil, 1, false, 0)
	scanner.SetImageCacher(cacher)
	scan := func() {
		t.Helper()
		if _, err := scanner.ScanFolder(ctx, fx.folder); err != nil {
			t.Fatalf("ScanFolder: %v", err)
		}
	}
	readStill := func() (path, source, thumbhash string) {
		t.Helper()
		if err := fx.pool.QueryRow(ctx, `
			SELECT still_path, still_source, still_thumbhash FROM media_extras WHERE content_id = $1`, extraID).
			Scan(&path, &source, &thumbhash); err != nil {
			t.Fatalf("read extra still: %v", err)
		}
		return path, source, thumbhash
	}

	scan()
	path, source, thumbhash := readStill()
	if want := "local/extras/" + extraID + "/still/original.first.webp"; path != want || thumbhash != "hash-first" {
		t.Fatalf("still = %q %q, want %q hash-first", path, thumbhash, want)
	}
	if !strings.HasPrefix(source, "file://"+image+"?v=") {
		t.Fatalf("still_source = %q, want the sidecar's file:// URL", source)
	}
	listed, err := catalog.NewExtraRepository(fx.pool).ListWithFilesByParentID(ctx, fx.movieID)
	if err != nil || len(listed) != 1 || listed[0].StillPath != path || listed[0].StillThumbhash != thumbhash {
		t.Fatalf("listed extras = %+v, %v; want the cached still", listed, err)
	}

	scan()
	if len(cacher.stills) != 1 {
		t.Fatalf("unchanged sidecar cached again: %v", cacher.stills)
	}

	writeTestFile(t, image, "second!")
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(image, later, later); err != nil {
		t.Fatal(err)
	}
	scan()
	if path, _, _ := readStill(); !strings.HasSuffix(path, "original.second!.webp") || len(cacher.stills) != 2 {
		t.Fatalf("replaced sidecar: still = %q, cached %v", path, cacher.stills)
	}

	if err := os.Remove(image); err != nil {
		t.Fatal(err)
	}
	scan()
	if path, source, thumbhash := readStill(); path != "" || source != "" || thumbhash != "" {
		t.Fatalf("removed sidecar left still %q %q %q", path, source, thumbhash)
	}
}

// An image that cannot be decoded is recorded without a still and read again
// only once it changes.
func TestScanRecordsUndecodableExtraStill(t *testing.T) {
	ctx := t.Context()
	fx := seedLateExtrasFixture(ctx, t)
	extra := filepath.Join(fx.extrasDir, "Making Of.mkv")
	writeTestFile(t, extra, "extra")
	writeTestFile(t, filepath.Join(fx.extrasDir, "Making Of-thumb.jpg"), "not an image")
	cacher := &fakeExtraStillCacher{}
	scanner := NewScanner(NewFileRepository(fx.pool), "", nil, 1, false, 0)
	scanner.SetImageCacher(cacher)
	for range 2 {
		if _, err := scanner.ScanFolder(ctx, fx.folder); err != nil {
			t.Fatalf("ScanFolder: %v", err)
		}
	}
	var path, source string
	if err := fx.pool.QueryRow(ctx, `SELECT still_path, still_source FROM media_extras WHERE content_id = $1`,
		contentid.ForLocal(extra)).Scan(&path, &source); err != nil {
		t.Fatal(err)
	}
	if path != "" || !strings.HasPrefix(source, "file://") || len(cacher.stills) != 1 {
		t.Fatalf("still = %q from %q after %d reads; want the source recorded once without a still", path, source, len(cacher.stills))
	}
}

// Deleting an extra's last file row clears its image so the revision GC
// collects it; an extra with another file keeps its image.
func TestDeletingAnExtrasLastFileClearsItsStill(t *testing.T) {
	ctx := t.Context()
	fx := seedLateExtrasFixture(ctx, t)
	only := filepath.Join(fx.extrasDir, "Making Of.mkv")
	shared := filepath.Join(fx.extrasDir, "Interview.mkv")
	for _, video := range []string{only, shared} {
		writeTestFile(t, video, "extra")
		writeTestFile(t, strings.TrimSuffix(video, ".mkv")+"-thumb.jpg", "img")
	}
	files := NewFileRepository(fx.pool)
	scanner := NewScanner(files, "", nil, 1, false, 0)
	scanner.SetImageCacher(&fakeExtraStillCacher{})
	if _, err := scanner.ScanFolder(ctx, fx.folder); err != nil {
		t.Fatalf("ScanFolder: %v", err)
	}
	fileOf := func(video string) int {
		t.Helper()
		var id int
		if err := fx.pool.QueryRow(ctx, `SELECT id FROM media_files WHERE file_path = $1`, video).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	stillOf := func(video string) string {
		t.Helper()
		var path string
		if err := fx.pool.QueryRow(ctx, `SELECT still_path FROM media_extras WHERE content_id = $1`,
			contentid.ForLocal(video)).Scan(&path); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// A second file row on the shared extra (another version of it).
	if _, err := fx.pool.Exec(ctx, `
		INSERT INTO media_files (media_folder_id, file_path, file_size, extra_id)
		VALUES ($1, $2, 1, $3)`, fx.folder.ID, shared+".copy", contentid.ForLocal(shared)); err != nil {
		t.Fatal(err)
	}

	if _, err := files.DeleteByIDs(ctx, []int{fileOf(only), fileOf(shared)}); err != nil {
		t.Fatal(err)
	}
	if got := stillOf(only); got != "" {
		t.Fatalf("extra without files kept still %q", got)
	}
	if got := stillOf(shared); got == "" {
		t.Fatal("extra with another file lost its still")
	}

	if _, err := fx.pool.Exec(ctx, `UPDATE media_files SET missing_since = now() - interval '1 hour' WHERE file_path = $1`, shared+".copy"); err != nil {
		t.Fatal(err)
	}
	if deleted, err := files.DeleteMissingByFolder(ctx, fx.folder.ID, 0, nil); err != nil || deleted != 1 {
		t.Fatalf("DeleteMissingByFolder = %d, %v; want 1", deleted, err)
	}
	if got := stillOf(shared); got != "" {
		t.Fatalf("extra whose last file was purged kept still %q", got)
	}
}
