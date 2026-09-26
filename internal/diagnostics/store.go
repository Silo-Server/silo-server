package diagnostics

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

var ErrObjectNotFound = errors.New("diagnostics object not found")

// ObjectStore is the diagnostics-owned object storage surface. It is narrow
// enough for fake-backed ingest/admin tests and wraps either the private S3
// bucket or the local blob store in production. The bucket argument exists so a
// report written to one bucket still reads from it after the configured bucket
// changes; a local store has one location and ignores it.
type ObjectStore interface {
	PutStream(ctx context.Context, bucket, key string, r io.Reader, contentType string) error
	GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error)
	DeleteObject(ctx context.Context, bucket, key string) error
	ListObjects(ctx context.Context, prefix string) ([]string, error)
	PresignGetURL(ctx context.Context, bucket, key string, expiry time.Duration) (string, error)
	Bucket() string
}

// localObjectStore backs diagnostics with the filesystem blob store. It has no
// presigned URLs, so PresignReportDownload fails and both the v1 handler and
// the v2 route stream the bundle through the API instead — which the v2 route
// already does unconditionally.
type localObjectStore struct {
	api *blobstore.BucketAPI
}

// NewLocalObjectStore adapts a blob store to the diagnostics store interface.
// A nil store returns nil so interface-nil checks remain reliable.
func NewLocalObjectStore(store blobstore.Store) ObjectStore {
	api := blobstore.NewBucketAPI(store)
	if api == nil {
		return nil
	}
	return &localObjectStore{api: api}
}

func (s *localObjectStore) PutStream(ctx context.Context, bucket, key string, r io.Reader, contentType string) error {
	return normalizeObjectStoreError(s.api.PutStream(ctx, bucket, key, r, contentType))
}

func (s *localObjectStore) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	body, err := s.api.GetObjectStream(ctx, bucket, key)
	if err != nil {
		return nil, normalizeObjectStoreError(err)
	}
	return body, nil
}

func (s *localObjectStore) DeleteObject(ctx context.Context, bucket, key string) error {
	return normalizeObjectStoreError(s.api.DeleteObject(ctx, bucket, key))
}

func (s *localObjectStore) ListObjects(ctx context.Context, prefix string) ([]string, error) {
	keys, err := s.api.ListObjects(ctx, s.api.Bucket(), prefix)
	if err != nil {
		return nil, normalizeObjectStoreError(err)
	}
	return keys, nil
}

func (s *localObjectStore) PresignGetURL(ctx context.Context, bucket, key string, expiry time.Duration) (string, error) {
	return s.api.PresignGetURL(ctx, bucket, key, expiry)
}

func (s *localObjectStore) Bucket() string { return s.api.Bucket() }

type s3ObjectStore struct {
	client *s3client.Client
}

// NewS3ObjectStore adapts the private S3 client to the diagnostics store
// interface. A nil client returns nil so interface-nil checks remain reliable.
func NewS3ObjectStore(client *s3client.Client) ObjectStore {
	if client == nil {
		return nil
	}
	return &s3ObjectStore{client: client}
}

func (s *s3ObjectStore) PutStream(ctx context.Context, bucket, key string, r io.Reader, contentType string) error {
	return normalizeObjectStoreError(s.client.PutObjectStream(ctx, bucket, key, r, contentType))
}

func (s *s3ObjectStore) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	body, err := s.client.GetObjectStream(ctx, bucket, key)
	if err != nil {
		return nil, normalizeObjectStoreError(err)
	}
	return body, nil
}

func (s *s3ObjectStore) DeleteObject(ctx context.Context, bucket, key string) error {
	return normalizeObjectStoreError(s.client.DeleteObject(ctx, bucket, key))
}

func (s *s3ObjectStore) ListObjects(ctx context.Context, prefix string) ([]string, error) {
	keys, err := s.client.ListObjects(ctx, s.client.Bucket(), prefix)
	if err != nil {
		return nil, normalizeObjectStoreError(err)
	}
	return keys, nil
}

func (s *s3ObjectStore) PresignGetURL(ctx context.Context, bucket, key string, expiry time.Duration) (string, error) {
	url, err := s.client.PresignGetURL(ctx, bucket, key, expiry)
	if err != nil {
		return "", normalizeObjectStoreError(err)
	}
	return url, nil
}

func (s *s3ObjectStore) EffectivePresignTTL(requested time.Duration) time.Duration {
	return s.client.EffectivePresignTTL(requested)
}

func (s *s3ObjectStore) Bucket() string {
	return s.client.Bucket()
}

func normalizeObjectStoreError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, s3client.ErrNotFound) || errors.Is(err, blobstore.ErrNotFound) {
		return ErrObjectNotFound
	}
	return err
}

func IsObjectNotFound(err error) bool {
	return errors.Is(err, ErrObjectNotFound) || errors.Is(err, s3client.ErrNotFound) || errors.Is(err, blobstore.ErrNotFound)
}
