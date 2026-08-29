package artworkstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkmetrics"
)

const (
	// storeDirPerm and storeFilePerm keep the canonical store private to the
	// server user. Nothing else reads the tree directly: bytes reach clients
	// through the signed artwork route, never through a web server pointed at
	// the directory.
	storeDirPerm  os.FileMode = 0o700
	storeFilePerm os.FileMode = 0o600

	// tempFilePrefix marks in-flight writes. It starts with a dot, which
	// ValidateKey rejects, so a temporary file can never be addressed as an
	// object and never becomes catalog-visible.
	tempFilePrefix = ".tmp-artwork-"

	// DefaultTempFileGrace is the conservative age after which an abandoned
	// temporary file is assumed to be crash debris rather than an in-flight
	// write. Materialization writes are small and short; hours of slack costs
	// nothing and guarantees a concurrent writer is never disturbed.
	DefaultTempFileGrace = 6 * time.Hour

	// maxTempFileAttempts bounds retries when a random temporary name collides
	// with an existing one.
	maxTempFileAttempts = 3

	// maxWriteAttempts bounds how often a publish is retried after losing a
	// race with directory pruning. Each attempt is idempotent and the losing
	// window is a single syscall wide, so a small bound is enough.
	maxWriteAttempts = 4
)

// FilesystemStore is the canonical artwork store backed by a directory tree.
// It also serves the shared-POSIX/NAS deployment: there is no separate backend
// for that case, the operator mounts one path identically on every API node.
//
// Confinement is enforced by os.Root, so a key can never resolve outside the
// configured root even through a symlink or a concurrent path swap. On top of
// that, object reads and immutable writes refuse anything that is not a plain
// regular file, so a symlink planted inside the tree is reported as corruption
// rather than followed.
type FilesystemStore struct {
	rootPath string

	mu               sync.Mutex
	root             *rootRef
	closed           bool
	pinnedGeneration string
}

// rootRef is one opened confined root plus the number of store operations
// currently borrowing it. Every borrower keeps using the *os.Root it took under
// the lock for the whole operation, so closing the handle out from under one —
// which ReopenRoot did on every health probe — failed that operation with
// os.ErrClosed. That is not a shape markPublishRace treats as a retryable
// publish race, so a mid-flight write surfaced as a hard materialization
// failure. A retired root is therefore closed by whichever borrower releases it
// last, and never while a borrow is outstanding.
//
// All three fields are owned by FilesystemStore.mu.
type rootRef struct {
	root    *os.Root
	borrows int
	retired bool
}

// borrowLocked hands out the cached root and the release func that returns it.
func (s *FilesystemStore) borrowLocked() (*os.Root, func()) {
	ref := s.root
	ref.borrows++
	return ref.root, func() { s.release(ref) }
}

func (s *FilesystemStore) release(ref *rootRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ref.borrows--
	_ = s.closeRetiredLocked(ref)
}

// retireLocked drops the cached root so the next operation resolves rootPath
// again. The handle itself is closed here only when nobody is using it.
func (s *FilesystemStore) retireLocked() error {
	ref := s.root
	s.root = nil
	if ref == nil {
		return nil
	}
	ref.retired = true
	return s.closeRetiredLocked(ref)
}

func (s *FilesystemStore) closeRetiredLocked(ref *rootRef) error {
	if ref == nil || !ref.retired || ref.borrows > 0 || ref.root == nil {
		return nil
	}
	root := ref.root
	ref.root = nil
	return root.Close()
}

func (s *FilesystemStore) setPinnedGeneration(generation string) {
	s.mu.Lock()
	s.pinnedGeneration = generation
	s.mu.Unlock()
}

var _ Store = (*FilesystemStore)(nil)

// NewFilesystemStore validates a configured root and returns a store for it.
// The directory is created on first use (Probe is the explicit startup path);
// construction only rejects roots that can never be right.
func NewFilesystemStore(rootPath string) (*FilesystemStore, error) {
	trimmed := strings.TrimSpace(rootPath)
	if trimmed == "" {
		return nil, errors.New("artworkstore: filesystem store root is empty")
	}
	if !filepath.IsAbs(trimmed) {
		return nil, fmt.Errorf("artworkstore: filesystem store root must be an absolute path: %s", trimmed)
	}
	clean := filepath.Clean(trimmed)
	if filepath.Dir(clean) == clean {
		return nil, errors.New("artworkstore: refusing to use the filesystem root as the artwork store")
	}
	return &FilesystemStore{rootPath: clean}, nil
}

// Root returns the configured store root. It is deployment configuration for
// admin status and log lines; it never leaks into a logical key or a client URL.
func (s *FilesystemStore) Root() string {
	return s.rootPath
}

func (s *FilesystemStore) FreeSpaceBytes(_ context.Context) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(s.rootPath, &stat); err != nil {
		return 0, fmt.Errorf("artworkstore: stat filesystem capacity: %w", err)
	}
	// stat.Bsize is int64 on linux but uint32 on darwin; the conversion is
	// required for the narrower platforms.
	return int64(stat.Bavail) * int64(stat.Bsize), nil //nolint:unconvert
}

// Close permanently releases the store root handle.
func (s *FilesystemStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.retireLocked()
}

// ReopenRoot drops the cached confined root without closing the store, so the
// next operation resolves rootPath again. That is what lets a local mount
// recover after a filesystem is replaced at the same pathname.
//
// The health loop calls this on every probe, so the steady state must be free:
// when the configured path still resolves to the directory the cached root
// already holds, nothing is retired at all. Only a genuine swap costs a
// reopen — and even then the retired handle stays open until its last in-flight
// borrower is done with it.
func (s *FilesystemStore) ReopenRoot() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil || s.root.root == nil {
		return nil
	}
	if current, err := os.Stat(s.rootPath); err == nil {
		if cached, statErr := s.root.root.Stat("."); statErr == nil && os.SameFile(current, cached) {
			return nil
		}
	}
	return s.retireLocked()
}

// openRoot returns the cached confined root handle, creating the directory on
// first use. The returned func releases the borrow and must be called exactly
// once, after the caller has finished issuing operations against the root.
func (s *FilesystemStore) openRoot() (*os.Root, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, errors.New("artworkstore: filesystem store is closed")
	}
	if s.root != nil {
		root, release := s.borrowLocked()
		return root, release, nil
	}
	if s.pinnedGeneration != "" {
		if _, err := os.Stat(s.rootPath); err != nil {
			return nil, nil, fmt.Errorf("%w: store root %s: %w", ErrBackendUnavailable, s.rootPath, err)
		}
	} else if err := os.MkdirAll(s.rootPath, storeDirPerm); err != nil {
		return nil, nil, fmt.Errorf("artworkstore: creating store root %s: %w", s.rootPath, err)
	}
	root, err := os.OpenRoot(s.rootPath)
	if err != nil {
		return nil, nil, fmt.Errorf("artworkstore: opening store root %s: %w", s.rootPath, err)
	}
	if s.pinnedGeneration != "" {
		marker, markerErr := readMarker(root)
		if markerErr != nil || marker.ID != s.pinnedGeneration {
			_ = root.Close()
			return nil, nil, ErrWrongMount
		}
		file, _, formatErr := openRegular(root, formatMarkerFileName)
		if formatErr != nil {
			_ = root.Close()
			return nil, nil, ErrWrongMount
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 128))
		_ = file.Close()
		if readErr != nil || string(data) != formatMarkerContents {
			_ = root.Close()
			return nil, nil, ErrWrongMount
		}
	}
	s.root = &rootRef{root: root}
	borrowed, release := s.borrowLocked()
	return borrowed, release, nil
}

// prepareEmptyRebuild creates the configured root when absent and removes its
// old identity sentinels only after proving that no logical artwork objects are
// present. The caller must block ordinary writes while this runs and then
// create fresh sentinels before making the store writable again.
func (s *FilesystemStore) prepareEmptyRebuild(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("artworkstore: filesystem store is closed")
	}
	_ = s.retireLocked()
	if err := os.MkdirAll(s.rootPath, storeDirPerm); err != nil {
		return fmt.Errorf("artworkstore: recreating store root %s: %w", s.rootPath, err)
	}
	root, err := os.OpenRoot(s.rootPath)
	if err != nil {
		return fmt.Errorf("artworkstore: opening rebuilt store root %s: %w", s.rootPath, err)
	}
	defer func() { _ = root.Close() }()
	if err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || name == markerFileName || name == formatMarkerFileName {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrStoreNotEmpty, name)
	}); err != nil {
		return err
	}
	for _, name := range []string{markerFileName, formatMarkerFileName} {
		if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("artworkstore: removing old store sentinel %s: %w", name, err)
		}
	}
	s.pinnedGeneration = ""
	return nil
}

func (s *FilesystemStore) openRootExisting() (*os.Root, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, errors.New("artworkstore: filesystem store is closed")
	}
	if s.root != nil {
		root, release := s.borrowLocked()
		return root, release, nil
	}
	if _, err := os.Stat(s.rootPath); err != nil {
		return nil, nil, fmt.Errorf("artworkstore: opening existing store root %s: %w", s.rootPath, err)
	}
	root, err := os.OpenRoot(s.rootPath)
	if err != nil {
		return nil, nil, fmt.Errorf("artworkstore: opening store root %s: %w", s.rootPath, err)
	}
	// Deliberately not cached in s.root: this path skips the pin and marker
	// verification openRoot performs, and caching an unverified root would let
	// concurrent writes land on a swapped mount during the very probe that is
	// about to flag it as wrong_mount.
	return root, func() { _ = root.Close() }, nil
}

// Probe creates the root if needed and proves it is a writable directory. An
// unwritable canonical store is a startup/readiness failure: the server must
// never quietly fall back to upstream sources or to another backend.
func (s *FilesystemStore) Probe(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, release, err := s.openRoot()
	if err != nil {
		return err
	}
	defer release()
	info, err := root.Stat(".")
	if err != nil {
		return fmt.Errorf("artworkstore: inspecting store root %s: %w", s.rootPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("artworkstore: store root %s is not a directory", s.rootPath)
	}
	name, err := tempFileName(".")
	if err != nil {
		return err
	}
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, storeFilePerm)
	if err != nil {
		return fmt.Errorf("artworkstore: store root %s is not writable: %w", s.rootPath, err)
	}
	defer func() { _ = root.Remove(name) }()
	if _, err := file.Write([]byte("silo artwork store write probe\n")); err != nil {
		_ = file.Close()
		return fmt.Errorf("artworkstore: writing to store root %s: %w", s.rootPath, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("artworkstore: flushing store root %s: %w", s.rootPath, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("artworkstore: closing probe file in %s: %w", s.rootPath, err)
	}
	return nil
}

// WriteImmutable writes data at key exactly once. The bytes land in a
// temporary file in the destination directory, are flushed and closed, and are
// then published under the real name with an atomic create-only link, so a
// reader or a crash never observes a partial object. Writing the same bytes
// again is a no-op success; writing different bytes returns ErrContentMismatch
// without touching the stored object.
func (s *FilesystemStore) WriteImmutable(ctx context.Context, key string, data []byte, metadata ObjectMetadata) error {
	_ = metadata // the filesystem store derives media type from the key extension on read.
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateKey(key); err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("artworkstore: refusing to write an empty object: %s", key)
	}
	root, release, err := s.openRoot()
	if err != nil {
		return err
	}
	defer release()
	digest := hashBytes(data)
	// Reference-aware GC prunes directories it empties, so a concurrent delete
	// can pull the destination directory out from under this write. Every step
	// below is idempotent, so a lost path is simply retried rather than
	// surfaced as a materialization failure.
	for attempt := 0; ; attempt++ {
		err = writeObject(root, key, data, digest)
		if err == nil || attempt >= maxWriteAttempts-1 || !errors.Is(err, errPublishRace) {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
}

// writeObject performs one publish attempt for an immutable key.
func writeObject(root *os.Root, key string, data []byte, digest string) error {
	switch exists, matched, err := compareObject(root, key, int64(len(data)), digest); {
	case err != nil:
		return markPublishRace(err)
	case exists && matched:
		return nil
	case exists:
		return fmt.Errorf("%w: %s", ErrContentMismatch, key)
	}

	dir := path.Dir(key)
	if dir != "." {
		if err := root.MkdirAll(dir, storeDirPerm); err != nil {
			return markPublishRace(fmt.Errorf("artworkstore: creating directory %s: %w", dir, err))
		}
	}
	tempName, err := writeTempFile(root, dir, data)
	if err != nil {
		return markPublishRace(err)
	}
	// After a successful link the object has two names; after a rename
	// fallback the temporary name is already gone. Removing it unconditionally
	// covers both, plus every error path.
	defer func() { _ = root.Remove(tempName) }()

	err = root.Link(tempName, key)
	if err != nil && !errors.Is(err, fs.ErrExist) && isLinkUnsupported(err) {
		// Filesystems without hard links lose the create-only race guard; the
		// pre-check above still covers the ordinary immutability case.
		err = root.Rename(tempName, key)
	}
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			// A concurrent writer published first. Identical bytes are the
			// expected case for content-addressed keys and count as success.
			exists, matched, cmpErr := compareObject(root, key, int64(len(data)), digest)
			if cmpErr != nil {
				return cmpErr
			}
			if exists && matched {
				return nil
			}
			return fmt.Errorf("%w: %s", ErrContentMismatch, key)
		}
		return markPublishRace(fmt.Errorf("artworkstore: publishing %s: %w", key, err))
	}
	syncDir(root, dir)
	return nil
}

// Open returns the object at key with a streaming body. The body is an
// *os.File, so delivery can hand it to http.ServeContent for range support
// without reading the object into memory.
func (s *FilesystemStore) Open(ctx context.Context, key string) (*Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	root, release, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	defer release()
	file, info, err := openRegular(root, key)
	if err != nil {
		return nil, err
	}
	return &Object{Info: objectInfo(key, info), Body: file}, nil
}

// Stat returns object metadata without opening the body.
func (s *FilesystemStore) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	if err := ValidateKey(key); err != nil {
		return ObjectInfo{}, err
	}
	root, release, err := s.openRoot()
	if err != nil {
		return ObjectInfo{}, err
	}
	defer release()
	info, err := root.Lstat(key)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ObjectInfo{}, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return ObjectInfo{}, fmt.Errorf("artworkstore: inspecting %s: %w", key, err)
	}
	if !info.Mode().IsRegular() {
		return ObjectInfo{}, fmt.Errorf("%w: %s", ErrNotRegularFile, key)
	}
	return objectInfo(key, info), nil
}

// Matches reports whether key already holds exactly these bytes.
func (s *FilesystemStore) Matches(ctx context.Context, key string, data []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := ValidateKey(key); err != nil {
		return false, err
	}
	root, release, err := s.openRoot()
	if err != nil {
		return false, err
	}
	defer release()
	exists, matched, err := compareObject(root, key, int64(len(data)), hashBytes(data))
	if err != nil {
		return false, err
	}
	return exists && matched, nil
}

// DeleteObjects removes every key and prunes the object directory it empties.
// Keys that are already gone count as deleted so the revision GC's strict count
// check behaves identically on both backends. Deletion never follows a symlink:
// it unlinks the entry itself.
func (s *FilesystemStore) DeleteObjects(ctx context.Context, keys []string) (int, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	root, release, err := s.openRoot()
	if err != nil {
		return 0, err
	}
	defer release()
	deleted := 0
	var errs []error
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		if err := ValidateKey(key); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := root.Remove(key); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("artworkstore: deleting %s: %w", key, err))
			continue
		}
		deleted++
		pruneEmptyDir(root, path.Dir(key))
	}
	return deleted, errors.Join(errs...)
}

func (s *FilesystemStore) ListPage(ctx context.Context, prefix, cursor string, limit int) ([]ObjectInfo, string, bool, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	root, release, err := s.openRoot()
	if err != nil {
		return nil, cursor, false, err
	}
	defer release()
	var objects []ObjectInfo
	done := true
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if name != "." && skipListSubtree(name, prefix, cursor) {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(path.Base(name), ".") || name <= cursor || !strings.HasPrefix(name, prefix) {
			return nil
		}
		if len(objects) == limit {
			done = false
			return fs.SkipAll
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		objects = append(objects, objectInfo(name, info))
		return nil
	})
	if err != nil {
		return nil, cursor, false, err
	}
	next := cursor
	if len(objects) > 0 {
		next = objects[len(objects)-1].Key
	}
	return objects, next, done, nil
}

// skipListSubtree reports whether a directory can be pruned from a ListPage
// walk because it cannot contribute a key to this page. Without it, paging
// re-walked every already-consumed directory from the tree root on every page
// and only discarded the entries one at a time, which is quadratic in the
// number of objects — inventory refresh pages at 500 and seed import at 250, so
// a large store paid that cost thousands of times.
//
// Both tests are structural, and both rest on the same lexical ordering the
// cursor contract already assumes. Every key beneath dir begins with dir+"/":
//
//   - Cursor. If the cursor sorts inside the subtree, keys after it may still be
//     there. Otherwise, a subtree sorting before the cursor has all of its keys
//     before the cursor too, because they share the subtree's prefix.
//   - Prefix. The subtree can only intersect the filter if one of the two is a
//     prefix of the other: prefix ⊑ subtree matches every key below, and
//     subtree ⊑ prefix leaves deeper keys that may still match.
func skipListSubtree(dir, prefix, cursor string) bool {
	subtree := dir + "/"
	if cursor != "" && !strings.HasPrefix(cursor, subtree) && subtree < cursor {
		return true
	}
	if prefix != "" && !strings.HasPrefix(subtree, prefix) && !strings.HasPrefix(prefix, subtree) {
		return true
	}
	return false
}

func (s *FilesystemStore) DeletePrefixMaintenance(ctx context.Context, prefix string) (int, error) {
	if err := validateLegacyMaintenancePrefix(prefix); err != nil {
		return 0, err
	}
	var keys []string
	cursor := ""
	for {
		objects, next, done, err := s.ListPage(ctx, prefix, cursor, 500)
		if err != nil {
			return 0, err
		}
		for _, object := range objects {
			keys = append(keys, object.Key)
		}
		cursor = next
		if done {
			break
		}
	}
	return s.DeleteObjects(ctx, keys)
}

// CleanTempFiles removes abandoned temporary files older than olderThan, which
// defaults to DefaultTempFileGrace. Crash debris is invisible to the catalog
// but still occupies bytes, so startup and periodic maintenance sweep it.
// Unreadable subtrees are reported without aborting the sweep.
func (s *FilesystemStore) CleanTempFiles(ctx context.Context, olderThan time.Duration) (int, error) {
	if olderThan <= 0 {
		olderThan = DefaultTempFileGrace
	}
	root, release, err := s.openRoot()
	if err != nil {
		return 0, err
	}
	defer release()
	cutoff := time.Now().Add(-olderThan)
	removed := 0
	var errs []error
	walkErr := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("artworkstore: scanning %s: %w", name, err))
			return nil
		}
		if entry.IsDir() || !strings.HasPrefix(path.Base(name), tempFilePrefix) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("artworkstore: inspecting %s: %w", name, err))
			}
			return nil
		}
		if info.ModTime().After(cutoff) {
			return nil
		}
		if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("artworkstore: deleting %s: %w", name, err))
			return nil
		}
		removed++
		return nil
	})
	if walkErr != nil {
		errs = append(errs, walkErr)
	}
	artworkmetrics.TempFilesCleaned(removed)
	return removed, errors.Join(errs...)
}

// writeTempFile writes data to a fresh temporary file in dir and returns its
// name. The file is fully flushed before it is returned, so publishing it is
// the only remaining step. Once the temporary file exists it also pins its
// directory: pruning cannot remove a non-empty directory, so the publish step
// cannot lose the path underneath it.
func writeTempFile(root *os.Root, dir string, data []byte) (string, error) {
	for attempt := 0; attempt < maxTempFileAttempts; attempt++ {
		name, err := tempFileName(dir)
		if err != nil {
			return "", err
		}
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, storeFilePerm)
		if err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return "", fmt.Errorf("artworkstore: creating temporary file in %s: %w", dir, err)
		}
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			_ = root.Remove(name)
			return "", fmt.Errorf("artworkstore: writing temporary file in %s: %w", dir, err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			_ = root.Remove(name)
			return "", fmt.Errorf("artworkstore: flushing temporary file in %s: %w", dir, err)
		}
		if err := file.Close(); err != nil {
			_ = root.Remove(name)
			return "", fmt.Errorf("artworkstore: closing temporary file in %s: %w", dir, err)
		}
		return name, nil
	}
	return "", fmt.Errorf("artworkstore: could not create a temporary file in %s", dir)
}

// compareObject reports whether key exists and whether its stored bytes hash to
// digest. A store entry that is not a regular file is an error, never a silent
// overwrite target.
func compareObject(root *os.Root, key string, size int64, digest string) (exists bool, matched bool, err error) {
	info, err := root.Lstat(key)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, false, nil
		}
		return false, false, fmt.Errorf("artworkstore: inspecting %s: %w", key, err)
	}
	if !info.Mode().IsRegular() {
		return false, false, fmt.Errorf("%w: %s", ErrNotRegularFile, key)
	}
	if info.Size() != size {
		return true, false, nil
	}
	file, _, err := openRegular(root, key)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, false, nil
		}
		return false, false, err
	}
	defer func() { _ = file.Close() }()
	stored, err := hashReader(file)
	if err != nil {
		return false, false, fmt.Errorf("artworkstore: reading %s: %w", key, err)
	}
	return true, stored == digest, nil
}

// openRegular opens a store object, refusing anything that is not a plain file.
// os.Root already guarantees the path cannot escape the store; the Lstat,
// fstat, and SameFile checks close the symlink-swap window so an entry replaced
// mid-open is rejected instead of followed.
func openRegular(root *os.Root, key string) (*os.File, os.FileInfo, error) {
	info, err := root.Lstat(key)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, nil, fmt.Errorf("artworkstore: inspecting %s: %w", key, err)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%w: %s", ErrNotRegularFile, key)
	}
	file, err := root.Open(key)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return nil, nil, fmt.Errorf("artworkstore: opening %s: %w", key, err)
	}
	stat, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("artworkstore: inspecting %s: %w", key, err)
	}
	if !stat.Mode().IsRegular() || !os.SameFile(info, stat) {
		_ = file.Close()
		return nil, nil, fmt.Errorf("%w: %s", ErrNotRegularFile, key)
	}
	return file, stat, nil
}

func objectInfo(key string, info os.FileInfo) ObjectInfo {
	return ObjectInfo{
		Key:       key,
		SizeBytes: info.Size(),
		MediaType: MediaTypeForKey(key),
		ETag:      entityTag(key, info.Size(), info.ModTime()),
		ModTime:   info.ModTime(),
	}
}

// tempFileName returns an unpredictable temporary name inside dir. The name is
// dot-prefixed so it can never be addressed as an object.
func tempFileName(dir string) (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("artworkstore: generating a temporary file name: %w", err)
	}
	name := tempFilePrefix + hex.EncodeToString(buf)
	if dir == "" || dir == "." {
		return name, nil
	}
	return dir + "/" + name, nil
}

// pruneEmptyDir removes the object's own directory once GC has emptied it.
// That directory is the per-revision level, which is the only level with
// unbounded cardinality; the fixed levels above it (format, image type, and the
// 256 shards) are left in place deliberately. Walking further up would put
// every delete in a permanent race with every concurrent materialization for no
// meaningful space saving.
//
// Removing a non-empty directory fails, so a concurrent write is never
// destroyed, and a writer that loses its directory recreates it.
func pruneEmptyDir(root *os.Root, dir string) {
	if dir == "." || dir == "/" || dir == "" {
		return
	}
	_ = root.Remove(dir)
}

// syncDir flushes a directory entry so a published object survives a crash on
// filesystems that need it. Some filesystems reject directory fsync, and the
// object itself is already durable, so failures are not fatal.
func syncDir(root *os.Root, dir string) {
	if dir == "" {
		dir = "."
	}
	file, err := root.Open(dir)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	_ = file.Sync()
}

// isLinkUnsupported reports whether hard links are unavailable on this
// filesystem, in which case publishing falls back to rename.
func isLinkUnsupported(err error) bool {
	return errors.Is(err, errors.ErrUnsupported) || errors.Is(err, fs.ErrPermission)
}

// errPublishRace tags a write attempt that lost a race with directory pruning
// so WriteImmutable can retry it. It is never returned on its own.
var errPublishRace = errors.New("artworkstore: write lost a race with directory pruning")

// markPublishRace tags failures that look like a destination directory
// disappearing mid-write, which is exactly what reference-aware GC pruning
// looks like to a concurrent writer, and passes every other failure through
// untouched so real problems — permission, no space, quota, corruption — fail
// immediately.
//
// The shapes vary by platform and by which syscall lost the race: a removed
// parent surfaces as ENOENT on Linux and EINVAL on macOS, a directory replaced
// by a file as ENOTDIR, and MkdirAll reports EEXIST when the directory it just
// found is unlinked before it can be inspected.
func markPublishRace(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist),
		errors.Is(err, fs.ErrExist),
		errors.Is(err, syscall.EINVAL),
		errors.Is(err, syscall.ENOTDIR):
		return fmt.Errorf("%w: %w", errPublishRace, err)
	default:
		return err
	}
}
