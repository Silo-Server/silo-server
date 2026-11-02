package blobstore

import (
	"context"
	"io"
	"strings"
)

// Route returns a store that sends keys under prefix to routed and every
// other key to base. It lets a reader that serves several namespaces, such as
// the signed artwork route, find a namespace kept in another store. Probe and
// Identity are base's: the namespaces it routes record their own store.
// A nil routed, or routed equal to base, returns base unchanged.
func Route(base Store, prefix string, routed Store) Store {
	if base == nil || routed == nil || routed == base || prefix == "" {
		return base
	}
	return routedStore{base: base, prefix: prefix, routed: routed}
}

type routedStore struct {
	base   Store
	prefix string
	routed Store
}

func (s routedStore) storeFor(key string) Store {
	if strings.HasPrefix(key, s.prefix) {
		return s.routed
	}
	return s.base
}

func (s routedStore) Put(ctx context.Context, key string, data []byte) error {
	return s.storeFor(key).Put(ctx, key, data)
}

func (s routedStore) PutStream(ctx context.Context, key string, r io.Reader, contentType string) error {
	return s.storeFor(key).PutStream(ctx, key, r, contentType)
}

func (s routedStore) Get(ctx context.Context, key string) (io.ReadCloser, ObjectInfo, error) {
	return s.storeFor(key).Get(ctx, key)
}

func (s routedStore) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	return s.storeFor(key).Stat(ctx, key)
}

func (s routedStore) Delete(ctx context.Context, keys []string) (int, error) {
	var routed, base []string
	for _, key := range keys {
		if strings.HasPrefix(key, s.prefix) {
			routed = append(routed, key)
		} else {
			base = append(base, key)
		}
	}
	deleted := 0
	for _, part := range []struct {
		store Store
		keys  []string
	}{{s.routed, routed}, {s.base, base}} {
		if len(part.keys) == 0 {
			continue
		}
		n, err := part.store.Delete(ctx, part.keys)
		deleted += n
		if err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

// DeletePrefix and List go to routed only for a prefix inside the routed
// namespace. A wider prefix stays with base: the routed namespace is
// listed and deleted through its own store.
func (s routedStore) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	return s.storeFor(prefix).DeletePrefix(ctx, prefix)
}

func (s routedStore) List(ctx context.Context, prefix, cursor string, limit int) ([]ObjectInfo, string, error) {
	return s.storeFor(prefix).List(ctx, prefix, cursor, limit)
}

func (s routedStore) Probe(ctx context.Context) error { return s.base.Probe(ctx) }
func (s routedStore) Identity() string                { return s.base.Identity() }
