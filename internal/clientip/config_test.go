package clientip

import (
	"context"
	"testing"
)

type fakeStore struct {
	values map[string]string
	sets   int
}

func newFakeStore() *fakeStore {
	return &fakeStore{values: map[string]string{}}
}

func (s *fakeStore) Get(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s *fakeStore) Set(_ context.Context, key, value string) error {
	s.values[key] = value
	s.sets++
	return nil
}

func (s *fakeStore) GetAll(_ context.Context) (map[string]string, error) {
	return s.values, nil
}

func TestSeedDefaultsWritesDefaultsWhenUnset(t *testing.T) {
	store := newFakeStore()
	if err := SeedDefaults(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if got := store.values[SettingTrustedProxies]; got != DefaultTrustedProxies {
		t.Fatalf("got %q, want defaults", got)
	}
}

func TestSeedDefaultsKeepsExistingValue(t *testing.T) {
	store := newFakeStore()
	store.values[SettingTrustedProxies] = "203.0.113.0/24"
	if err := SeedDefaults(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if store.sets != 0 {
		t.Fatalf("expected no writes, got %d", store.sets)
	}
}
