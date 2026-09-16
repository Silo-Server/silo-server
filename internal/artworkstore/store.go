// Package artworkstore stores logical artwork keys in local or S3 storage.
package artworkstore

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrNotFound   = errors.New("artworkstore: object not found")
	ErrInvalidKey = errors.New("artworkstore: invalid key")
)

const (
	BackendLocal = "local"
	BackendS3    = "s3"
)

// IdentitySettingKey is the server_settings row that records the storage the
// catalog's artwork keys belong to. The first successful write records the
// store's Identity; Open refuses a store whose Identity differs.
const IdentitySettingKey = "artwork.storage_identity"

type DirectURLer interface {
	DirectURL(ctx context.Context, key string, ttl time.Duration) (string, error)
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
