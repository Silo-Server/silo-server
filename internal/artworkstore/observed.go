package artworkstore

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkmetrics"
)

type observedStore struct {
	Store
	backend string
}

func observeStore(store Store, backend string) Store {
	if store == nil {
		return nil
	}
	return &observedStore{Store: store, backend: backend}
}

func (s *observedStore) WriteImmutable(ctx context.Context, key string, data []byte, metadata ObjectMetadata) (err error) {
	started := time.Now()
	defer func() { artworkmetrics.ObserveStore(s.backend, "write", started, err) }()
	return s.Store.WriteImmutable(ctx, key, data, metadata)
}

func (s *observedStore) Open(ctx context.Context, key string) (object *Object, err error) {
	started := time.Now()
	defer func() { artworkmetrics.ObserveStore(s.backend, "open", started, err) }()
	return s.Store.Open(ctx, key)
}

func (s *observedStore) Stat(ctx context.Context, key string) (info ObjectInfo, err error) {
	started := time.Now()
	defer func() { artworkmetrics.ObserveStore(s.backend, "stat", started, err) }()
	return s.Store.Stat(ctx, key)
}

func (s *observedStore) Matches(ctx context.Context, key string, data []byte) (matched bool, err error) {
	started := time.Now()
	defer func() { artworkmetrics.ObserveStore(s.backend, "matches", started, err) }()
	return s.Store.Matches(ctx, key, data)
}

func (s *observedStore) DeleteObjects(ctx context.Context, keys []string) (deleted int, err error) {
	started := time.Now()
	defer func() { artworkmetrics.ObserveStore(s.backend, "delete", started, err) }()
	return s.Store.DeleteObjects(ctx, keys)
}

func (s *observedStore) Probe(ctx context.Context) (err error) {
	started := time.Now()
	defer func() { artworkmetrics.ObserveStore(s.backend, "probe", started, err) }()
	return s.Store.Probe(ctx)
}

func (s *observedStore) ListPage(ctx context.Context, prefix, cursor string, limit int) (objects []ObjectInfo, next string, done bool, err error) {
	started := time.Now()
	defer func() { artworkmetrics.ObserveStore(s.backend, "list", started, err) }()
	return s.Store.ListPage(ctx, prefix, cursor, limit)
}

func (s *observedStore) DeletePrefixMaintenance(ctx context.Context, prefix string) (deleted int, err error) {
	started := time.Now()
	defer func() { artworkmetrics.ObserveStore(s.backend, "maintenance_delete", started, err) }()
	return s.Store.DeletePrefixMaintenance(ctx, prefix)
}

func (s *observedStore) Root() string {
	if rooted, ok := s.Store.(interface{ Root() string }); ok {
		return rooted.Root()
	}
	return ""
}

func (s *observedStore) FreeSpaceBytes(ctx context.Context) (int64, error) {
	if capacity, ok := s.Store.(CapacityProvider); ok {
		return capacity.FreeSpaceBytes(ctx)
	}
	return 0, ErrNotFound
}

func (s *observedStore) CleanTempFiles(ctx context.Context, olderThan time.Duration) (int, error) {
	cleaner, ok := s.Store.(interface {
		CleanTempFiles(context.Context, time.Duration) (int, error)
	})
	if !ok {
		return 0, ErrNotFound
	}
	return cleaner.CleanTempFiles(ctx, olderThan)
}
