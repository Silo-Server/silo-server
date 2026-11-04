package plugins

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"github.com/Silo-Server/silo-server/internal/secret"
)

func instanceStateTestCipher(t *testing.T) *secret.Cipher {
	t.Helper()
	cipher, err := secret.New(bytes.Repeat([]byte("k"), secret.MinMasterKeyLen))
	if err != nil {
		t.Fatalf("secret.New: %v", err)
	}
	return cipher
}

func TestInstanceStateEmptyValueAuthenticatesRow(t *testing.T) {
	store := NewInstanceStateStore(nil, instanceStateTestCipher(t))
	stored, err := store.seal(5, "api", "key", nil)
	if err != nil {
		t.Fatal(err)
	}
	if value, err := store.open(5, "api", "key", stored); err != nil || len(value) != 0 {
		t.Fatalf("roundtrip=%q, %v", value, err)
	}
	for _, row := range []struct {
		id         int
		scope, key string
	}{{6, "api", "key"}, {5, "node:1", "key"}, {5, "api", "other"}} {
		if _, err := store.open(row.id, row.scope, row.key, stored); err == nil {
			t.Errorf("empty value accepted under another row: %+v", row)
		}
	}
	if _, err := store.open(5, "api", "key", []byte("empty:v1")); err == nil {
		t.Error("plaintext empty marker accepted")
	}
	if _, err := store.open(5, "api", "key", bytes.TrimPrefix(stored, []byte(instanceStateEmptyMarker))); err == nil {
		t.Error("removing the empty marker changed an authenticated empty value into ordinary data")
	}
	ordinary, err := store.seal(5, "api", "key:empty", []byte("empty"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.open(5, "api", "key", append([]byte(instanceStateEmptyMarker), ordinary...)); err == nil {
		t.Error("ordinary ciphertext became an authenticated empty value")
	}
	value := []byte("empty:v1")
	stored, err = store.seal(5, "api", "key", value)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.open(5, "api", "key", stored); err != nil || !bytes.Equal(got, value) {
		t.Fatalf("marker literal=%q, %v", got, err)
	}
}

// TestInstanceStateConditionalWritesPostgres covers the versioned writes
// every API server shares one scope through: versions count writes, a write
// at a stale or wrong version changes nothing, create-only fails on an
// existing key, the key budget still refuses a new key (as a budget error,
// not a conflict), and of several writers at one version exactly one lands.
func TestInstanceStateConditionalWritesPostgres(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := context.Background()
	store := NewInstanceStateStore(pool, instanceStateTestCipher(t))
	id := seedResidentQueryInstallation(t, pool, true)
	at := func(v int64) *int64 { return &v }
	write := func(t *testing.T, key, value string, expected *int64) (int64, error) {
		t.Helper()
		return store.Write(ctx, id, HostScopeAPI, key, []byte(value), expected)
	}
	expectState := func(t *testing.T, key, value string, version int64) {
		t.Helper()
		got, gotVersion, err := store.Read(ctx, id, HostScopeAPI, key)
		if err != nil || string(got) != value || gotVersion != version {
			t.Fatalf("read %q = %q@%d, %v; want %q@%d", key, got, gotVersion, err, value, version)
		}
	}

	expectState(t, "k", "", 0)
	if v, err := write(t, "k", "a", at(0)); err != nil || v != 1 {
		t.Fatalf("create-only on a missing key = %d, %v; want 1", v, err)
	}
	if _, err := write(t, "k", "b", at(0)); !errors.Is(err, pluginhost.ErrInstanceStateVersionConflict) {
		t.Fatalf("create-only on an existing key = %v, want conflict", err)
	}
	expectState(t, "k", "a", 1)
	if v, err := write(t, "k", "c", nil); err != nil || v != 2 {
		t.Fatalf("unconditional write = %d, %v; want 2", v, err)
	}
	for _, stale := range []int64{1, 3, -1} {
		if _, err := write(t, "k", "d", at(stale)); !errors.Is(err, pluginhost.ErrInstanceStateVersionConflict) {
			t.Fatalf("write at version %d = %v, want conflict", stale, err)
		}
	}
	expectState(t, "k", "c", 2)
	if v, err := write(t, "k", "e", at(2)); err != nil || v != 3 {
		t.Fatalf("write at the current version = %d, %v; want 3", v, err)
	}
	expectState(t, "k", "e", 3)
	if _, err := write(t, "missing", "x", at(1)); !errors.Is(err, pluginhost.ErrInstanceStateVersionConflict) {
		t.Fatalf("versioned write on a missing key = %v, want conflict", err)
	}
	expectState(t, "missing", "", 0)

	t.Run("one of several writers at a version lands", func(t *testing.T) {
		const writers = 8
		var wg sync.WaitGroup
		results := make([]error, writers)
		for i := range writers {
			wg.Go(func() {
				_, results[i] = store.Write(ctx, id, HostScopeAPI, "k", []byte{byte('0' + i)}, at(3))
			})
		}
		wg.Wait()
		landed := -1
		for i, err := range results {
			switch {
			case err == nil && landed < 0:
				landed = i
			case err == nil:
				t.Fatalf("writers %d and %d both landed at version 3", landed, i)
			case !errors.Is(err, pluginhost.ErrInstanceStateVersionConflict):
				t.Fatalf("writer %d: %v", i, err)
			}
		}
		if landed < 0 {
			t.Fatal("no writer landed")
		}
		expectState(t, "k", string(rune('0'+landed)), 4)
	})

	t.Run("key budget", func(t *testing.T) {
		// Fill the scope to the limit next to "k". Rows that are only counted
		// need no real ciphertext.
		if _, err := pool.Exec(ctx, `
			INSERT INTO plugin_instance_state (plugin_installation_id, host_scope, state_key, state_value)
			SELECT $1, $2, 'fill-' || g, '\x00'::bytea FROM generate_series(1, $3::int) g
		`, id, HostScopeAPI, pluginhost.InstanceStateMaxKeys-1); err != nil {
			t.Fatal(err)
		}
		for _, expected := range []*int64{nil, at(0)} {
			if _, err := write(t, "new", "x", expected); !errors.Is(err, pluginhost.ErrInstanceStateTooManyKeys) {
				t.Fatalf("new key at the limit (expected %v) = %v, want too many keys", expected, err)
			}
		}
		if _, err := write(t, "k", "x", at(0)); !errors.Is(err, pluginhost.ErrInstanceStateVersionConflict) {
			t.Fatalf("create-only on an existing key at the limit = %v, want conflict", err)
		}
		if v, err := write(t, "k", "x", at(4)); err != nil || v != 5 {
			t.Fatalf("replacing a key at the limit = %d, %v; want 5", v, err)
		}
		if v, err := write(t, "k", "y", nil); err != nil || v != 6 {
			t.Fatalf("unconditional replace at the limit = %d, %v; want 6", v, err)
		}
	})
}
