// Package blobstore stores logical object keys in local or S3 storage. It backs
// artwork, branding assets, intro/credit markers, chapter thumbnails, and
// profile avatars. Downloaded subtitles, diagnostic bundles, and job artifacts
// still use bucket-oriented code and move onto it in later changes. Callers own
// their key namespaces; see docs/architecture/blob-storage.md for the reserved
// prefixes that keep them apart.
package blobstore

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrNotFound   = errors.New("blobstore: object not found")
	ErrInvalidKey = errors.New("blobstore: invalid key")
)

const (
	BackendLocal = "local"
	BackendS3    = "s3"
)

// IdentitySettingKey is the server_settings row that records the storage the
// catalog's keys belong to. The first successful write records the store's
// Identity; Open refuses a store whose Identity differs. The row keeps its
// original artwork-era name so existing deployments need no migration; it
// governs the assets store, which on a local backend is the whole root.
const IdentitySettingKey = "artwork.storage_identity"

// OperationalIdentitySettingKey records the configured private bucket that
// holds diagnostic bundles, job artifacts, and avatars. Startup seeds it for
// buckets written by older releases and refuses a different private location.
// Private writes do not record the assets IdentitySettingKey.
const OperationalIdentitySettingKey = "storage.operational_identity"

// DirectURLer hands clients a URL that reads an object from the backend
// without this server.
type DirectURLer interface {
	// DirectURL returns a read URL for key and the time it stops working.
	// Calls within the same window, on any replica, return the same URL; a zero
	// window issues a fresh URL per call.
	DirectURL(ctx context.Context, key string, ttl, window time.Duration) (string, time.Time, error)
}

type ObjectInfo struct {
	Key     string
	Size    int64
	ModTime time.Time
	ETag    string
}
type Store interface {
	// Put idempotently overwrites an object. Content matching is not required.
	Put(ctx context.Context, key string, data []byte) error
	// PutStream is Put for an object too large to hold in memory. A local
	// backend ignores contentType and derives a media type from the key.
	//
	// Every write method must be forwarded by recordingStore, or a store whose
	// first write arrives here would never record its identity.
	PutStream(ctx context.Context, key string, r io.Reader, contentType string) error
	Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error)
	Stat(ctx context.Context, key string) (ObjectInfo, error)
	// Delete counts absent keys as deleted, matching S3 batch deletion.
	Delete(ctx context.Context, keys []string) (int, error)
	// DeletePrefix removes a directory subtree and counts removed regular files.
	DeletePrefix(ctx context.Context, prefix string) (int, error)
	// List returns lexical key order. Cursor is the last returned key, or empty
	// at the end. An empty prefix lists the store; a nonpositive limit lists all.
	List(ctx context.Context, prefix, cursor string, limit int) ([]ObjectInfo, string, error)
	// Probe checks storage access. Callers cache readiness probes for 30 seconds.
	Probe(ctx context.Context) error
	// Identity names where objects live: the backend followed by the fields
	// that select a location (a local root, or an S3 endpoint, bucket, and key
	// prefix). Delivery settings such as a public read endpoint do not
	// participate, so changing how objects are served never reads as a move.
	Identity() string
}
