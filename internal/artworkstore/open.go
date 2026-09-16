package artworkstore

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/Silo-Server/silo-server/internal/s3client"
)

type SettingsStore interface {
	Get(context.Context, string) (string, error)
	SetIfAbsent(context.Context, string, string) (bool, error)
}

type Options struct {
	Backend   string
	LocalPath string
	S3        *s3client.Client
	Settings  SettingsStore
}

func Open(ctx context.Context, opts Options) (Store, string, error) {
	backend := strings.ToLower(strings.TrimSpace(opts.Backend))
	if backend == "" || backend == "auto" {
		if opts.S3 != nil {
			backend = BackendS3
		} else {
			backend = BackendLocal
		}
	}
	var store Store
	var err error
	switch backend {
	case BackendLocal:
		store, err = NewFilesystem(opts.LocalPath)
	case BackendS3:
		if opts.S3 == nil {
			return nil, "", fmt.Errorf("artwork storage backend s3 is configured but no S3 client is available")
		}
		store = NewS3(opts.S3)
	default:
		return nil, "", fmt.Errorf("unknown artwork storage backend %q", opts.Backend)
	}
	if err != nil {
		return nil, "", err
	}
	// Availability is checked by readiness through Probe, allowing outage recovery.
	if opts.Settings == nil {
		return store, backend, nil
	}
	// The catalog's keys belong to exactly one storage location. Opening a
	// different one, whether another backend or another bucket or root, would
	// serve a catalog whose objects live elsewhere.
	active, err := opts.Settings.Get(ctx, IdentitySettingKey)
	if err != nil {
		return nil, "", fmt.Errorf("read %s: %w", IdentitySettingKey, err)
	}
	if active != "" && active != store.Identity() {
		return nil, "", fmt.Errorf("artwork storage is recorded as %q but configured as %q; copy the artwork tree to the new storage, then delete the %s row", active, store.Identity(), IdentitySettingKey)
	}
	recorded := &recordingStore{Store: store, settings: opts.Settings}
	if direct, ok := store.(DirectURLer); ok {
		return &recordingDirectStore{recordingStore: recorded, DirectURLer: direct}, backend, nil
	}
	return recorded, backend, nil
}

type recordingStore struct {
	Store
	settings SettingsStore
	mu       sync.Mutex
	recorded bool
}

type recordingDirectStore struct {
	*recordingStore
	DirectURLer
}

func (s *recordingDirectStore) ObjectAvailable(ctx context.Context, key string) (bool, error) {
	checker, ok := s.Store.(interface {
		ObjectAvailable(context.Context, string) (bool, error)
	})
	if !ok {
		return false, fmt.Errorf("artwork backend does not support external availability checks")
	}
	return checker.ObjectAvailable(ctx, key)
}

func (s *recordingStore) Put(ctx context.Context, key string, data []byte) error {
	if err := s.Store.Put(ctx, key, data); err != nil {
		return err
	}
	return s.recordBackend(ctx)
}

func (s *recordingStore) recordBackend(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recorded {
		return nil
	}
	identity := s.Identity()
	inserted, err := s.settings.SetIfAbsent(ctx, IdentitySettingKey, identity)
	if err != nil {
		return fmt.Errorf("record artwork storage: %w", err)
	}
	if !inserted {
		active, err := s.settings.Get(ctx, IdentitySettingKey)
		if err != nil {
			return fmt.Errorf("verify recorded artwork storage: %w", err)
		}
		if active != identity {
			return fmt.Errorf("artwork storage changed concurrently: recorded %q, writing %q", active, identity)
		}
	}
	s.recorded = true
	return nil
}
