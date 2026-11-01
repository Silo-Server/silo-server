package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
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

func (d StoreDestination) Put(ctx context.Context, key string, r io.Reader) error {
	return d.Store.PutStream(ctx, key, r, "application/octet-stream")
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
	root, ok := strings.CutPrefix(blobstore.Location(d.Store), blobstore.BackendLocal+"|")
	if !ok || root == "" {
		return 0, false, nil
	}
	var st unix.Statfs_t
	if err := unix.Statfs(root, &st); err != nil {
		return 0, true, fmt.Errorf("reading free space at the backup destination: %w", err)
	}
	return uint64(st.Bavail) * uint64(st.Bsize), true, nil //nolint:unconvert // field widths differ by platform
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
