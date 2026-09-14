package plugins

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
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

// The store round-trips arbitrary bytes, keeps the row encrypted with AAD
// bound to installation, scope and key, isolates scopes and installations,
// and distinguishes an empty value from an absent one.
func TestInstanceStateStoreRoundTripEncryptedAndScoped(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := context.Background()
	cipher := instanceStateTestCipher(t)
	store := NewInstanceStateStore(pool, cipher)

	installation := seedResidentQueryInstallation(t, pool, true)
	other := seedResidentQueryInstallation(t, pool, true)

	value := []byte("node-key\x00\x01binary")
	if err := store.Write(ctx, installation, HostScopeAPI, "_machinekey", value); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, found, err := store.Read(ctx, installation, HostScopeAPI, "_machinekey")
	if err != nil || !found || !bytes.Equal(got, value) {
		t.Fatalf("Read = %q, %v, %v; want %q", got, found, err, value)
	}

	// The row never holds the plaintext; it is an enc:v1 envelope bound to
	// this exact (installation, scope, key).
	var stored []byte
	if err := pool.QueryRow(ctx,
		`SELECT state_value FROM plugin_instance_state WHERE plugin_installation_id = $1 AND host_scope = $2 AND state_key = $3`,
		installation, HostScopeAPI, "_machinekey").Scan(&stored); err != nil {
		t.Fatalf("select row: %v", err)
	}
	if bytes.Contains(stored, value) || !secret.IsEncrypted(string(stored)) {
		t.Fatalf("stored value is not an encrypted envelope: %q", stored)
	}
	if _, err := cipher.Decrypt(string(stored), instanceStateAAD(installation, NodeHostScope(3), "_machinekey")); err == nil {
		t.Fatal("envelope decrypted under another scope's AAD")
	}
	if plain, err := cipher.Decrypt(string(stored), instanceStateAAD(installation, HostScopeAPI, "_machinekey")); err != nil || plain != string(value) {
		t.Fatalf("envelope under the row AAD: %q, %v", plain, err)
	}

	// Scope and installation isolation.
	if _, found, err := store.Read(ctx, installation, NodeHostScope(3), "_machinekey"); err != nil || found {
		t.Fatalf("node scope saw the api value: found=%v err=%v", found, err)
	}
	if _, found, err := store.Read(ctx, other, HostScopeAPI, "_machinekey"); err != nil || found {
		t.Fatalf("other installation saw the value: found=%v err=%v", found, err)
	}
	if err := store.Write(ctx, installation, NodeHostScope(3), "_machinekey", []byte("proxy-key")); err != nil {
		t.Fatalf("Write node scope: %v", err)
	}
	got, _, err = store.Read(ctx, installation, HostScopeAPI, "_machinekey")
	if err != nil || !bytes.Equal(got, value) {
		t.Fatalf("api scope changed by node write: %q, %v", got, err)
	}

	// Overwrite and empty values.
	if err := store.Write(ctx, installation, HostScopeAPI, "_machinekey", []byte("rotated")); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	got, _, _ = store.Read(ctx, installation, HostScopeAPI, "_machinekey")
	if string(got) != "rotated" {
		t.Fatalf("after overwrite = %q", got)
	}
	if err := store.Write(ctx, installation, HostScopeAPI, "empty", nil); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	got, found, err = store.Read(ctx, installation, HostScopeAPI, "empty")
	if err != nil || !found || len(got) != 0 {
		t.Fatalf("empty value = %q, %v, %v; want found and empty", got, found, err)
	}
	if _, found, err := store.Read(ctx, installation, HostScopeAPI, "missing"); err != nil || found {
		t.Fatalf("missing key: found=%v err=%v", found, err)
	}

	// Key order follows the database collation, so compare as a set.
	keys, err := store.Keys(ctx, installation, HostScopeAPI)
	sort.Strings(keys)
	if err != nil || strings.Join(keys, ",") != "_machinekey,empty" {
		t.Fatalf("Keys = %v, %v", keys, err)
	}

	// The scoped adapter satisfies the pluginhost contract with the scope fixed.
	var scoped pluginhost.InstanceStateStore = store.ForScope(NodeHostScope(3))
	got, found, err = scoped.ReadInstanceState(ctx, installation, "_machinekey")
	if err != nil || !found || string(got) != "proxy-key" {
		t.Fatalf("scoped read = %q, %v, %v", got, found, err)
	}

	// Uninstall cascades.
	if _, err := pool.Exec(ctx, `DELETE FROM plugin_installations WHERE id = $1`, installation); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM plugin_instance_state WHERE plugin_installation_id = $1`, installation).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("%d rows survived the installation delete", remaining)
	}
}

func TestInstanceStateStoreLimits(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := context.Background()
	store := NewInstanceStateStore(pool, instanceStateTestCipher(t))
	installation := seedResidentQueryInstallation(t, pool, true)

	longKey := strings.Repeat("k", pluginhost.InstanceStateMaxKeyBytes+1)
	if err := store.Write(ctx, installation, HostScopeAPI, longKey, []byte("x")); !errors.Is(err, pluginhost.ErrInstanceStateKeyTooLong) {
		t.Fatalf("long key: %v", err)
	}
	if _, _, err := store.Read(ctx, installation, HostScopeAPI, longKey); !errors.Is(err, pluginhost.ErrInstanceStateKeyTooLong) {
		t.Fatalf("long key read: %v", err)
	}
	if err := store.Write(ctx, installation, HostScopeAPI, "", []byte("x")); err == nil {
		t.Fatal("empty key accepted")
	}
	maxKey := strings.Repeat("k", pluginhost.InstanceStateMaxKeyBytes)
	if err := store.Write(ctx, installation, HostScopeAPI, maxKey, []byte("x")); err != nil {
		t.Fatalf("256-byte key rejected: %v", err)
	}

	big := bytes.Repeat([]byte("v"), pluginhost.InstanceStateMaxValueBytes+1)
	if err := store.Write(ctx, installation, HostScopeAPI, "big", big); !errors.Is(err, pluginhost.ErrInstanceStateValueTooLarge) {
		t.Fatalf("oversize value: %v", err)
	}
	if err := store.Write(ctx, installation, HostScopeAPI, "big", big[:pluginhost.InstanceStateMaxValueBytes]); err != nil {
		t.Fatalf("256 KiB value rejected: %v", err)
	}

	// Key budget per scope: the two keys above plus 254 more fill it; the
	// 257th new key is refused while rewriting an existing key still works
	// and another scope is unaffected.
	for i := 0; i < pluginhost.InstanceStateMaxKeys-2; i++ {
		if err := store.Write(ctx, installation, HostScopeAPI, fmt.Sprintf("k%03d", i), []byte("v")); err != nil {
			t.Fatalf("fill key %d: %v", i, err)
		}
	}
	if err := store.Write(ctx, installation, HostScopeAPI, "one-too-many", []byte("v")); !errors.Is(err, pluginhost.ErrInstanceStateTooManyKeys) {
		t.Fatalf("257th key: %v", err)
	}
	if err := store.Write(ctx, installation, HostScopeAPI, "k000", []byte("rewritten")); err != nil {
		t.Fatalf("rewrite at the budget: %v", err)
	}
	if err := store.Write(ctx, installation, NodeHostScope(9), "one-too-many", []byte("v")); err != nil {
		t.Fatalf("other scope blocked by the api budget: %v", err)
	}
	keys, err := store.Keys(ctx, installation, HostScopeAPI)
	if err != nil || len(keys) != pluginhost.InstanceStateMaxKeys {
		t.Fatalf("Keys = %d, %v; want %d", len(keys), err, pluginhost.InstanceStateMaxKeys)
	}

	// Config test-runs use negative installation ids and get no state.
	for _, id := range []int{0, -1, -42} {
		if err := store.Write(ctx, id, HostScopeAPI, "k", []byte("v")); !errors.Is(err, pluginhost.ErrInstanceStateUnavailable) {
			t.Fatalf("installation %d write: %v", id, err)
		}
		if _, _, err := store.Read(ctx, id, HostScopeAPI, "k"); !errors.Is(err, pluginhost.ErrInstanceStateUnavailable) {
			t.Fatalf("installation %d read: %v", id, err)
		}
	}
	if err := store.Write(ctx, installation, "", "k", []byte("v")); err == nil {
		t.Fatal("empty scope accepted")
	}
}

func TestInstanceStateStoreRequiresCipher(t *testing.T) {
	pool := builtinGuardTestPool(t)
	store := NewInstanceStateStore(pool, nil)
	installation := seedResidentQueryInstallation(t, pool, true)
	if err := store.Write(context.Background(), installation, HostScopeAPI, "k", []byte("v")); err == nil {
		t.Fatal("plaintext write accepted without a cipher")
	}
}

// Concurrent first writes of distinct keys at the budget must not overshoot
// it: admission of a new key is serialized per scope.
func TestInstanceStateWriteKeyBudgetHoldsUnderConcurrency(t *testing.T) {
	pool := builtinGuardTestPool(t)
	store := NewInstanceStateStore(pool, instanceStateTestCipher(t))
	ctx := context.Background()
	installation := seedResidentQueryInstallation(t, pool, true)
	for i := 0; i < pluginhost.InstanceStateMaxKeys-1; i++ {
		if err := store.Write(ctx, installation, HostScopeAPI, fmt.Sprintf("k%03d", i), []byte("v")); err != nil {
			t.Fatalf("fill key %d: %v", i, err)
		}
	}
	const racers = 16
	errs := make(chan error, racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		go func(i int) {
			<-start
			errs <- store.Write(ctx, installation, HostScopeAPI, fmt.Sprintf("race%02d", i), []byte("v"))
		}(i)
	}
	close(start)
	admitted := 0
	for i := 0; i < racers; i++ {
		switch err := <-errs; {
		case err == nil:
			admitted++
		case errors.Is(err, pluginhost.ErrInstanceStateTooManyKeys):
		default:
			t.Fatalf("racing write: %v", err)
		}
	}
	keys, err := store.Keys(ctx, installation, HostScopeAPI)
	if err != nil {
		t.Fatal(err)
	}
	if admitted != 1 || len(keys) != pluginhost.InstanceStateMaxKeys {
		t.Fatalf("admitted %d racers, scope holds %d keys; want exactly 1 and %d", admitted, len(keys), pluginhost.InstanceStateMaxKeys)
	}
}
