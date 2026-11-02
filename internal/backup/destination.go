package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

// DestinationDefault is the operational blob store blobstore.Open returns on
// local storage.
const DestinationDefault = "default"

// Destination is where backup objects are written, listed, and deleted. Keys
// are full keys under RootPrefix. Nothing above this interface knows which
// implementation it has.
type Destination interface {
	Name() string
	// Put streams r to key. Nothing is spooled locally first.
	Put(ctx context.Context, key string, r io.Reader) error
	Open(ctx context.Context, key string) (io.ReadCloser, int64, error)
	List(ctx context.Context, prefix string) ([]blobstore.ObjectInfo, error)
	DeletePrefix(ctx context.Context, prefix string) error
	// Delete removes objects under RootPrefix; absent keys count as deleted.
	Delete(ctx context.Context, keys ...string) error
	Probe(ctx context.Context) error
	// FreeBytes reports the bytes available on a local filesystem
	// destination. ok is false for any other destination.
	FreeBytes(ctx context.Context) (free uint64, ok bool, err error)
	// DownloadURL presigns a read of key that needs no session. ok is false
	// when the destination cannot presign; the caller then streams the bytes.
	DownloadURL(ctx context.Context, key string, ttl time.Duration) (url string, expires time.Time, ok bool, err error)
	// Location names where key is, for an operator: a filesystem path.
	Location(key string) string
}

// StoreDestination writes backups into a blob store.
type StoreDestination struct {
	Store blobstore.Store
}

// NewStoreDestination is the default destination over the operational store.
// It returns nil when there is no operational store on local storage: an S3
// artwork backend keeps it in a bucket, or has none, and backups written
// there would not be encrypted.
//
// It returns the interface so a missing store is a nil Destination, never a
// non-nil one wrapping nothing.
func NewStoreDestination(store blobstore.Store) Destination { //nolint:iface // see above
	if store == nil || !strings.HasPrefix(blobstore.Location(store), blobstore.BackendLocal+"|") {
		return nil
	}
	return StoreDestination{Store: store}
}

func (d StoreDestination) Name() string { return DestinationDefault }

// Put writes key and leaves it readable by this account only: a backup holds
// password hashes and settings, and the store's own modes (0755 folders,
// 0644 files) would let any account on the host that can reach the store
// root read it. The backups folder is made private before the write, so the
// object is never readable by others, even while it is written.
func (d StoreDestination) Put(ctx context.Context, key string, r io.Reader) error {
	dir, local := d.localRoot()
	if !local {
		return d.Store.PutStream(ctx, key, r, "application/octet-stream")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	top := strings.TrimSuffix(RootPrefix, "/")
	if err := root.Mkdir(top, privateDirMode); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("creating the backups folder: %w", err)
	}
	if err := root.Chmod(top, privateDirMode); err != nil {
		return fmt.Errorf("restricting the backups folder: %w", err)
	}
	if err := d.Store.PutStream(ctx, key, r, "application/octet-stream"); err != nil {
		return err
	}
	if err := root.Chmod(key, privateFileMode); err != nil {
		return fmt.Errorf("restricting %s: %w", key, err)
	}
	for folder := path.Dir(key); folder != top && folder != "." && folder != "/"; folder = path.Dir(folder) {
		if err := root.Chmod(folder, privateDirMode); err != nil {
			return fmt.Errorf("restricting %s: %w", folder, err)
		}
	}
	return nil
}

// Modes of a local destination's backup folders and objects.
const (
	privateDirMode  fs.FileMode = 0o700
	privateFileMode fs.FileMode = 0o600
)

// localRoot is the directory a local store writes to.
func (d StoreDestination) localRoot() (string, bool) {
	root, ok := strings.CutPrefix(blobstore.Location(d.Store), blobstore.BackendLocal+"|")
	return root, ok && root != ""
}

func (d StoreDestination) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	body, info, err := d.Store.Get(ctx, key)
	if errors.Is(err, blobstore.ErrNotFound) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	return body, info.Size, nil
}

func (d StoreDestination) List(ctx context.Context, prefix string) ([]blobstore.ObjectInfo, error) {
	objects, _, err := d.Store.List(ctx, prefix, "", 0)
	return objects, err
}

func (d StoreDestination) DeletePrefix(ctx context.Context, prefix string) error {
	if err := checkDeletePrefix(prefix); err != nil {
		return err
	}
	_, err := d.Store.DeletePrefix(ctx, prefix)
	return err
}

func (d StoreDestination) Delete(ctx context.Context, keys ...string) error {
	if err := checkDeleteKeys(keys); err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	_, err := d.Store.Delete(ctx, keys)
	return err
}

func (d StoreDestination) Probe(ctx context.Context) error { return d.Store.Probe(ctx) }

func (d StoreDestination) FreeBytes(context.Context) (uint64, bool, error) {
	root, ok := d.localRoot()
	if !ok {
		return 0, false, nil
	}
	var st unix.Statfs_t
	if err := unix.Statfs(root, &st); err != nil {
		return 0, true, fmt.Errorf("reading free space at the backup destination: %w", err)
	}
	return blockBytes(st.Bavail, st.Bsize), true, nil
}

// blockBytes is blocks times a Statfs block size, whose type differs by
// platform (int64 on Linux, uint32 on macOS).
func blockBytes[S ~int32 | ~int64 | ~uint32 | ~uint64](blocks uint64, size S) uint64 {
	if size <= 0 {
		return 0
	}
	return blocks * uint64(size)
}

func (d StoreDestination) DownloadURL(ctx context.Context, key string, ttl time.Duration) (string, time.Time, bool, error) {
	direct, ok := d.Store.(blobstore.DirectURLer)
	if !ok {
		return "", time.Time{}, false, nil
	}
	url, expires, err := direct.DirectURL(ctx, key, ttl, 0)
	if err != nil {
		return "", time.Time{}, true, err
	}
	return url, expires, true, nil
}

func (d StoreDestination) Location(key string) string {
	where := blobstore.Location(d.Store)
	if root, ok := strings.CutPrefix(where, blobstore.BackendLocal+"|"); ok {
		path := filepath.Join(root, filepath.FromSlash(key))
		if strings.HasSuffix(key, "/") {
			path += string(filepath.Separator)
		}
		return path
	}
	return key
}

// checkDeletePrefix refuses a prefix that is not a folder under RootPrefix.
func checkDeletePrefix(prefix string) error {
	if !strings.HasPrefix(prefix, RootPrefix) || !strings.HasSuffix(prefix, "/") {
		return fmt.Errorf("backup: refusing to delete %q outside a backup folder", prefix)
	}
	return nil
}

// checkDeleteKeys refuses any key outside RootPrefix.
func checkDeleteKeys(keys []string) error {
	for _, key := range keys {
		if !strings.HasPrefix(key, RootPrefix) {
			return fmt.Errorf("backup: refusing to delete %q outside the backups folder", key)
		}
	}
	return nil
}
