package artworkstore

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Silo-Server/silo-server/internal/s3client"
)

// S3Client is the subset of *s3client.Client the artwork adapter uses. It is an
// interface so the adapter is testable without a live backend, and so the rest
// of the codebase cannot reach the bucket through the store.
type S3Client interface {
	Bucket() string
	PutObject(ctx context.Context, bucket, key string, data []byte) error
	GetObjectStream(ctx context.Context, bucket, key string) (io.ReadCloser, error)
	StatObject(ctx context.Context, bucket, key string) (s3client.ObjectInfo, error)
	ObjectMatches(ctx context.Context, bucket, key string, data []byte) (bool, error)
	DeleteObjects(ctx context.Context, bucket string, keys []string) (int, error)
	HeadBucket(ctx context.Context, bucket string) error
	ListObjectInfosPage(ctx context.Context, bucket, prefix, cursor string, limit int) ([]s3client.ObjectInfo, string, bool, error)
	DeletePrefix(ctx context.Context, bucket, prefix string) (int, error)
}

// S3Store is the canonical artwork store backed by an S3-compatible bucket.
//
// The bucket name, the client's key prefix, the endpoint, and the URL-auth
// strategy are all private to this adapter: callers address objects by logical
// key exactly as they do on the filesystem store, so the same logical tree can
// be copied between a bucket and a directory without rewriting a single catalog
// reference.
type S3Store struct {
	client S3Client
	bucket string
}

var _ Store = (*S3Store)(nil)

// NewS3Store returns an artwork store over an S3 bucket client.
func NewS3Store(client S3Client) (*S3Store, error) {
	if client == nil {
		return nil, errors.New("artworkstore: s3 client is required")
	}
	bucket := client.Bucket()
	if bucket == "" {
		return nil, errors.New("artworkstore: s3 client has no bucket configured")
	}
	return &S3Store{client: client, bucket: bucket}, nil
}

// WriteImmutable uploads data at key.
//
// Unlike the filesystem store it does not pre-read the object to enforce
// ErrContentMismatch. Two reasons: the pipeline already asks Matches before
// writing, so the extra HEAD would only double the request count on the hot
// path; and objects written before the client recorded content checksums report
// no match at all, so refusing to overwrite would make them permanently
// unhealable. Content addressing is what keeps the write idempotent here.
func (s *S3Store) WriteImmutable(ctx context.Context, key string, data []byte, _ ObjectMetadata) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	// Content type is derived from the key by the client itself, which keeps
	// the header identical for objects written through any other S3 path.
	return s.client.PutObject(ctx, s.bucket, key, data)
}

// Open streams the object at key. The body is a network stream, so it does not
// implement io.ReadSeeker and delivery streams it rather than serving ranges
// from it.
func (s *S3Store) Open(ctx context.Context, key string) (*Object, error) {
	if err := ValidateKey(key); err != nil {
		return nil, err
	}
	info, err := s.Stat(ctx, key)
	if err != nil {
		return nil, err
	}
	body, err := s.client.GetObjectStream(ctx, s.bucket, key)
	if err != nil {
		return nil, s.translate(key, err)
	}
	return &Object{Info: info, Body: body}, nil
}

// Stat returns object metadata without transferring the body.
func (s *S3Store) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	if err := ValidateKey(key); err != nil {
		return ObjectInfo{}, err
	}
	raw, err := s.client.StatObject(ctx, s.bucket, key)
	if err != nil {
		return ObjectInfo{}, s.translate(key, err)
	}
	info := ObjectInfo{
		Key:       key,
		SizeBytes: raw.SizeBytes,
		MediaType: raw.ContentType,
		ETag:      quotedETag(raw.ETag),
	}
	if info.MediaType == "" {
		info.MediaType = MediaTypeForKey(key)
	}
	if raw.LastModified != nil {
		info.ModTime = *raw.LastModified
	}
	if info.ETag == "" {
		info.ETag = entityTag(key, info.SizeBytes, info.ModTime)
	}
	return info, nil
}

// Matches reports whether the object already holds exactly these bytes. Objects
// stored before the client recorded a content checksum report false and are
// rewritten by the caller, which is the safe direction.
func (s *S3Store) Matches(ctx context.Context, key string, data []byte) (bool, error) {
	if err := ValidateKey(key); err != nil {
		return false, err
	}
	matched, err := s.client.ObjectMatches(ctx, s.bucket, key, data)
	if err != nil {
		return false, fmt.Errorf("artworkstore: comparing %s: %w", key, err)
	}
	return matched, nil
}

// DeleteObjects removes every key, batching through the client. Already-absent
// keys count as deleted, matching the filesystem store and the strict count
// check the revision GC performs.
func (s *S3Store) DeleteObjects(ctx context.Context, keys []string) (int, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	for _, key := range keys {
		if err := ValidateKey(key); err != nil {
			return 0, err
		}
	}
	deleted, err := s.client.DeleteObjects(ctx, s.bucket, keys)
	if err != nil {
		return deleted, fmt.Errorf("artworkstore: deleting %d objects: %w", len(keys), err)
	}
	return deleted, nil
}

// Probe verifies the bucket is reachable and accessible.
func (s *S3Store) Probe(ctx context.Context) error {
	if err := s.client.HeadBucket(ctx, s.bucket); err != nil {
		return fmt.Errorf("artworkstore: probing s3 bucket: %w", err)
	}
	return nil
}

func (s *S3Store) ListPage(ctx context.Context, prefix, cursor string, limit int) ([]ObjectInfo, string, bool, error) {
	objects, next, done, err := s.client.ListObjectInfosPage(ctx, s.bucket, prefix, cursor, limit)
	if err != nil {
		return nil, cursor, false, err
	}
	result := make([]ObjectInfo, 0, len(objects))
	for _, object := range objects {
		info := ObjectInfo{Key: object.Key, SizeBytes: object.SizeBytes}
		if object.LastModified != nil {
			info.ModTime = *object.LastModified
		}
		result = append(result, info)
	}
	return result, next, done, nil
}

func (s *S3Store) DeletePrefixMaintenance(ctx context.Context, prefix string) (int, error) {
	if err := validateLegacyMaintenancePrefix(prefix); err != nil {
		return 0, err
	}
	return s.client.DeletePrefix(ctx, s.bucket, prefix)
}

// translate maps client errors onto the store's sentinels so callers can treat
// a missing object identically on every backend.
func (s *S3Store) translate(key string, err error) error {
	if errors.Is(err, s3client.ErrNotFound) {
		return fmt.Errorf("artworkstore: %s: %w", key, ErrNotFound)
	}
	return err
}

// quotedETag normalizes an S3 ETag into HTTP entity-tag form. S3 returns it
// already quoted; a backend that omits the quotes would otherwise emit an
// invalid header.
func quotedETag(raw string) string {
	if raw == "" {
		return ""
	}
	if raw[0] == '"' || raw[0] == 'W' {
		return raw
	}
	return `"` + raw + `"`
}
