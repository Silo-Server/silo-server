package artworkstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testRevision = "9f8e7d6c5b4a39281706f5e4d3c2b1a09f8e7d6c5b4a39281706f5e4d3c2b1a0"
	testKey      = "artwork/v1/objects/poster/9f/" + testRevision + "/original.webp"
	siblingKey   = "artwork/v1/objects/poster/9f/" + testRevision + "/w500.webp"
)

func newTestStore(t *testing.T) *FilesystemStore {
	t.Helper()
	store, err := NewFilesystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	return store
}

func mustWrite(t *testing.T, store *FilesystemStore, key string, data []byte) {
	t.Helper()
	if err := store.WriteImmutable(context.Background(), key, data, ObjectMetadata{MediaType: "image/webp"}); err != nil {
		t.Fatalf("WriteImmutable(%q): %v", key, err)
	}
}

func readObject(t *testing.T, store *FilesystemStore, key string) ([]byte, ObjectInfo) {
	t.Helper()
	object, err := store.Open(context.Background(), key)
	if err != nil {
		t.Fatalf("Open(%q): %v", key, err)
	}
	defer func() { _ = object.Close() }()
	data, err := io.ReadAll(object.Body)
	if err != nil {
		t.Fatalf("reading %q: %v", key, err)
	}
	return data, object.Info
}

func TestNewFilesystemStoreRejectsUnusableRoots(t *testing.T) {
	filesystemRoot := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	for _, root := range []string{"", "   ", "relative/path", filesystemRoot} {
		if _, err := NewFilesystemStore(root); err == nil {
			t.Errorf("NewFilesystemStore(%q) = nil error, want failure", root)
		}
	}
}

func TestWriteImmutableStoresObject(t *testing.T) {
	store := newTestStore(t)
	data := []byte("poster-bytes")
	mustWrite(t, store, testKey, data)

	got, info := readObject(t, store, testKey)
	if !bytes.Equal(got, data) {
		t.Fatalf("body = %q, want %q", got, data)
	}
	if info.Key != testKey {
		t.Errorf("Key = %q, want %q", info.Key, testKey)
	}
	if info.SizeBytes != int64(len(data)) {
		t.Errorf("SizeBytes = %d, want %d", info.SizeBytes, len(data))
	}
	if info.MediaType != "image/webp" {
		t.Errorf("MediaType = %q, want image/webp", info.MediaType)
	}
	if !strings.HasPrefix(info.ETag, `"`) || !strings.HasSuffix(info.ETag, `"`) || len(info.ETag) < 4 {
		t.Errorf("ETag = %q, want a quoted entity tag", info.ETag)
	}
	if info.ModTime.IsZero() {
		t.Error("ModTime is zero")
	}

	statInfo, err := store.Stat(context.Background(), testKey)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if statInfo != info {
		t.Errorf("Stat = %+v, want %+v", statInfo, info)
	}

	// The store is private to the server user; nothing else reads the tree.
	fileInfo, err := os.Stat(filepath.Join(store.Root(), filepath.FromSlash(testKey)))
	if err != nil {
		t.Fatalf("stat on disk: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != storeFilePerm {
		t.Errorf("object permissions = %o, want %o", perm, storeFilePerm)
	}
	dirInfo, err := os.Stat(filepath.Dir(filepath.Join(store.Root(), filepath.FromSlash(testKey))))
	if err != nil {
		t.Fatalf("stat directory on disk: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != storeDirPerm {
		t.Errorf("directory permissions = %o, want %o", perm, storeDirPerm)
	}
}

// TestWriteImmutableIsIdempotent proves a repeated write of identical bytes is
// a no-op: the pipeline re-materializes the same revision routinely and must
// not churn the object or leave temporary files behind.
func TestWriteImmutableIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	data := []byte("poster-bytes")
	mustWrite(t, store, testKey, data)

	diskPath := filepath.Join(store.Root(), filepath.FromSlash(testKey))
	before, err := os.Stat(diskPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	mustWrite(t, store, testKey, data)

	after, err := os.Stat(diskPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Error("second write replaced the stored object; want an untouched no-op")
	}
	assertNoTempFiles(t, store)
	assertDirEntries(t, filepath.Dir(diskPath), 1)
}

// TestWriteImmutableRejectsDifferentContent is the immutability guard: a key is
// a content address, so different bytes must never overwrite it.
func TestWriteImmutableRejectsDifferentContent(t *testing.T) {
	store := newTestStore(t)
	original := []byte("poster-bytes")
	mustWrite(t, store, testKey, original)

	err := store.WriteImmutable(context.Background(), testKey, []byte("different-bytes"), ObjectMetadata{})
	if !errors.Is(err, ErrContentMismatch) {
		t.Fatalf("WriteImmutable = %v, want ErrContentMismatch", err)
	}
	got, _ := readObject(t, store, testKey)
	if !bytes.Equal(got, original) {
		t.Fatalf("stored bytes = %q, want the original %q", got, original)
	}
	assertNoTempFiles(t, store)
}

func TestWriteImmutableRejectsEmptyData(t *testing.T) {
	store := newTestStore(t)
	if err := store.WriteImmutable(context.Background(), testKey, nil, ObjectMetadata{}); err == nil {
		t.Fatal("WriteImmutable(nil) = nil, want an error")
	}
	if _, err := store.Stat(context.Background(), testKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat = %v, want ErrNotFound", err)
	}
}

// TestWriteImmutableConcurrentSameKey exercises the case the cache queue
// actually produces: several workers materializing the same revision at once.
// Every writer must succeed and the object must be complete.
func TestWriteImmutableConcurrentSameKey(t *testing.T) {
	store := newTestStore(t)
	data := bytes.Repeat([]byte("poster"), 4096)

	const writers = 8
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- store.WriteImmutable(context.Background(), testKey, data, ObjectMetadata{})
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent WriteImmutable: %v", err)
		}
	}
	got, _ := readObject(t, store, testKey)
	if !bytes.Equal(got, data) {
		t.Fatalf("stored bytes differ from the written bytes (%d vs %d)", len(got), len(data))
	}
	assertNoTempFiles(t, store)
	assertDirEntries(t, filepath.Dir(filepath.Join(store.Root(), filepath.FromSlash(testKey))), 1)
}

// TestWriteImmutableConcurrentDifferentContent is the pathological case: two
// payloads racing for one key. Exactly one content may win, losers must see
// ErrContentMismatch, and the stored object must never be a blend of the two.
func TestWriteImmutableConcurrentDifferentContent(t *testing.T) {
	store := newTestStore(t)
	first := bytes.Repeat([]byte("a"), 8192)
	second := bytes.Repeat([]byte("b"), 8192)

	const writersPerPayload = 4
	start := make(chan struct{})
	errs := make(chan error, writersPerPayload*2)
	var wg sync.WaitGroup
	for i := 0; i < writersPerPayload; i++ {
		for _, payload := range [][]byte{first, second} {
			wg.Add(1)
			go func(data []byte) {
				defer wg.Done()
				<-start
				errs <- store.WriteImmutable(context.Background(), testKey, data, ObjectMetadata{})
			}(payload)
		}
	}
	close(start)
	wg.Wait()
	close(errs)

	succeeded := 0
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrContentMismatch):
		default:
			t.Fatalf("concurrent WriteImmutable = %v, want nil or ErrContentMismatch", err)
		}
	}
	if succeeded == 0 {
		t.Fatal("no writer succeeded")
	}
	got, _ := readObject(t, store, testKey)
	if !bytes.Equal(got, first) && !bytes.Equal(got, second) {
		t.Fatal("stored object matches neither payload; a write was torn")
	}
	assertNoTempFiles(t, store)
}

// TestWriteWhileGarbageCollectionPrunes covers the real interaction between
// materialization and reference-aware GC: deleting the last object in a
// revision directory prunes it, and a writer that loses its destination
// directory mid-write must recover instead of failing the cache job.
func TestWriteWhileGarbageCollectionPrunes(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	data := []byte("poster-bytes")

	// One materialization per round, each followed by the GC deleting that
	// revision — the deleter never spins, so a write only ever contends with a
	// prune that a real cleanup pass would also perform.
	const rounds = 100
	written := make(chan struct{}, 1)
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer close(written)
		for i := 0; i < rounds; i++ {
			if err := store.WriteImmutable(ctx, testKey, data, ObjectMetadata{}); err != nil {
				failures <- err
				return
			}
			written <- struct{}{}
		}
	}()
	go func() {
		defer wg.Done()
		for range written {
			if _, err := store.DeleteObjects(ctx, []string{testKey}); err != nil {
				failures <- err
				return
			}
		}
	}()
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("write/delete race: %v", err)
	}
	assertNoTempFiles(t, store)
}

func TestIsLinkUnsupported(t *testing.T) {
	if isLinkUnsupported(os.ErrExist) {
		t.Error("an existing destination must not be mistaken for a filesystem without hard links")
	}
	if isLinkUnsupported(os.ErrNotExist) {
		t.Error("a missing source must not be mistaken for a filesystem without hard links")
	}
	if !isLinkUnsupported(errors.ErrUnsupported) {
		t.Error("ErrUnsupported must select the rename fallback")
	}
	if !isLinkUnsupported(os.ErrPermission) {
		t.Error("a refused link must select the rename fallback")
	}
}

// TestInvalidKeysAreRejectedByEveryOperation makes sure key validation is not
// only in the write path: a signed delivery URL reaches Open and Stat, and GC
// reaches DeleteObjects.
func TestInvalidKeysAreRejectedByEveryOperation(t *testing.T) {
	store := newTestStore(t)

	outsideDir := t.TempDir()
	secretPath := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("secret"), 0o600); err != nil {
		t.Fatalf("seeding outside file: %v", err)
	}

	badKeys := []string{
		"",
		"../" + filepath.Base(outsideDir) + "/secret.txt",
		"artwork/../../" + filepath.Base(outsideDir) + "/secret.txt",
		secretPath,
		"artwork/v1\x00/original.webp",
		`artwork\v1\original.webp`,
		".silo-artwork-store",
		"artwork/v1/" + tempFilePrefix + "abc",
		"artwork//v1/original.webp",
	}
	ctx := context.Background()
	for _, key := range badKeys {
		if err := store.WriteImmutable(ctx, key, []byte("x"), ObjectMetadata{}); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("WriteImmutable(%q) = %v, want ErrInvalidKey", key, err)
		}
		if _, err := store.Open(ctx, key); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Open(%q) = %v, want ErrInvalidKey", key, err)
		}
		if _, err := store.Stat(ctx, key); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Stat(%q) = %v, want ErrInvalidKey", key, err)
		}
		if _, err := store.Matches(ctx, key, []byte("x")); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Matches(%q) = %v, want ErrInvalidKey", key, err)
		}
		deleted, err := store.DeleteObjects(ctx, []string{key})
		if !errors.Is(err, ErrInvalidKey) {
			t.Errorf("DeleteObjects(%q) = %v, want ErrInvalidKey", key, err)
		}
		if deleted != 0 {
			t.Errorf("DeleteObjects(%q) deleted %d, want 0", key, deleted)
		}
	}

	if data, err := os.ReadFile(secretPath); err != nil || string(data) != "secret" {
		t.Fatalf("outside file was disturbed: data=%q err=%v", data, err)
	}
	entries, err := os.ReadDir(store.Root())
	if err != nil {
		t.Fatalf("reading store root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("store root has %d entries after rejected keys, want 0", len(entries))
	}
}

// TestOperationsRefuseSymlinkedObject covers a tampered or hand-built store:
// an entry that is not a plain file is reported as corruption instead of being
// followed, so artwork delivery can never read an arbitrary file and an
// immutable write can never clobber a symlink target.
func TestOperationsRefuseSymlinkedObject(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	outsideDir := t.TempDir()
	targetPath := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(targetPath, []byte("outside-bytes"), 0o600); err != nil {
		t.Fatalf("seeding outside file: %v", err)
	}

	// An in-root object the second symlink can point at.
	mustWrite(t, store, siblingKey, []byte("sibling-bytes"))

	linkPath := filepath.Join(store.Root(), filepath.FromSlash(testKey))
	if err := os.MkdirAll(filepath.Dir(linkPath), storeDirPerm); err != nil {
		t.Fatalf("creating directory: %v", err)
	}

	for _, tc := range []struct {
		name   string
		target string
	}{
		{"escaping symlink", targetPath},
		{"in-root symlink", filepath.Join(store.Root(), filepath.FromSlash(siblingKey))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.Remove(linkPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("clearing link: %v", err)
			}
			if err := os.Symlink(tc.target, linkPath); err != nil {
				t.Fatalf("creating symlink: %v", err)
			}

			if object, err := store.Open(ctx, testKey); err == nil {
				_ = object.Close()
				t.Fatal("Open followed a symlink")
			} else if !errors.Is(err, ErrNotRegularFile) {
				t.Fatalf("Open = %v, want ErrNotRegularFile", err)
			}
			if _, err := store.Stat(ctx, testKey); !errors.Is(err, ErrNotRegularFile) {
				t.Fatalf("Stat = %v, want ErrNotRegularFile", err)
			}
			if _, err := store.Matches(ctx, testKey, []byte("outside-bytes")); !errors.Is(err, ErrNotRegularFile) {
				t.Fatalf("Matches = %v, want ErrNotRegularFile", err)
			}
			if err := store.WriteImmutable(ctx, testKey, []byte("new-bytes"), ObjectMetadata{}); !errors.Is(err, ErrNotRegularFile) {
				t.Fatalf("WriteImmutable = %v, want ErrNotRegularFile", err)
			}
			if data, err := os.ReadFile(targetPath); err != nil || string(data) != "outside-bytes" {
				t.Fatalf("symlink target was modified: data=%q err=%v", data, err)
			}
		})
	}

	// GC must still be able to remove the corrupt entry, and doing so must not
	// touch what it pointed at.
	deleted, err := store.DeleteObjects(ctx, []string{testKey})
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteObjects = (%d, %v), want (1, nil)", deleted, err)
	}
	if _, err := os.Lstat(linkPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink still present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.Root(), filepath.FromSlash(siblingKey))); err != nil {
		t.Fatalf("symlink target inside the store was removed: %v", err)
	}
}

// TestWriteRefusesSymlinkedDirectoryEscape covers a symlinked *directory*
// planted inside the tree: os.Root must keep the write from landing outside
// the configured root.
func TestWriteRefusesSymlinkedDirectoryEscape(t *testing.T) {
	store := newTestStore(t)
	outsideDir := t.TempDir()

	linkDir := filepath.Join(store.Root(), "artwork")
	if err := os.Symlink(outsideDir, linkDir); err != nil {
		t.Fatalf("creating directory symlink: %v", err)
	}

	err := store.WriteImmutable(context.Background(), testKey, []byte("poster-bytes"), ObjectMetadata{})
	if err == nil {
		t.Fatal("WriteImmutable through an escaping directory symlink succeeded")
	}
	entries, err := os.ReadDir(outsideDir)
	if err != nil {
		t.Fatalf("reading outside dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("wrote %d entries outside the store root", len(entries))
	}
}

func TestMatchesReportsStoredContent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	data := []byte("poster-bytes")

	matched, err := store.Matches(ctx, testKey, data)
	if err != nil || matched {
		t.Fatalf("Matches on a missing object = (%v, %v), want (false, nil)", matched, err)
	}

	mustWrite(t, store, testKey, data)

	if matched, err := store.Matches(ctx, testKey, data); err != nil || !matched {
		t.Fatalf("Matches = (%v, %v), want (true, nil)", matched, err)
	}
	if matched, err := store.Matches(ctx, testKey, []byte("other-bytes!")); err != nil || matched {
		t.Fatalf("Matches with different bytes of equal length = (%v, %v), want (false, nil)", matched, err)
	}
	if matched, err := store.Matches(ctx, testKey, []byte("short")); err != nil || matched {
		t.Fatalf("Matches with different length = (%v, %v), want (false, nil)", matched, err)
	}
}

// TestOpenBodyIsSeekable pins what the signed delivery route needs: a body it
// can hand to http.ServeContent so a range request is answered without reading
// the object into memory.
func TestOpenBodyIsSeekable(t *testing.T) {
	store := newTestStore(t)
	data := []byte("poster-bytes")
	mustWrite(t, store, testKey, data)

	object, err := store.Open(context.Background(), testKey)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = object.Close() }()

	seeker, ok := object.ReadSeeker()
	if !ok {
		t.Fatal("filesystem object body is not seekable")
	}
	if _, err := seeker.Seek(7, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	tail, err := io.ReadAll(seeker)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(tail) != "bytes" {
		t.Fatalf("tail = %q, want %q", tail, "bytes")
	}
}

func TestOpenAndStatMissingObject(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.Open(ctx, testKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open = %v, want ErrNotFound", err)
	}
	if _, err := store.Stat(ctx, testKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stat = %v, want ErrNotFound", err)
	}
}

// TestDeleteObjectsCountsAndPrunes pins the two behaviors the revision GC
// depends on: an already-absent key counts as deleted (matching the S3 batch
// delete the GC's strict count check was written against), and emptied
// revision directories do not accumulate.
func TestDeleteObjectsCountsAndPrunes(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	mustWrite(t, store, testKey, []byte("poster-bytes"))
	mustWrite(t, store, siblingKey, []byte("variant-bytes"))

	missingKey := "artwork/v1/objects/poster/9f/" + testRevision + "/w300.webp"
	deleted, err := store.DeleteObjects(ctx, []string{testKey, missingKey})
	if err != nil {
		t.Fatalf("DeleteObjects: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2 (a missing key counts as deleted)", deleted)
	}
	revisionDir := filepath.Dir(filepath.Join(store.Root(), filepath.FromSlash(testKey)))
	if _, err := os.Stat(revisionDir); err != nil {
		t.Fatalf("revision directory removed while it still holds an object: %v", err)
	}

	deleted, err = store.DeleteObjects(ctx, []string{siblingKey})
	if err != nil {
		t.Fatalf("DeleteObjects: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if _, err := os.Stat(revisionDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("emptied revision directory was not pruned: %v", err)
	}
	// The fixed levels above the revision — format, image type, shard — stay,
	// so deletes never fight concurrent materializations for them.
	if _, err := os.Stat(filepath.Dir(revisionDir)); err != nil {
		t.Fatalf("shard directory was pruned: %v", err)
	}

	if deleted, err := store.DeleteObjects(ctx, nil); deleted != 0 || err != nil {
		t.Fatalf("DeleteObjects(nil) = (%d, %v), want (0, nil)", deleted, err)
	}
}

func TestDeleteObjectsReportsPerKeyFailures(t *testing.T) {
	store := newTestStore(t)
	mustWrite(t, store, testKey, []byte("poster-bytes"))

	deleted, err := store.DeleteObjects(context.Background(), []string{testKey, "../escape"})
	if !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("DeleteObjects = %v, want ErrInvalidKey", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1 (the valid key still goes)", deleted)
	}
}

// TestCleanTempFilesRemovesCrashDebris simulates a crash mid-write: temporary
// files left behind are invisible to the catalog but still occupy bytes.
func TestCleanTempFilesRemovesCrashDebris(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	mustWrite(t, store, testKey, []byte("poster-bytes"))

	revisionDir := filepath.Dir(filepath.Join(store.Root(), filepath.FromSlash(testKey)))
	stale := filepath.Join(revisionDir, tempFilePrefix+"staleentry01")
	staleAtRoot := filepath.Join(store.Root(), tempFilePrefix+"staleroot0001")
	fresh := filepath.Join(revisionDir, tempFilePrefix+"freshentry01")
	for _, path := range []string{stale, staleAtRoot, fresh} {
		if err := os.WriteFile(path, []byte("partial"), storeFilePerm); err != nil {
			t.Fatalf("seeding %s: %v", path, err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, path := range []string{stale, staleAtRoot} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatalf("aging %s: %v", path, err)
		}
	}

	removed, err := store.CleanTempFiles(ctx, time.Hour)
	if err != nil {
		t.Fatalf("CleanTempFiles: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	for _, path := range []string{stale, staleAtRoot} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stale temporary file %s survived: %v", path, err)
		}
	}
	if _, err := os.Lstat(fresh); err != nil {
		t.Errorf("in-flight temporary file was deleted: %v", err)
	}
	if _, err := store.Stat(ctx, testKey); err != nil {
		t.Errorf("stored object was disturbed: %v", err)
	}

	// A non-positive grace falls back to DefaultTempFileGrace rather than
	// deleting everything in flight.
	if _, err := store.CleanTempFiles(ctx, 0); err != nil {
		t.Fatalf("CleanTempFiles with default grace: %v", err)
	}
	if _, err := os.Lstat(fresh); err != nil {
		t.Errorf("default grace removed an in-flight temporary file: %v", err)
	}
}

func TestProbeCreatesRootAndLeavesNoResidue(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nested", "artwork")
	store, err := NewFilesystemStore(root)
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("root is not a directory")
	}
	if perm := info.Mode().Perm(); perm != storeDirPerm {
		t.Errorf("root permissions = %o, want %o", perm, storeDirPerm)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("probe left %d entries behind", len(entries))
	}
}

func TestProbeFailsWhenRootIsAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artwork")
	if err := os.WriteFile(path, []byte("not a store"), 0o600); err != nil {
		t.Fatalf("seeding file: %v", err)
	}
	store, err := NewFilesystemStore(path)
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Probe(context.Background()); err == nil {
		t.Fatal("Probe on a file root = nil, want failure")
	}
}

func TestProbeFailsWhenRootIsNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not restrict writes")
	}
	root := filepath.Join(t.TempDir(), "artwork")
	if err := os.MkdirAll(root, 0o500); err != nil {
		t.Fatalf("creating read-only root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })

	store, err := NewFilesystemStore(root)
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Probe(context.Background()); err == nil {
		t.Fatal("Probe on an unwritable root = nil, want failure")
	}
}

func TestOperationsRespectContextCancellation(t *testing.T) {
	store := newTestStore(t)
	mustWrite(t, store, testKey, []byte("poster-bytes"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := store.WriteImmutable(ctx, siblingKey, []byte("x"), ObjectMetadata{}); !errors.Is(err, context.Canceled) {
		t.Errorf("WriteImmutable = %v, want context.Canceled", err)
	}
	if _, err := store.Open(ctx, testKey); !errors.Is(err, context.Canceled) {
		t.Errorf("Open = %v, want context.Canceled", err)
	}
	if _, err := store.Stat(ctx, testKey); !errors.Is(err, context.Canceled) {
		t.Errorf("Stat = %v, want context.Canceled", err)
	}
	if _, err := store.Matches(ctx, testKey, []byte("x")); !errors.Is(err, context.Canceled) {
		t.Errorf("Matches = %v, want context.Canceled", err)
	}
	if _, err := store.DeleteObjects(ctx, []string{testKey}); !errors.Is(err, context.Canceled) {
		t.Errorf("DeleteObjects = %v, want context.Canceled", err)
	}
	if err := store.Probe(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Probe = %v, want context.Canceled", err)
	}
}

func TestClosedStoreFailsCleanly(t *testing.T) {
	store := newTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := store.WriteImmutable(context.Background(), testKey, []byte("x"), ObjectMetadata{}); err == nil {
		t.Fatal("WriteImmutable on a closed store = nil, want an error")
	}
}

func TestFilesystemStoreListsBoundedPagesAndConfinesLegacyPrefixCleanup(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	legacy := []string{
		"tmdb/movies/550/poster/original.old.webp",
		"tmdb/movies/550/poster/w500.old.webp",
	}
	for _, key := range append(append([]string(nil), legacy...), testKey) {
		mustWrite(t, store, key, []byte(key))
	}
	first, cursor, done, err := store.ListPage(ctx, "", "", 2)
	if err != nil || done || len(first) != 2 || cursor != first[1].Key {
		t.Fatalf("first page = %#v cursor=%q done=%v err=%v", first, cursor, done, err)
	}
	second, _, done, err := store.ListPage(ctx, "", cursor, 2)
	if err != nil || !done || len(second) != 1 {
		t.Fatalf("second page = %#v done=%v err=%v", second, done, err)
	}
	deleted, err := store.DeletePrefixMaintenance(ctx, "tmdb/movies/550/poster/")
	if err != nil || deleted != len(legacy) {
		t.Fatalf("legacy maintenance delete = (%d, %v)", deleted, err)
	}
	if _, err := store.Stat(ctx, testKey); err != nil {
		t.Fatalf("portable object was removed: %v", err)
	}
	if _, err := store.DeletePrefixMaintenance(ctx, "artwork/v1/objects/"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("portable prefix delete error = %v, want ErrInvalidKey", err)
	}
}

func assertNoTempFiles(t *testing.T, store *FilesystemStore) {
	t.Helper()
	err := filepath.WalkDir(store.Root(), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), tempFilePrefix) {
			t.Errorf("temporary file left behind: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking store: %v", err)
	}
}

func assertDirEntries(t *testing.T, dir string, want int) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	if len(entries) != want {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("%s holds %d entries (%s), want %d", dir, len(entries), strings.Join(names, ", "), want)
	}
}

// retireRootForTest forces the root swap ReopenRoot performs only when the
// configured path resolves somewhere new. The steady-state skip is what keeps a
// healthy deployment from churning handles, so a test that only called
// ReopenRoot would never exercise the retirement path at all.
func retireRootForTest(t *testing.T, store *FilesystemStore) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.retireLocked(); err != nil {
		t.Errorf("retireLocked: %v", err)
	}
}

// TestFilesystemStoreRootLeaseSurvivesConcurrentReopen pins the borrow count on
// the cached confined root. Handle.check calls ReopenRoot on every local probe
// and the health loop probes every 30 seconds, so a probe regularly lands in
// the middle of a write, a read, or a listing. Closing the root under one of
// those failed it with os.ErrClosed, which markPublishRace does not classify as
// a retryable publish race — so a materialization that raced a probe failed
// outright and the artwork request behind it returned 500.
func TestFilesystemStoreRootLeaseSurvivesConcurrentReopen(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	mustWrite(t, store, testKey, []byte("seed"))

	const workers = 6
	const iterations = 40

	failures := make(chan error, 4*workers*iterations)
	record := func(op string, err error) {
		if err != nil {
			failures <- fmt.Errorf("%s: %w", op, err)
		}
	}

	stop := make(chan struct{})
	var reopeners sync.WaitGroup
	reopeners.Add(2)
	go func() {
		defer reopeners.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			record("ReopenRoot", store.ReopenRoot())
		}
	}()
	go func() {
		defer reopeners.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			retireRootForTest(t, store)
		}
	}()

	var work sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		work.Add(3)
		go func(worker int) {
			defer work.Done()
			for i := 0; i < iterations; i++ {
				key := fmt.Sprintf("artwork/v1/objects/poster/%02d/%s/w%d.webp", worker, testRevision, i)
				record("WriteImmutable", store.WriteImmutable(ctx, key, []byte(key), ObjectMetadata{}))
			}
		}(worker)
		go func() {
			defer work.Done()
			for i := 0; i < iterations; i++ {
				object, err := store.Open(ctx, testKey)
				record("Open", err)
				if err == nil {
					_, err = io.ReadAll(object.Body)
					record("read body", err)
					record("close body", object.Body.Close())
				}
			}
		}()
		go func() {
			defer work.Done()
			for i := 0; i < iterations; i++ {
				_, _, _, err := store.ListPage(ctx, "", "", 100)
				record("ListPage", err)
			}
		}()
	}

	work.Wait()
	close(stop)
	reopeners.Wait()
	close(failures)

	for err := range failures {
		if errors.Is(err, os.ErrClosed) {
			t.Errorf("operation raced a root swap and saw a closed root: %v", err)
			continue
		}
		t.Errorf("concurrent store operation failed: %v", err)
	}
}

// TestFilesystemStoreReopenRootKeepsAnUnchangedMount pins the steady state: the
// health loop must not retire and re-resolve the root on every probe when the
// configured path still points at the same directory.
func TestFilesystemStoreReopenRootKeepsAnUnchangedMount(t *testing.T) {
	store := newTestStore(t)
	store.mu.Lock()
	before := store.root
	store.mu.Unlock()
	if before == nil {
		t.Fatal("store has no cached root after Probe")
	}
	if err := store.ReopenRoot(); err != nil {
		t.Fatalf("ReopenRoot: %v", err)
	}
	store.mu.Lock()
	after := store.root
	store.mu.Unlock()
	if after != before {
		t.Fatalf("ReopenRoot swapped the root of an unchanged mount")
	}
}

// TestFilesystemStoreListPagePagesIdenticallyAcrossSiblingDirectories pins the
// structural pruning in skipListSubtree against the unpruned semantics it
// replaced: paging a tree with many sibling directories must return exactly the
// keys an ordered full walk would, for every page, prefix, and limit the paging
// loop actually produces.
func TestFilesystemStoreListPagePagesIdenticallyAcrossSiblingDirectories(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	otherRevision := strings.Repeat("ab", 32)
	var written []string
	for _, imageType := range []string{"backdrop", "poster", "thumb"} {
		for _, shard := range []string{"0a", "9f", "ff"} {
			for _, revision := range []string{testRevision, otherRevision} {
				for _, variant := range []string{"original.webp", "w500.webp"} {
					key := "artwork/v1/objects/" + imageType + "/" + shard + "/" + revision + "/" + variant
					mustWrite(t, store, key, []byte(key))
					written = append(written, key)
				}
			}
		}
	}
	sort.Strings(written)

	for _, prefix := range []string{"", "artwork/v1/objects/poster/", "artwork/v1/objects/poster/9f"} {
		for _, limit := range []int{1, 3, 7} {
			var want []string
			for _, key := range written {
				if strings.HasPrefix(key, prefix) {
					want = append(want, key)
				}
			}

			var got []string
			cursor := ""
			for pages := 0; ; pages++ {
				if pages > len(written)+2 {
					t.Fatalf("prefix=%q limit=%d: paging did not terminate", prefix, limit)
				}
				objects, next, done, err := store.ListPage(ctx, prefix, cursor, limit)
				if err != nil {
					t.Fatalf("prefix=%q limit=%d: ListPage: %v", prefix, limit, err)
				}
				if len(objects) > limit {
					t.Fatalf("prefix=%q limit=%d: page returned %d objects", prefix, limit, len(objects))
				}
				for _, object := range objects {
					got = append(got, object.Key)
				}
				if len(objects) > 0 && next != objects[len(objects)-1].Key {
					t.Fatalf("prefix=%q limit=%d: cursor %q is not the last returned key", prefix, limit, next)
				}
				if done {
					break
				}
				cursor = next
			}

			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("prefix=%q limit=%d: paged %d keys, want %d\ngot:  %v\nwant: %v",
					prefix, limit, len(got), len(want), got, want)
			}
		}
	}
}

// TestSkipListSubtreeMatchesUnprunedFilter checks the pruning predicate against
// the per-entry filter it accelerates: a subtree may only be skipped when no
// key it could hold would have survived the cursor and prefix tests anyway.
func TestSkipListSubtreeMatchesUnprunedFilter(t *testing.T) {
	cases := []struct {
		dir, prefix, cursor string
		skip                bool
	}{
		{dir: "a", skip: false},
		{dir: "a", cursor: "b/x", skip: true},
		{dir: "b", cursor: "b/x", skip: false},
		{dir: "c", cursor: "b/x", skip: false},
		{dir: "b/c", cursor: "b/c/x", skip: false},
		{dir: "b/a", cursor: "b/c/x", skip: true},
		{dir: "b/z", cursor: "b/c/x", skip: false},
		{dir: "artwork", prefix: "artwork/v1/objects/poster/", skip: false},
		{dir: "artwork/v1/objects/poster", prefix: "artwork/v1/objects/poster/", skip: false},
		{dir: "artwork/v1/objects/thumb", prefix: "artwork/v1/objects/poster/", skip: true},
		{dir: "legacy", prefix: "artwork/", skip: true},
	}
	for _, testCase := range cases {
		if got := skipListSubtree(testCase.dir, testCase.prefix, testCase.cursor); got != testCase.skip {
			t.Errorf("skipListSubtree(%q, %q, %q) = %v, want %v",
				testCase.dir, testCase.prefix, testCase.cursor, got, testCase.skip)
		}
	}
}
