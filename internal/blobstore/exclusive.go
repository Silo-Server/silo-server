package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/s3client"
)

// ErrExists reports an exclusive create that found an object at its key.
// Nothing was written.
var ErrExists = errors.New("blobstore: object already exists")

// ExclusiveCreator creates an object only when none exists at its key,
// atomically: a concurrent writer of the same key is never overwritten.
type ExclusiveCreator interface {
	CreateExclusive(ctx context.Context, key string, r io.Reader, contentType string) error
}

// ErrExclusiveUnsupported reports a store that cannot create an object
// atomically only when none exists: a bucket that rejects conditional writes,
// or a store without ExclusiveCreator. Nothing was written. A check followed
// by a write could replace an object another writer published between the
// two, so there is no fallback.
var ErrExclusiveUnsupported = errors.New("blobstore: this storage cannot create an object only when none exists")

// CreateExclusive creates key from r only when nothing is there, returning
// ErrExists otherwise, and ErrExclusiveUnsupported when s cannot make that
// promise atomically.
func CreateExclusive(ctx context.Context, s Store, key string, r io.Reader, contentType string) error {
	if c, ok := s.(ExclusiveCreator); ok {
		return c.CreateExclusive(ctx, key, r, contentType)
	}
	return ErrExclusiveUnsupported
}

// maxExclusiveCreateBytes bounds what an S3 exclusive create holds in
// memory: it is one conditional PutObject, not a multipart upload.
const maxExclusiveCreateBytes = 512 << 20

// CreateExclusive uploads with If-None-Match: *, which AWS S3 and most
// compatible stores honor. A store that rejects the header returns
// ErrExclusiveUnsupported, logged, and writes nothing.
func (s *S3) CreateExclusive(ctx context.Context, key string, r io.Reader, _ string) error {
	if err := ValidateKey(key); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(r, maxExclusiveCreateBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxExclusiveCreateBytes {
		return fmt.Errorf("blobstore: %s is too large for an exclusive create", key)
	}
	err = s.client.PutObjectIfAbsent(ctx, s.client.Bucket(), key, data)
	switch {
	case errors.Is(err, s3client.ErrObjectExists):
		return ErrExists
	case errors.Is(err, s3client.ErrConditionalWriteUnsupported):
		slog.WarnContext(ctx, "storage rejects conditional writes, so an object that must not replace another was not written", "component", "blobstore", "key", key)
		return ErrExclusiveUnsupported
	}
	return err
}

// CreateExclusive holds the mutation fence like every other write.
func (s *fencedStore) CreateExclusive(ctx context.Context, key string, r io.Reader, contentType string) error {
	if err := s.mutations.Acquire(ctx, 1); err != nil {
		return err
	}
	defer s.mutations.Release(1)
	return CreateExclusive(ctx, s.Store, key, r, contentType)
}

// CreateExclusive records where the object went, as every write does.
func (s *recordingStore) CreateExclusive(ctx context.Context, key string, r io.Reader, contentType string) error {
	if err := CreateExclusive(ctx, s.Store, key, r, contentType); err != nil {
		return err
	}
	return s.recordBackend(ctx)
}
