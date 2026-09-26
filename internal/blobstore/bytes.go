package blobstore

import (
	"context"
	"fmt"
	"io"
)

// GetBytes reads a whole object. Use it for objects small enough to hold in
// memory — subtitle files, manifests — and Store.Get for anything streamed.
func GetBytes(ctx context.Context, store Store, key string) ([]byte, error) {
	body, _, err := store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	return io.ReadAll(body)
}

// ByteStore adapts a Store to the key-only, whole-object surface that callers
// storing small blobs want. Delete takes a single key because those callers
// remove one object at a time and treat an absent key as deleted.
type ByteStore struct{ store Store }

func NewByteStore(store Store) *ByteStore {
	if store == nil {
		return nil
	}
	return &ByteStore{store: store}
}

func (b *ByteStore) Put(ctx context.Context, key string, data []byte) error {
	return b.store.Put(ctx, key, data)
}

func (b *ByteStore) Get(ctx context.Context, key string) ([]byte, error) {
	return GetBytes(ctx, b.store, key)
}

// Delete removes one object. An absent key counts as deleted, matching the
// store contract, so cleanup after a failed publish is not an error.
//
// The count is checked rather than discarded. S3 batch deletion reports
// per-object failures in its response body and still returns a nil error with a
// short count, so ignoring it would report success while the object remains.
// Subtitle deletion removes the database row before the object, and a silent
// success there leaves an orphan nothing will ever look for again.
func (b *ByteStore) Delete(ctx context.Context, key string) error {
	deleted, err := b.store.Delete(ctx, []string{key})
	if err != nil {
		return err
	}
	if deleted != 1 {
		return fmt.Errorf("blobstore: delete %q reported %d of 1 objects removed", key, deleted)
	}
	return nil
}
