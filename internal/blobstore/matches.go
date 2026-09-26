package blobstore

import (
	"bytes"
	"context"
	"errors"
	"io"
)

// Matches verifies content before an immutable object is reused.
func (f *Filesystem) Matches(ctx context.Context, key string, data []byte) (bool, error) {
	reader, info, err := f.Get(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = reader.Close() }()
	if info.Size != int64(len(data)) {
		return false, nil
	}
	stored, err := io.ReadAll(io.LimitReader(reader, int64(len(data))+1))
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return bytes.Equal(stored, data), nil
}

// Matches uses the checksum recorded in S3 object metadata.
func (s *S3) Matches(ctx context.Context, key string, data []byte) (bool, error) {
	if err := ValidateKey(key); err != nil {
		return false, err
	}
	return s.client.ObjectMatches(ctx, s.client.Bucket(), key, data)
}

func (s *recordingStore) Matches(ctx context.Context, key string, data []byte) (bool, error) {
	if matcher, ok := s.Store.(interface {
		Matches(context.Context, string, []byte) (bool, error)
	}); ok {
		matched, err := matcher.Matches(ctx, key, data)
		if err != nil || !matched {
			return matched, err
		}
		if err := s.recordBackend(ctx); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}
