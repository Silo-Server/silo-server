package blobstore

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type Filesystem struct{ root string }

func NewFilesystem(root string) (*Filesystem, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("artwork filesystem root is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	// Resolve existing ancestor aliases once (for example macOS /var). The
	// configured root itself and paths within it must still be real directories.
	parent := filepath.Dir(abs)
	suffix := filepath.Base(abs)
	for {
		resolved, resolveErr := filepath.EvalSymlinks(parent)
		if resolveErr == nil {
			abs = filepath.Join(resolved, suffix)
			break
		}
		if !os.IsNotExist(resolveErr) || parent == filepath.Dir(parent) {
			break
		}
		suffix = filepath.Join(filepath.Base(parent), suffix)
		parent = filepath.Dir(parent)
	}
	if err = ensureRoot(abs); err != nil && !temporaryStorageError(err) {
		return nil, err
	}
	return &Filesystem{root: filepath.Clean(abs)}, nil
}

func temporaryStorageError(err error) bool {
	return errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EROFS) || errors.Is(err, syscall.EIO) || errors.Is(err, syscall.ESTALE) || errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) || errors.Is(err, syscall.ETIMEDOUT)
}

func ensureRoot(dir string) error {
	// Reject symlinks and non-directories even when a later component is absent.
	current := string(filepath.Separator)
	for _, component := range strings.Split(strings.TrimPrefix(dir, current), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("artwork root component is not a directory: %s", current)
		}
	}
	return os.MkdirAll(dir, 0755)
}

// check refuses existing symlink components. Root operations additionally enforce
// containment if a component changes after this check.
func check(root *os.Root, key string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	parts := strings.Split(key, "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrNotFound
		}
	}
	return nil
}
func temporary(root *os.Root, dir, prefix string) (*os.File, string, error) {
	name := path.Join(dir, fmt.Sprintf("%s%x", prefix, rand.Text()))
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0644)
	if err == nil {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err != nil {
			_ = f.Close()
			_ = root.Remove(name)
			return nil, "", err
		}
	}
	return f, name, err
}
func (f *Filesystem) Put(ctx context.Context, key string, data []byte) error {
	return f.publish(ctx, key, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// PutStream publishes an object without holding it in memory. The content type
// is ignored: local objects carry no metadata, and reads derive a media type
// from the key's extension. Large operational blobs — diagnostic bundles, job
// artifacts — are written this way.
//
// The copy observes ctx. A local reader never blocks on the network, so without
// this an upload deadline would elapse unnoticed and the object would still be
// renamed into place; the admin job runner's upload timeout would be inert for
// exactly the large exports it exists to bound.
func (f *Filesystem) PutStream(ctx context.Context, key string, r io.Reader, _ string) error {
	return f.publish(ctx, key, func(w io.Writer) error {
		_, err := io.Copy(w, &contextReader{ctx: ctx, r: r})
		return err
	})
}

// contextReader fails the next read once ctx is done, so a canceled write stops
// instead of running to the end of its reader. publish discards the temporary
// file on any error, so nothing partial is published.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// publish writes an object through a temporary file and renames it into place,
// so a crash after it returns cannot leave a caller referencing a key the store
// does not show. write fills the temporary file.
func (f *Filesystem) publish(ctx context.Context, key string, write func(io.Writer) error) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := os.OpenRoot(f.root)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err = check(root, key); err != nil {
		return err
	}
	created, err := mkdirAllTracked(root, path.Dir(key))
	if err != nil {
		return err
	}
	tmp, name, err := temporary(root, path.Dir(key), ".tmp-")
	if err != nil {
		return err
	}
	defer func() { _ = tmp.Close(); _ = root.Remove(name) }()
	if err = tmp.Chmod(0644); err == nil {
		err = write(tmp)
	}
	if err == nil {
		err = tmp.Sync()
	}

	if err != nil {
		return err
	}
	if err = root.Rename(name, key); err != nil {
		return err
	}
	// The data is durable once the file is synced, but the name is not until
	// the directory entry is. A crash between the rename and the directory
	// flush would leave the catalog referencing a key the store never shows.
	// Directories this write created are new entries in their parents, so
	// each parent is flushed too, from the leaf up to the first one that
	// already existed.
	if err = syncDir(root, path.Dir(key)); err != nil {
		return err
	}
	for _, dir := range created {
		if err = syncDir(root, path.Dir(dir)); err != nil {
			return err
		}
	}
	return nil
}

// mkdirAllTracked creates dir and any missing ancestors inside root and
// returns the directories it created, deepest first, so the caller can sync
// each one's parent after publishing a file.
func mkdirAllTracked(root *os.Root, dir string) ([]string, error) {
	var missing []string
	for probe := dir; probe != "." && probe != "/" && probe != ""; probe = path.Dir(probe) {
		if _, err := root.Stat(probe); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		missing = append(missing, probe)
	}
	if len(missing) == 0 {
		return nil, nil
	}
	if err := root.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return missing, nil
}

// syncDir flushes a directory's entries so a published rename survives a
// crash. Filesystems that do not support fsync on directories report EINVAL
// or ENOTSUP; the rename itself already ordered the write there.
func syncDir(root *os.Root, dir string) error {
	d, err := root.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
		return err
	}
	return nil
}
func fileInfo(key string, info os.FileInfo) ObjectInfo {
	return ObjectInfo{Key: key, Size: info.Size(), ModTime: info.ModTime(), ETag: "\"" + strconv.FormatInt(info.Size(), 10) + "-" + strconv.FormatInt(info.ModTime().UnixNano(), 36) + "\""}
}
func (f *Filesystem) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	if err := ValidateKey(key); err != nil {
		return nil, ObjectInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, ObjectInfo{}, err
	}
	root, err := os.OpenRoot(f.root)
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	defer func() { _ = root.Close() }()
	if err = check(root, key); err != nil {
		return nil, ObjectInfo{}, err
	}
	before, err := root.Lstat(key)
	if os.IsNotExist(err) {
		return nil, ObjectInfo{}, ErrNotFound
	}
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	if !before.Mode().IsRegular() {
		return nil, ObjectInfo{}, ErrNotFound
	}
	file, err := root.Open(key)
	if os.IsNotExist(err) {
		err = ErrNotFound
	}
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	info, err := file.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = ErrNotFound
	}
	if err != nil {
		_ = file.Close()
		return nil, ObjectInfo{}, err
	}
	return file, fileInfo(key, info), nil
}
func (f *Filesystem) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	if err := ValidateKey(key); err != nil {
		return ObjectInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	root, err := os.OpenRoot(f.root)
	if err != nil {
		return ObjectInfo{}, err
	}
	defer func() { _ = root.Close() }()
	if err = check(root, key); err != nil {
		return ObjectInfo{}, err
	}
	info, err := root.Lstat(key)
	if os.IsNotExist(err) {
		err = ErrNotFound
	}
	if err != nil {
		return ObjectInfo{}, err
	}
	if !info.Mode().IsRegular() {
		return ObjectInfo{}, ErrNotFound
	}
	return fileInfo(key, info), nil
}
func prune(root *os.Root, dir string) {
	for dir != "." {
		if err := root.Remove(dir); err != nil {
			return
		}
		dir = path.Dir(dir)
	}
}
func (f *Filesystem) Delete(ctx context.Context, keys []string) (int, error) {
	for _, key := range keys {
		if err := ValidateKey(key); err != nil {
			return 0, err
		}
	}
	root, err := os.OpenRoot(f.root)
	if err != nil {
		return 0, err
	}
	defer func() { _ = root.Close() }()
	n := 0
	for _, key := range keys {
		if err = ctx.Err(); err != nil {
			return n, err
		}
		if err = check(root, key); err != nil {
			return n, err
		}
		err = root.Remove(key)
		if err != nil && !os.IsNotExist(err) {
			return n, err
		}
		n++
		prune(root, path.Dir(key))
	}
	return n, nil
}
func (f *Filesystem) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	prefix = strings.TrimSuffix(prefix, "/")
	if err := ValidateKey(prefix); err != nil {
		return 0, err
	}
	root, err := os.OpenRoot(f.root)
	if err != nil {
		return 0, err
	}
	defer func() { _ = root.Close() }()
	if err = check(root, prefix); err != nil {
		return 0, err
	}
	n := 0
	err = fs.WalkDir(root.FS(), prefix, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		if d.Type().IsRegular() {
			n++
		}
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if err = root.RemoveAll(prefix); err != nil {
		return 0, err
	}
	prune(root, path.Dir(prefix))
	return n, nil
}
func (f *Filesystem) List(ctx context.Context, prefix, cursor string, limit int) ([]ObjectInfo, string, error) {
	if prefix != "" {
		if err := ValidateKey(strings.TrimSuffix(prefix, "/")); err != nil {
			return nil, "", err
		}
	}
	root, err := os.OpenRoot(f.root)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = root.Close() }()
	prefix = strings.TrimSuffix(prefix, "/")
	start := "."
	if prefix != "" {
		if err = check(root, prefix); err != nil {
			return nil, "", err
		}
		start = prefix
	}
	out := []ObjectInfo{}
	// Sort each visited directory by full key order, skipping cursor subtrees.
	// Stop after the first object beyond the requested page.
	stop := errors.New("artwork list page complete")
	cleanupBudget := 32
	var walk func(string) error
	walk = func(p string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := root.Lstat(p)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if info.IsDir() {
			if p != "." && p+"/" <= cursor && !strings.HasPrefix(cursor, p+"/") {
				return nil
			}
			dir, err := root.Open(p)
			if err != nil {
				return err
			}
			entries, err := dir.ReadDir(-1)
			_ = dir.Close()
			if err != nil {
				return err
			}
			order := func(e os.DirEntry) string {
				if e.IsDir() {
					return e.Name() + "/"
				}
				return e.Name()
			}
			sort.Slice(entries, func(i, j int) bool { return order(entries[i]) < order(entries[j]) })
			for _, entry := range entries {
				if err := ctx.Err(); err != nil {
					return err
				}
				child := path.Join(p, entry.Name())
				if strings.HasPrefix(entry.Name(), ".tmp-") || strings.HasPrefix(entry.Name(), ".probe-") {
					if cleanupBudget > 0 && cleanupTemporary(root, child) {
						cleanupBudget--
					}
					continue
				}
				if entry.IsDir() {
					if child+"/" <= cursor && !strings.HasPrefix(cursor, child+"/") {
						continue
					}
				} else if child <= cursor {
					continue
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		} else if info.Mode().IsRegular() && p > cursor {
			out = append(out, fileInfo(p, info))
			if limit > 0 && len(out) > limit {
				return stop
			}
		}
		return nil
	}
	err = walk(start)
	if os.IsNotExist(err) {
		return []ObjectInfo{}, "", nil
	}
	if err != nil && !errors.Is(err, stop) {
		return nil, "", err
	}
	next := ""
	if limit > 0 && len(out) > limit {
		next = out[limit-1].Key
		out = out[:limit]
	}
	return out, next, nil
}
func (f *Filesystem) Probe(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ensureRoot(f.root); err != nil {
		return err
	}
	root, err := os.OpenRoot(f.root)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	file, name, err := temporary(root, ".", ".probe-")
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(name) }()
	_, err = file.Write([]byte("ok"))
	if err == nil {
		err = file.Sync()
	}
	if e := file.Close(); err == nil {
		err = e
	}
	if err == nil {
		cleanupRootTemporary(ctx, root)
	}
	return err
}

// Identity is the absolute root, so two mounts of different directories never
// share a catalog by accident.
func (f *Filesystem) Identity() string { return BackendLocal + "|" + f.root }

var _ Store = (*Filesystem)(nil)

// Remove only abandoned temporary files. Writers hold the lock
// until publication, including slow writes whose modification time is old.
func cleanupTemporary(root *os.Root, name string) bool {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || time.Since(info.ModTime()) < 24*time.Hour {
		return false
	}
	file, err := root.Open(name)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	if unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return false
	}
	locked, err := file.Stat()
	if err != nil || !os.SameFile(info, locked) {
		return false
	}
	current, err := root.Lstat(name)
	if err == nil && os.SameFile(locked, current) && time.Since(current.ModTime()) >= 24*time.Hour {
		return root.Remove(name) == nil
	}
	return false
}

// Temporary files in the root lie outside the namespaces visited by the sweeper. Stream
// root entries so cleanup does not allocate a complete directory listing. Fresh
// and locked files do not consume the deletion quota.
func cleanupRootTemporary(ctx context.Context, root *os.Root) {
	dir, err := root.Open(".")
	if err != nil {
		return
	}
	defer func() { _ = dir.Close() }()
	remaining := 32
	for remaining > 0 && ctx.Err() == nil {
		entries, err := dir.ReadDir(32)
		for _, entry := range entries {
			if ctx.Err() != nil {
				return
			}
			if (strings.HasPrefix(entry.Name(), ".probe-") || strings.HasPrefix(entry.Name(), ".tmp-")) && cleanupTemporary(root, entry.Name()) {
				remaining--
				if remaining == 0 {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}
