package blobstore

import (
	"context"
	"errors"
	"io"
	"net/url"
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
func (s *S3) PutStream(ctx context.Context, key string, r io.Reader, contentType string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	return s.client.PutObjectStream(ctx, s.client.Bucket(), key, r, contentType)
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
func (s *S3) BeginMutationFence(ctx context.Context) (func(), error) {
	return s.client.BeginMutationFence(ctx)
}

// Identity covers the endpoint, bucket, and key prefix. Bucket names and the
// endpoint's scheme and host are case-insensitive; an endpoint path (a
// gateway tenant, for example) and the key prefix address case-sensitive
// storage and are kept as configured, the prefix normalized exactly as the
// client applies it.
func (s *S3) Identity() string {
	return BackendS3 + "|" + normalizeEndpoint(s.client.Endpoint()) + "|" + strings.ToLower(strings.TrimSpace(s.client.Bucket())) + "|" + s.client.KeyPrefix()
}

// normalizeEndpoint lowercases only the case-insensitive parts of an endpoint
// URL. An endpoint that does not parse is lowercased whole, as before.
// NormalizeEndpoint is the endpoint form Identity uses, for callers comparing
// configured endpoints the way the store will.
func NormalizeEndpoint(raw string) string { return normalizeEndpoint(raw) }

func normalizeEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return strings.ToLower(raw)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String()
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
