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
	t.Setenv(EnvTrustedProxies, "")
	store := newFakeStore()
	if err := SeedDefaults(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if got := store.values[SettingTrustedProxies]; got != DefaultTrustedProxies {
		t.Fatalf("got %q, want defaults", got)
	}
}

func TestSeedDefaultsKeepsExistingValue(t *testing.T) {
	t.Setenv(EnvTrustedProxies, "")
	store := newFakeStore()
	store.values[SettingTrustedProxies] = "203.0.113.0/24"
	if err := SeedDefaults(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if store.sets != 0 {
		t.Fatalf("expected no writes, got %d", store.sets)
	}
}

func TestSeedDefaultsEnvOverridesExistingValue(t *testing.T) {
	t.Setenv(EnvTrustedProxies, " 10.0.0.0/8 ,203.0.113.7/32 ")
	store := newFakeStore()
	store.values[SettingTrustedProxies] = "192.168.0.0/16"
	if err := SeedDefaults(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	want := "10.0.0.0/8, 203.0.113.7/32"
	if got := store.values[SettingTrustedProxies]; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSeedDefaultsEnvIdempotent(t *testing.T) {
	t.Setenv(EnvTrustedProxies, "203.0.113.7/32")
	store := newFakeStore()
	store.values[SettingTrustedProxies] = "203.0.113.7/32"
	if err := SeedDefaults(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if store.sets != 0 {
		t.Fatalf("expected no writes for unchanged env value, got %d", store.sets)
	}
}

func TestSeedDefaultsEnvInvalid(t *testing.T) {
	t.Setenv(EnvTrustedProxies, "not-a-cidr")
	store := newFakeStore()
	if err := SeedDefaults(context.Background(), store); err == nil {
		t.Fatal("expected error for invalid env CIDR list")
	}
	if store.sets != 0 {
		t.Fatalf("expected no writes on invalid env, got %d", store.sets)
	}
}

func TestNormalizeCIDRList(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{" , ,", "", false},
		{"10.0.0.0/8", "10.0.0.0/8", false},
		{" 10.0.0.0/8, ::1/128 ", "10.0.0.0/8, ::1/128", false},
		{"10.0.0.1", "", true},   // bare IP, no prefix
		{"garbage/33", "", true}, // invalid prefix
	}
	for _, tc := range cases {
		got, err := NormalizeCIDRList(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("NormalizeCIDRList(%q): expected error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeCIDRList(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeCIDRList(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
