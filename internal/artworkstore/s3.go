package artworkstore

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/s3client"
)

type S3 struct{ client *s3client.Client }

func NewS3(client *s3client.Client) *S3 { return &S3{client: client} }
func (s *S3) Put(ctx context.Context, key string, data []byte) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	return s.client.PutObject(ctx, s.client.Bucket(), key, data)
}
func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	if err := ValidateKey(key); err != nil {
		return nil, ObjectInfo{}, err
	}
	r, info, err := s.client.GetObjectStreamInfo(ctx, s.client.Bucket(), key)
	if errors.Is(err, s3client.ErrNotFound) {
		return nil, ObjectInfo{}, ErrNotFound
	}
	if err != nil {
		return nil, ObjectInfo{}, err
	}
	return r, fromS3ObjectInfo(info), nil
}
func (s *S3) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	if err := ValidateKey(key); err != nil {
		return ObjectInfo{}, err
	}
	info, err := s.client.HeadObject(ctx, s.client.Bucket(), key)
	if err != nil {
		if errors.Is(err, s3client.ErrNotFound) {
			return ObjectInfo{}, ErrNotFound
		}
		return ObjectInfo{}, err
	}
	return fromS3ObjectInfo(info), nil
}
func (s *S3) Delete(ctx context.Context, keys []string) (int, error) {
	for _, key := range keys {
		if err := ValidateKey(key); err != nil {
			return 0, err
		}
	}
	return s.client.DeleteObjects(ctx, s.client.Bucket(), keys)
}
func (s *S3) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	prefix = strings.TrimSuffix(prefix, "/")
	if err := ValidateKey(prefix); err != nil {
		return 0, err
	}
	return s.client.DeletePrefix(ctx, s.client.Bucket(), prefix+"/")
}
func (s *S3) List(ctx context.Context, prefix, cursor string, limit int) ([]ObjectInfo, string, error) {
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix != "" {
		if err := ValidateKey(prefix); err != nil {
			return nil, "", err
		}
	}
	if prefix != "" {
		prefix += "/"
	}
	out := make([]ObjectInfo, 0)
	for {
		pageSize := 1000
		if limit > 0 && limit-len(out) < pageSize {
			pageSize = limit - len(out)
		}
		items, next, err := s.client.ListObjectInfosAfter(ctx, s.client.Bucket(), prefix, cursor, pageSize)
		if err != nil {
			return nil, "", err
		}
		for _, item := range items {
			out = append(out, fromS3ObjectInfo(item))
		}
		if next == "" || (limit > 0 && len(out) >= limit) {
			return out, next, nil
		}
		cursor = next
	}
}
func (s *S3) Probe(ctx context.Context) error { return s.client.HeadBucket(ctx, s.client.Bucket()) }
func (s *S3) DirectURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	return s.client.PresignGetURL(ctx, s.client.Bucket(), key, ttl)
}
func (s *S3) ObjectAvailable(ctx context.Context, key string) (bool, error) {
	return s.client.ObjectAvailable(ctx, s.client.Bucket(), key)
}

// Identity covers the endpoint, bucket, and key prefix. Endpoints and bucket
// names are case-insensitive; the key prefix feeds case-sensitive object keys
// and is normalized exactly as the client applies it.
func (s *S3) Identity() string {
	insensitive := func(v string) string { return strings.ToLower(strings.TrimSpace(v)) }
	return BackendS3 + "|" + insensitive(s.client.Endpoint()) + "|" + insensitive(s.client.Bucket()) + "|" + s.client.KeyPrefix()
}

var _ Store = (*S3)(nil)
var _ DirectURLer = (*S3)(nil)

func fromS3ObjectInfo(info s3client.ObjectInfo) ObjectInfo {
	out := ObjectInfo{Key: info.Key, Size: info.SizeBytes, ETag: info.ETag}
	if info.LastModified != nil {
		out.ModTime = *info.LastModified
	}
	return out
}
