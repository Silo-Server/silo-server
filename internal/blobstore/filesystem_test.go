package blobstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/iotest"
	"time"
)

func TestFilesystemHidesTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	s, err := NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Put(context.Background(), "poster.jpg", []byte("image")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, ".probe-ready"), []byte("probe"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(root, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "nested", ".tmp-writing"), []byte("tmp"), 0644); err != nil {
		t.Fatal(err)
	}
	objects, next, err := s.List(context.Background(), "", "", 1)
	if err != nil || next != "" || len(objects) != 1 || objects[0].Key != "poster.jpg" {
		t.Fatalf("list: %v %q %v", objects, next, err)
	}
}
func TestFilesystemRejectsSymlinkParents(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	s, err := NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(outside, "image.jpg"), []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = s.Put(ctx, "linked/image.jpg", []byte("replace")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("put: %v", err)
	}
	if _, _, err = s.Get(ctx, "linked/image.jpg"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get: %v", err)
	}
	if _, err = s.DeletePrefix(ctx, "linked"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(outside, "image.jpg"))
	if err != nil || string(data) != "outside" {
		t.Fatalf("outside changed: %q %v", data, err)
	}
}
func TestFilesystemOpenReaderSurvivesReplacement(t *testing.T) {
	s, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = s.Put(ctx, "image.jpg", []byte("old")); err != nil {
		t.Fatal(err)
	}
	r, info, err := s.Get(ctx, "image.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if err = s.Put(ctx, "image.jpg", []byte("replacement")); err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(r)
	if err != nil || string(b) != "old" || info.Size != int64(len(b)) {
		t.Fatalf("reader and metadata differ: %q %+v %v", b, info, err)
	}
}

func TestFilesystemCleansOnlyAbandonedTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	active, name, err := temporary(root, ".", ".tmp-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = active.Close() }()
	old := time.Now().Add(-48 * time.Hour)
	for _, key := range []string{name, ".tmp-orphan", ".probe-new"} {
		if key != name {
			if err := os.WriteFile(filepath.Join(dir, key), nil, 0644); err != nil {
				t.Fatal(err)
			}
		}
		if key != ".probe-new" {
			if err := os.Chtimes(filepath.Join(dir, key), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, _, err := s.List(context.Background(), "", "", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".tmp-orphan")); !os.IsNotExist(err) {
		t.Fatalf("orphan: %v", err)
	}
	for _, key := range []string{name, ".probe-new"} {
		if _, err := os.Stat(filepath.Join(dir, key)); err != nil {
			t.Fatal(err)
		}
	}
	if err := active.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.List(context.Background(), "", "", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		t.Fatalf("released orphan: %v", err)
	}
}

func TestProbeReclaimsRootOrphansPastFreshFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFilesystem(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf(".probe-fresh-%02d", i)), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	var orphans []string
	for _, name := range []string{".probe-z-orphan", ".tmp-z-orphan"} {
		orphan := filepath.Join(dir, name)
		if err := os.WriteFile(orphan, nil, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(orphan, old, old); err != nil {
			t.Fatal(err)
		}
		orphans = append(orphans, orphan)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(outside, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, ".probe-link")); err != nil {
		t.Fatal(err)
	}
	if err := s.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, orphan := range orphans {
		if _, err := os.Stat(orphan); !os.IsNotExist(err) {
			t.Fatalf("orphan remains: %v", err)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".probe-link")); err != nil {
		t.Fatal(err)
	}
}

// cancelAfter reports cancellation once Err has been called more than n times.
type cancelAfter struct {
	context.Context //nolint:containedctx // wraps the context to count Err calls
	n               int
}

func (c *cancelAfter) Err() error {
	if c.n <= 0 {
		return context.Canceled
	}
	c.n--
	return nil
}

func TestFilesystemDeletePrefixStopsOnCancellation(t *testing.T) {
	s, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := range 10 {
		if err = s.Put(ctx, fmt.Sprintf("item/h%d/image.jpg", i), []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	// The walk checks the context once per entry and once before removing
	// each directory: the prefix, then per item its hash directory, its file,
	// and the directory removal. Allow two files; the second hash directory is
	// left behind (empty) because cancellation stops before removing it.
	n, err := s.DeletePrefix(&cancelAfter{Context: ctx, n: 6}, "item")
	if !errors.Is(err, context.Canceled) || n != 2 {
		t.Fatalf("partial delete = %d, %v; want 2, context.Canceled", n, err)
	}
	keys, _, err := s.List(ctx, "item/", "", 100)
	if err != nil || len(keys) != 8 {
		t.Fatalf("remaining = %d, %v; want 8", len(keys), err)
	}
	if _, err = os.Stat(filepath.Join(s.root, "item", "h0")); !os.IsNotExist(err) {
		t.Fatalf("emptied directory not removed during the walk: %v", err)
	}
	if n, err = s.DeletePrefix(ctx, "item"); err != nil || n != 8 {
		t.Fatalf("resumed delete = %d, %v; want 8", n, err)
	}
	if _, err = os.Stat(filepath.Join(s.root, "item")); !os.IsNotExist(err) {
		t.Fatalf("prefix directory left behind: %v", err)
	}
}

// TestFilesystemDeletePrefixDoesNotFollowSwappedDirectory replaces a
// directory with a symlink to another key's directory between the Lstat and
// the descent, as a concurrent writer could, and checks that the other key's
// file survives.
func TestFilesystemDeletePrefixDoesNotFollowSwappedDirectory(t *testing.T) {
	s, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, key := range []string{"item/h0/image.jpg", "other/h1/image.jpg"} {
		if err = s.Put(ctx, key, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat("item/h0")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(s.root, "item", "h0"), filepath.Join(s.root, "item", "moved")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join("..", "other", "h1"), filepath.Join(s.root, "item", "h0")); err != nil {
		t.Fatal(err)
	}

	n := 0
	if err = deleteChildren(ctx, root, "item/h0", info, &n); err == nil || n != 0 {
		t.Fatalf("descent through swapped directory = %d, %v; want 0 and an error", n, err)
	}
	if _, err = os.Stat(filepath.Join(s.root, "other", "h1", "image.jpg")); err != nil {
		t.Fatalf("file outside the prefix was removed: %v", err)
	}
}

// TestFilesystemDeletePrefixContinuesPastStuckEntry checks that one entry
// that cannot be removed does not leave the rest of the prefix behind.
func TestFilesystemDeletePrefixContinuesPastStuckEntry(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not apply to root")
	}
	s, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := range 5 {
		if err = s.Put(ctx, fmt.Sprintf("item/h%d/image.jpg", i), []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	stuck := filepath.Join(s.root, "item", "h1")
	if err = os.Chmod(stuck, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stuck, 0o755) })

	n, err := s.DeletePrefix(ctx, "item")
	if err == nil || n != 4 {
		t.Fatalf("delete with a stuck entry = %d, %v; want 4 and an error", n, err)
	}
	keys, _, err := s.List(ctx, "item/", "", 100)
	if err != nil || len(keys) != 1 || keys[0].Key != "item/h1/image.jpg" {
		t.Fatalf("remaining = %v, %v; want only item/h1/image.jpg", keys, err)
	}
}

// TestFilesystemDeletePrefixDoesNotFollowSymlinkToSameDirectory renames a
// directory out of the prefix and leaves a symlink to it under its old name
// between the Lstat and the descent. The opened directory is still the file
// Lstat saw, so only the second look at the name catches the swap.
func TestFilesystemDeletePrefixDoesNotFollowSymlinkToSameDirectory(t *testing.T) {
	s, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, key := range []string{"item/h0/image.jpg", "other/keep.jpg"} {
		if err = s.Put(ctx, key, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat("item/h0")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(s.root, "item", "h0"), filepath.Join(s.root, "other", "h0")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join("..", "other", "h0"), filepath.Join(s.root, "item", "h0")); err != nil {
		t.Fatal(err)
	}

	n := 0
	if err = deleteChildren(ctx, root, "item/h0", info, &n); err == nil || n != 0 {
		t.Fatalf("descent through symlink to the same directory = %d, %v; want 0 and an error", n, err)
	}
	if _, err = os.Stat(filepath.Join(s.root, "other", "h0", "image.jpg")); err != nil {
		t.Fatalf("file outside the prefix was removed: %v", err)
	}
}

// TestFilesystemDeletePrefixReadsInBatches deletes a directory with more
// entries than one batch, including a full batch of entries that cannot be
// removed, and checks that every removable entry is still deleted.
func TestFilesystemDeletePrefixReadsInBatches(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not apply to root")
	}
	defer func(old int) { deleteBatch = old }(deleteBatch)
	deleteBatch = 2

	s, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := range 7 {
		if err = s.Put(ctx, fmt.Sprintf("item/h%d/image.jpg", i), []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	for _, h := range []string{"h2", "h5"} {
		stuck := filepath.Join(s.root, "item", h)
		if err = os.Chmod(stuck, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(stuck, 0o755) })
	}

	n, err := s.DeletePrefix(ctx, "item")
	if err == nil || n != 5 {
		t.Fatalf("batched delete = %d, %v; want 5 and an error", n, err)
	}
	keys, _, err := s.List(ctx, "item/", "", 100)
	if err != nil || len(keys) != 2 {
		t.Fatalf("remaining = %v, %v; want the two stuck files", keys, err)
	}
}

// cancelingReader cancels its write's context while handing over the last
// bytes, as a deadline passing mid-write does.
type cancelingReader struct {
	data   []byte
	cancel context.CancelFunc
}

func (r *cancelingReader) Read(p []byte) (int, error) {
	r.cancel()
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, io.EOF
}

// TestFilesystemCanceledStreamIsNeverPublished: a write whose context ends
// while it is writing closes and removes its temporary file and publishes
// nothing, even when its reader finished.
func TestFilesystemCanceledStreamIsNeverPublished(t *testing.T) {
	root := t.TempDir()
	s, err := NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err = s.PutStream(ctx, "uploads/part", &cancelingReader{data: []byte("bytes"), cancel: cancel}, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("PutStream = %v, want context.Canceled", err)
	}
	if _, err := s.Stat(context.Background(), "uploads/part"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a canceled write was published: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "uploads"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("left behind %v (%v)", entries, err)
	}
}

// A local store stops at its free-space reserve instead of filling the disk,
// and still deletes, which is how an admin makes room again.
func TestFilesystemStopsAtFreeSpaceReserve(t *testing.T) {
	root := t.TempDir()
	s, err := NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// A 100 GiB filesystem with 10 bytes above the reserve, less whatever is
	// written to the store after the seed below, temporary files included.
	// The reserve is the 2 GiB cap, which a tenth of 100 GiB exceeds. The
	// fake goes in before the seed, so the real disk's free space never
	// decides the test.
	const total, reserve = 100 << 30, 2 << 30
	stored := func() int64 {
		var n int64
		_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				if info, err := d.Info(); err == nil {
					n += info.Size()
				}
			}
			return nil
		})
		return n
	}
	var start int64
	realStat := statSpace
	statSpace = func(string) (int64, int64, error) { return reserve + 10 - (stored() - start), total, nil }
	t.Cleanup(func() { statSpace = realStat })
	if err = s.Put(ctx, "kept/poster.jpg", []byte("image")); err != nil {
		t.Fatal(err)
	}
	start = stored()

	// A write that would cut into the reserve is refused before it creates
	// anything, and one whose length is unknown stops when it reaches it.
	if err = s.Put(ctx, "new/poster.jpg", make([]byte, 11)); !errors.Is(err, ErrStorageFull) || !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("put past the reserve: %v", err)
	}
	if _, err = os.Stat(filepath.Join(root, "new")); !os.IsNotExist(err) {
		t.Fatalf("refused put left a directory: %v", err)
	}
	if err = s.PutStream(ctx, "kept/sheet.jpg", bytes.NewReader(make([]byte, 64<<10)), ""); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("stream past the reserve: %v", err)
	}
	if _, err = s.Stat(ctx, "kept/sheet.jpg"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refused stream was published: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "kept"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("refused stream left files behind: %v %v", entries, err)
	}

	// Writes in flight share the budget, and each byte counts once. Stream A
	// writes 2 of the 10 bytes.
	settledBefore := s.space.settled.Load()
	waitSettled := func(n int64) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); s.space.settled.Load() != settledBefore+n; {
			if time.Now().After(deadline) {
				t.Fatalf("stream never wrote its bytes: %d of %d settled", s.space.settled.Load()-settledBefore, n)
			}
			runtime.Gosched()
		}
	}
	pr, pw := io.Pipe()
	streamed := make(chan error, 1)
	streamDone := make(chan struct{})
	// Runs before statSpace is restored, so the stream never reads the
	// real function mid-test.
	t.Cleanup(func() { _ = pw.Close(); <-streamDone })
	go func() {
		defer close(streamDone)
		err := s.PutStream(ctx, "kept/stream.jpg", pr, "")
		_ = pr.CloseWithError(err)
		streamed <- err
	}()
	if _, err = pw.Write(make([]byte, 2)); err != nil {
		t.Fatal(err)
	}
	waitSettled(2)
	// A competing stream writes 8 bytes, fails, and removes its file. A's
	// next 7 bytes then fit, although A's earlier reading still counts them.
	errSource := errors.New("source failed")
	if err = s.PutStream(ctx, "kept/failed.jpg", io.MultiReader(bytes.NewReader(make([]byte, 8)), iotest.ErrReader(errSource)), ""); !errors.Is(err, errSource) {
		t.Fatalf("competing stream: %v", err)
	}
	if _, err = pw.Write(make([]byte, 7)); err != nil {
		t.Fatal(err)
	}
	waitSettled(17)
	// A 1-byte put fits beside A's 9 bytes, which statfs already counts, and
	// then A's next byte does not.
	if err = s.Put(ctx, "kept/during.jpg", make([]byte, 1)); err != nil {
		t.Fatalf("put that fits beside a stream: %v", err)
	}
	if _, err = pw.Write(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	_ = pw.Close()
	if err = <-streamed; !errors.Is(err, ErrStorageFull) {
		t.Fatalf("stream past the budget another write took: %v", err)
	}
	// A's failure removed its 9 bytes. Fill them, so the store sits at the
	// reserve: an existing key still reports ErrExists, which a restore run
	// again relies on, and deletes still work.
	if err = s.Put(ctx, "kept/fill.jpg", make([]byte, 9)); err != nil {
		t.Fatalf("put that fills the budget: %v", err)
	}
	if err = s.Put(ctx, "kept/more.jpg", make([]byte, 1)); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("put at the reserve: %v", err)
	}
	if err = s.CreateExclusive(ctx, "kept/poster.jpg", bytes.NewReader(make([]byte, 64<<10)), ""); !errors.Is(err, ErrExists) {
		t.Fatalf("exclusive create of an existing key at the reserve: %v", err)
	}
	if n, err := s.Delete(ctx, []string{"kept/poster.jpg", "kept/during.jpg", "kept/fill.jpg"}); err != nil || n != 3 {
		t.Fatalf("delete while full: %d %v", n, err)
	}
}

// A restore's rollback putting a replaced file back may use the reserve,
// which other writes may not.
func TestFilesystemRollbackWritesMayUseTheReserve(t *testing.T) {
	s, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realStat := statSpace
	statSpace = func(string) (int64, int64, error) { return 2 << 30, 100 << 30, nil }
	t.Cleanup(func() { statSpace = realStat })
	if err = s.PutStream(t.Context(), "posters/1.jpg", bytes.NewReader([]byte("original")), ""); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("write at the reserve: %v", err)
	}
	if err = s.PutStream(WithoutFreeSpaceReserve(t.Context()), "posters/1.jpg", bytes.NewReader([]byte("original")), ""); err != nil {
		t.Fatalf("rollback write at the reserve: %v", err)
	}
}

// A long stream reads free space again as it goes, so space another writer
// takes meanwhile (PostgreSQL growing beside a backup) stops the stream at
// the reserve instead of letting it run on its first reading.
func TestFilesystemStreamSeesSpaceTakenByOthers(t *testing.T) {
	root := t.TempDir()
	s, err := NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	const total, reserve = 100 << 30, 2 << 30
	var written, others atomic.Int64
	realStat := statSpace
	statSpace = func(string) (int64, int64, error) {
		return reserve + 3*streamRecheckBytes - written.Load() - others.Load(), total, nil
	}
	t.Cleanup(func() { statSpace = realStat })
	// After the stream's first streamRecheckBytes, another writer takes
	// enough that the rest of the stream no longer fits above the reserve.
	src := &countingReader{n: 2 * streamRecheckBytes, written: &written, onPast: streamRecheckBytes, past: func() { others.Store(2 * streamRecheckBytes) }}
	if err = s.PutStream(t.Context(), "backups/archive.tar", src, ""); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("stream after another writer took the room: %v", err)
	}
}

// A free-space reading that fails partway through a stream does not leave
// the rest of it unlimited: the next reading still stops it.
func TestFilesystemStreamRechecksAfterAFailedReading(t *testing.T) {
	s, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const total, reserve = 100 << 30, 2 << 30
	var written, others atomic.Int64
	var reads atomic.Int32
	realStat := statSpace
	statSpace = func(string) (int64, int64, error) {
		if reads.Add(1) == 2 {
			return 0, 0, errors.New("statfs: interrupted")
		}
		return reserve + 4*streamRecheckBytes - written.Load() - others.Load(), total, nil
	}
	t.Cleanup(func() { statSpace = realStat })
	// The first recheck fails; another writer then takes the room, which
	// the second recheck sees.
	src := &countingReader{n: 3 * streamRecheckBytes, written: &written, onPast: streamRecheckBytes, past: func() { others.Store(3 * streamRecheckBytes) }}
	if err = s.PutStream(t.Context(), "backups/archive.tar", src, ""); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("stream after a failed reading: %v", err)
	}
}

// countingReader yields n zero bytes, counting them in written, and calls
// past once more than onPast bytes have been read.
type countingReader struct {
	n, onPast int64
	written   *atomic.Int64
	past      func()
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, io.EOF
	}
	k := min(int64(len(p)), r.n)
	clear(p[:k])
	r.n -= k
	if r.written.Add(k) > r.onPast && r.past != nil {
		r.past()
		r.past = nil
	}
	return int(k), nil
}
