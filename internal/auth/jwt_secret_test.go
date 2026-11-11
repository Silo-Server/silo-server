package auth

import (
	"context"
	"sync"
	"testing"
)

// secretStore is an in-memory server_settings. SetIfAbsent writes only over
// an empty string, as ServerSettingsRepo's conditional upsert does; a stored
// whitespace value is ciphertext there, so it does not count as empty.
type secretStore struct {
	mu     sync.Mutex
	values map[string]string
	writes int
	// dropWrites makes writes succeed without storing anything.
	dropWrites bool
}

func (s *secretStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key], nil
}

func (s *secretStore) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store(key, value)
	return nil
}

func (s *secretStore) SetIfAbsent(_ context.Context, key, value string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values[key] != "" {
		return false, nil
	}
	s.store(key, value)
	return true, nil
}

func (s *secretStore) store(key, value string) {
	s.writes++
	if s.dropWrites {
		return
	}
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
}

// Issue #3312: processes starting at once on a new database each generate a
// secret, and all of them must end up with the one that was stored.
func TestEnsureJWTSecretConcurrentStartsConverge(t *testing.T) {
	store := &secretStore{}
	const processes = 16
	secrets := make([]string, processes)
	var wg sync.WaitGroup
	for i := range processes {
		wg.Go(func() {
			secret, err := EnsureJWTSecret(t.Context(), store)
			if err != nil {
				t.Error(err)
			}
			secrets[i] = secret
		})
	}
	wg.Wait()
	stored := store.values[JWTSecretSettingKey]
	if stored == "" || store.writes != 1 {
		t.Fatalf("stored %q after %d writes, want one generated secret", stored, store.writes)
	}
	for i, secret := range secrets {
		if secret != stored {
			t.Fatalf("process %d uses %q, want the stored %q", i, secret, stored)
		}
	}
}

func TestEnsureJWTSecret(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stored     string
		dropWrites bool
		wantKept   bool
		wantErr    bool
	}{
		{name: "keeps a stored secret", stored: "existing-secret", wantKept: true},
		{name: "replaces a whitespace-only secret", stored: "  \n"},
		{name: "fails when the stored value stays empty", dropWrites: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &secretStore{values: map[string]string{JWTSecretSettingKey: tc.stored}, dropWrites: tc.dropWrites}
			secret, err := EnsureJWTSecret(t.Context(), store)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("secret %q, want an error", secret)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantKept {
				if secret != tc.stored || store.writes != 0 {
					t.Fatalf("secret %q after %d writes, want %q kept", secret, store.writes, tc.stored)
				}
				return
			}
			if secret == tc.stored || secret != store.values[JWTSecretSettingKey] || store.writes != 1 {
				t.Fatalf("secret %q, stored %q after %d writes; want one new secret", secret, store.values[JWTSecretSettingKey], store.writes)
			}
		})
	}
}
