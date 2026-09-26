package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Silo-Server/silo-server/internal/s3client"
)

// LocalBucket is the bucket name a filesystem-backed store reports. Callers
// that persist an object's location — admin_jobs.artifact_bucket,
// client_diagnostic_reports.blob_bucket — record it and hand it back on read,
// where BucketAPI ignores it. It only has to be non-empty and honest: readers
// treat an empty bucket as "storage unavailable".
const LocalBucket = BackendLocal

// ErrNoPresign reports that a store cannot mint a self-authorizing URL for an
// object. Callers fall back to streaming the bytes through the API, or to a
// signed route of their own. A filesystem has no equivalent of a presigned S3
// URL, so this is the expected answer on a local backend, not a failure.
var ErrNoPresign = errors.New("blobstore: backend cannot presign object URLs")

// BucketAPI adapts a Store to the bucket-shaped object API that diagnostics,
// admin jobs, and catalog seed were written against for S3. Those callers pass
// the bucket an object was written to so a bucket change does not orphan it; a
// filesystem has one location, so the argument is accepted and ignored.
//
// Only a local backend needs this. S3 deployments keep passing *s3client.Client
// straight through, so their code path is unchanged.
type BucketAPI struct{ store Store }

func NewBucketAPI(store Store) *BucketAPI {
	if store == nil {
		return nil
	}
	return &BucketAPI{store: store}
}

// Store exposes the underlying store for callers that want the key-only API.
func (b *BucketAPI) Store() Store { return b.store }

func (b *BucketAPI) Bucket() string { return LocalBucket }

func (b *BucketAPI) PutObject(ctx context.Context, _, key string, data []byte) error {
	return b.store.Put(ctx, key, data)
}

func (b *BucketAPI) PutStream(ctx context.Context, _, key string, r io.Reader, contentType string) error {
	return b.store.PutStream(ctx, key, r, contentType)
}

func (b *BucketAPI) GetObject(ctx context.Context, _, key string) ([]byte, error) {
	return GetBytes(ctx, b.store, key)
}

func (b *BucketAPI) GetObjectStream(ctx context.Context, _, key string) (io.ReadCloser, error) {
	body, _, err := b.store.Get(ctx, key)
	return body, err
}

func (b *BucketAPI) DeleteObject(ctx context.Context, _, key string) error {
	_, err := b.store.Delete(ctx, []string{key})
	return err
}

// UploadFile publishes a file already on disk. It streams rather than reading
// the file in, because catalog exports are gzipped catalogs, not thumbnails.
func (b *BucketAPI) UploadFile(ctx context.Context, _, key, path, contentType string) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if err := b.store.PutStream(ctx, key, file, contentType); err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// ListObjects returns every key under prefix. Diagnostics cleanup walks its own
// namespace this way; the listing is bounded by the prefix, never the root.
func (b *BucketAPI) ListObjects(ctx context.Context, _ string, prefix string) ([]string, error) {
	infos, _, err := b.store.List(ctx, prefix, "", 0)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(infos))
	for _, info := range infos {
		keys = append(keys, info.Key)
	}
	return keys, nil
}

func (b *BucketAPI) ListObjectInfos(ctx context.Context, _, prefix string) ([]s3client.ObjectInfo, error) {
	infos, _, err := b.store.List(ctx, prefix, "", 0)
	if err != nil {
		return nil, err
	}
	return toS3ObjectInfos(infos), nil
}

func (b *BucketAPI) ListObjectInfosPage(ctx context.Context, _, prefix, cursor string, limit int) ([]s3client.ObjectInfo, string, error) {
	infos, next, err := b.store.List(ctx, prefix, cursor, limit)
	if err != nil {
		return nil, "", err
	}
	return toS3ObjectInfos(infos), next, nil
}

// PresignGetURL always fails. A filesystem cannot hand a browser a URL that
// authorizes itself; callers answer with a streamed response or a signed route.
func (b *BucketAPI) PresignGetURL(context.Context, string, string, time.Duration) (string, error) {
	return "", fmt.Errorf("%w: %s", ErrNoPresign, b.store.Identity())
}

// SupportsPresign lets callers ask before trying, so a feature that only
// storage-side presigning can provide is hidden rather than offered and failed.
func (b *BucketAPI) SupportsPresign() bool { return false }

func toS3ObjectInfos(infos []ObjectInfo) []s3client.ObjectInfo {
	out := make([]s3client.ObjectInfo, 0, len(infos))
	for _, info := range infos {
		modified := info.ModTime
		out = append(out, s3client.ObjectInfo{
			Key: info.Key, SizeBytes: info.Size, LastModified: &modified, ETag: info.ETag,
		})
	}
	return out
}
