package handlers

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

type serializedSettingsStore struct {
	mu sync.Mutex

	values map[string]string

	atomicCalls int
	active      int
	maxActive   int
	directSets  int
	directBatch int
}

type nonAtomicSettingsStore struct {
	values map[string]string
}

func (s *nonAtomicSettingsStore) Get(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s *nonAtomicSettingsStore) GetAll(context.Context) (map[string]string, error) {
	return cloneSettings(s.values), nil
}

func (s *nonAtomicSettingsStore) Set(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

func newSerializedSettingsStore(values map[string]string) *serializedSettingsStore {
	return &serializedSettingsStore{values: values}
}

func (s *serializedSettingsStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key], nil
}

func (s *serializedSettingsStore) GetAll(context.Context) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSettings(s.values), nil
}

func (s *serializedSettingsStore) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.directSets++
	s.values[key] = value
	return nil
}

func (s *serializedSettingsStore) SetMany(_ context.Context, values map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.directBatch++
	for key, value := range values {
		s.values[key] = value
	}
	return nil
}

func (s *serializedSettingsStore) UpdateAtomic(
	_ context.Context,
	update func(current map[string]string) (map[string]string, error),
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.atomicCalls++
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	defer func() { s.active-- }()

	// Widen the race window: a handler that reads outside this capability would
	// allow both prospective snapshots to validate against the same state.
	time.Sleep(10 * time.Millisecond)
	writes, err := update(cloneSettings(s.values))
	if err != nil {
		return err
	}
	for key, value := range writes {
		s.values[key] = value
	}
	return nil
}

func cloneSettings(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

// raceSaves runs two saves at once and reports how many were accepted and how
// many refused as invalid.
func raceSaves(t *testing.T, saves ...func() error) (accepted, refused int) {
	t.Helper()
	start := make(chan struct{})
	results := make(chan error, len(saves))
	for _, save := range saves {
		go func() {
			<-start
			results <- save()
		}()
	}
	close(start)
	for range saves {
		switch err := <-results; adminErrorStatus(err) {
		case 200:
			accepted++
		case 400:
			refused++
		default:
			t.Fatalf("unexpected result: %v", err)
		}
	}
	return accepted, refused
}

// Two saves that are each valid but invalid together run one at a time, so
// the second sees the first and is refused.
func TestAdminSettingsAtomicUpdateSerializesCrossFieldValidation(t *testing.T) {
	store := newSerializedSettingsStore(map[string]string{
		"auth.access_token_expiry":  "8h",
		"auth.refresh_token_expiry": "30d",
	})
	handler := &AdminHandler{SettingsRepo: store}

	accepted, refused := raceSaves(t,
		func() error { return saveAdminSetting(t, handler, "auth.access_token_expiry", "48h", false) },
		func() error { return saveAdminSetting(t, handler, "auth.refresh_token_expiry", "24h", false) },
	)
	if accepted != 1 || refused != 1 {
		t.Fatalf("accepted %d refused %d, want one of each", accepted, refused)
	}
	current, _ := store.GetAll(t.Context())
	if err := validateProspectiveAdminSettings(current, false); err != nil {
		t.Fatalf("serialized final settings are invalid: %v; values=%#v", err, current)
	}
	if store.atomicCalls != 2 || store.maxActive != 1 {
		t.Fatalf("atomic calls=%d max active=%d, want 2 and 1", store.atomicCalls, store.maxActive)
	}
}

func TestAdminSingleRoutingUpdatesSerializeCrossFieldValidation(t *testing.T) {
	for _, test := range []struct {
		name         string
		executionKey string
		egressKey    string
	}{
		{"remux", config.PlaybackRoutingRemuxExecutionSettingKey, config.PlaybackRoutingRemuxEgressSettingKey},
		{"video transcode", config.PlaybackRoutingVideoTranscodeExecutionSettingKey, config.PlaybackRoutingVideoTranscodeEgressSettingKey},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newSerializedSettingsStore(map[string]string{})
			handler := &AdminHandler{SettingsRepo: store}

			accepted, refused := raceSaves(t,
				func() error {
					return saveAdminSetting(t, handler, test.executionKey, string(config.PlaybackExecutionAPIOnly), true)
				},
				func() error {
					return saveAdminSetting(t, handler, test.egressKey, string(config.PlaybackEgressProxyOnly), true)
				},
			)
			if accepted != 1 || refused != 1 {
				t.Fatalf("accepted %d refused %d, want one of each", accepted, refused)
			}
			current, _ := store.GetAll(t.Context())
			if _, err := config.LoadFromDB(current); err != nil {
				t.Fatalf("serialized routing settings are restart-invalid: %v; values=%#v", err, current)
			}
			if store.atomicCalls != 2 || store.maxActive != 1 {
				t.Fatalf("atomic calls=%d max active=%d, want 2 and 1", store.atomicCalls, store.maxActive)
			}
		})
	}
}

// The batch and single-key writes share one atomic boundary and never write
// around it.
func TestAdminSettingsWritesShareAtomicBoundary(t *testing.T) {
	store := newSerializedSettingsStore(map[string]string{})
	handler := &AdminHandler{SettingsRepo: store}

	accepted, _ := raceSaves(t,
		func() error { return saveAdminSetting(t, handler, "branding.server_name", "Casa", false) },
		func() error { return saveAdminSetting(t, handler, "server.log_level", "debug", true) },
	)
	if accepted != 2 {
		t.Fatalf("accepted %d, want both", accepted)
	}
	if store.atomicCalls != 2 || store.maxActive != 1 {
		t.Fatalf("atomic calls=%d max active=%d, want 2 and 1", store.atomicCalls, store.maxActive)
	}
	if store.directSets != 0 || store.directBatch != 0 {
		t.Fatalf("direct writes: Set=%d SetMany=%d, want zero", store.directSets, store.directBatch)
	}
}

// Clearing redis.url and moving rate limiting onto Redis are each valid
// alone; the rate-limit write shares the settings boundary, so one is refused.
func TestRateLimitAndAdminRedisUpdatesShareAtomicBoundary(t *testing.T) {
	store := newSerializedSettingsStore(map[string]string{
		"ratelimit.backend": "memory",
		"redis.url":         "redis://cache.example.invalid:6379",
	})
	adminHandler := &AdminHandler{SettingsRepo: store}
	rateLimitHandler := NewRateLimitHandler(store, nil, nil, NewServerRestartStatusTracker())

	accepted, refused := raceSaves(t,
		func() error { return saveAdminSetting(t, adminHandler, "redis.url", "", false) },
		func() error {
			_, err := rateLimitHandler.UpdateAdminRateLimitConfig(t.Context(), AdminRateLimitUpdate{Backend: "redis"}, nil)
			return err
		},
	)
	if accepted != 1 || refused != 1 {
		t.Fatalf("accepted %d refused %d, want one of each", accepted, refused)
	}
	current, _ := store.GetAll(t.Context())
	if err := config.ValidateRedisRateLimitTransport(current, false); err != nil {
		t.Fatalf("serialized final Redis transport is invalid: %v; values=%#v", err, current)
	}
}

func TestAdminSettingsWritesFailClosedWithoutAtomicCapability(t *testing.T) {
	store := &nonAtomicSettingsStore{values: map[string]string{}}

	if err := saveAdminSetting(t, &AdminHandler{SettingsRepo: store}, "branding.server_name", "Casa", false); adminErrorStatus(err) != 500 {
		t.Fatalf("settings save on a non-atomic store: %v, want 500", err)
	}
	rateLimitHandler := NewRateLimitHandler(store, nil, nil, NewServerRestartStatusTracker())
	if _, err := rateLimitHandler.UpdateAdminRateLimitConfig(t.Context(), AdminRateLimitUpdate{Enabled: new(false)}, nil); adminErrorStatus(err) != 500 {
		t.Fatalf("rate-limit save on a non-atomic store: %v, want 500", err)
	}
	if len(store.values) != 0 {
		t.Fatalf("non-atomic store was mutated: %#v", store.values)
	}
}
