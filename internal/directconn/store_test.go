package directconn

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
	"github.com/darthShadow/shunpike/coordination/server/storetest"
	"github.com/redis/go-redis/v9"
)

func TestNewCoordinationStoreSelection(t *testing.T) {
	t.Run("without Redis the store is the module memory store", func(t *testing.T) {
		store := NewCoordinationStore(nil, "")
		if _, ok := store.(*redisStore); ok {
			t.Fatal("a nil Redis client selected the Redis adapter")
		}
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		d := server.Device{Ref: server.DeviceRef{Account: "1", Device: "d"}, NodeKey: protocol.NodeKey{1}, LeaseExpires: now.Add(time.Minute)}
		if _, err := store.PutDevice(context.Background(), d, 32, now); err != nil {
			t.Fatalf("memory store PutDevice: %v", err)
		}
		if _, err := store.Device(context.Background(), d.Ref, now); err != nil {
			t.Fatalf("memory store Device: %v", err)
		}
	})
	t.Run("with Redis the store is the Redis adapter", func(t *testing.T) {
		client := redis.NewClient(&redis.Options{Dialer: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("redis down")
		}, MaxRetries: -1})
		t.Cleanup(func() { _ = client.Close() })
		if _, ok := NewCoordinationStore(client, labFence).(*redisStore); !ok {
			t.Fatal("a Redis client did not select the Redis adapter")
		}
	})
}

// The memory store passes the module's store suite and the conditional
// operations' suite.
func TestMemoryStoreConformance(t *testing.T) {
	storetest.Run(t, func(*testing.T) server.Store { return newMemoryStore() })
	runConditionalStoreConformance(t, func(*testing.T) CoordinationStore { return newMemoryStore() }, memoryConditionalHooks)
}

// conditionalHooks adapts the conditional cases to how a store decides that an
// entry has lapsed. The memory store judges the caller's now. The Redis store
// judges Redis time, so its entry is made lapsed by rewriting the stored lease
// end while the key still exists.
type conditionalHooks struct {
	// lapse makes d's entry lapsed and returns the now the call passes.
	lapse func(t *testing.T, store CoordinationStore, d server.Device) time.Time
	// raw reads d's stored policy and lease end with no liveness check. It is nil
	// for the memory store, whose reads at labNow still see a lapsed entry.
	raw func(t *testing.T, store CoordinationStore, d server.Device) (policy, leaseEnd string)
}

// memoryConditionalHooks passes conditionalLapsed as the call's now.
var memoryConditionalHooks = conditionalHooks{
	lapse: func(*testing.T, CoordinationStore, server.Device) time.Time { return conditionalLapsed },
}

// redisConditionalHooks writes a lease end one millisecond before Redis time and
// reads the device hash directly.
var redisConditionalHooks = conditionalHooks{
	lapse: func(t *testing.T, store CoordinationStore, d server.Device) time.Time {
		t.Helper()
		client := store.(*redisStore).client
		redisNow, err := client.Time(t.Context()).Result()
		if err != nil {
			t.Fatal(err)
		}
		if err := client.HSet(t.Context(), devKey(d.Ref), "lease_end_ms", redisNow.UnixMilli()-1).Err(); err != nil {
			t.Fatal(err)
		}
		return labNow
	},
	raw: func(t *testing.T, store CoordinationStore, d server.Device) (string, string) {
		t.Helper()
		got, err := store.(*redisStore).client.HMGet(t.Context(), devKey(d.Ref), "policy", "lease_end_ms").Result()
		if err != nil {
			t.Fatal(err)
		}
		policy, _ := got[0].(string)
		leaseEnd, _ := got[1].(string)
		return policy, leaseEnd
	},
}

// runConditionalStoreConformance checks SwapDevicePolicy and DeleteDeviceIf on a
// fresh store per case: each acts only on a live entry that holds the named node
// key and policy, and otherwise leaves the store unchanged. It also checks that
// PutDeviceReplacing names the key it replaced.
func runConditionalStoreConformance(t *testing.T, newStore func(*testing.T) CoordinationStore, hooks conditionalHooks) {
	t.Helper()
	runPutDeviceReplacingConformance(t, newStore)
	runSwapDevicePolicyConformance(t, newStore, hooks)
	runDeleteDeviceIfConformance(t, newStore, hooks)
}

// PutDeviceReplacing returns the node key the write replaced, and the zero key
// when the device had no entry or kept its key.
func runPutDeviceReplacingConformance(t *testing.T, newStore func(*testing.T) CoordinationStore) {
	t.Helper()
	t.Run("put names the key it replaced", func(t *testing.T) {
		ctx := context.Background()
		store := newStore(t)
		put := func(key byte) protocol.NodeKey {
			t.Helper()
			replaced, err := store.PutDeviceReplacing(ctx, labDevice("1", "tv", key), 32, labNow)
			if err != nil {
				t.Fatalf("PutDeviceReplacing(key %d): %v", key, err)
			}
			return replaced
		}
		if got := put(1); !got.IsZero() {
			t.Fatalf("first write replaced %v, want no key", got)
		}
		if got := put(1); !got.IsZero() {
			t.Fatalf("renewal with the same key replaced %v, want no key", got)
		}
		if got, want := put(2), (protocol.NodeKey{1}); got != want {
			t.Fatalf("write of a new key replaced %v, want %v", got, want)
		}
		if got, want := put(3), (protocol.NodeKey{2}); got != want {
			t.Fatalf("second write of a new key replaced %v, want %v", got, want)
		}
	})
}

// policyAfterRefusal returns the policy of d's stored entry after a refused
// call. A lapsed Redis entry is no longer live, so it is read raw.
func policyAfterRefusal(t *testing.T, store CoordinationStore, hooks conditionalHooks, d server.Device, lapsed bool) string {
	t.Helper()
	if lapsed && hooks.raw != nil {
		policy, _ := hooks.raw(t, store, d)
		return policy
	}
	return storedPolicy(t, store, d)
}

// The conditional cases' shared values: a lease end past the entry's, and a
// node key the entry does not hold.
var (
	conditionalLapsed   = labNow.Add(2 * time.Minute)
	conditionalOtherKey = protocol.NodeKey{9}
)

// enrolledConditionalStore returns a fresh store holding one entry with a
// registration and a confirmation.
func enrolledConditionalStore(t *testing.T, newStore func(*testing.T) CoordinationStore) (CoordinationStore, server.Device) {
	t.Helper()
	ctx := context.Background()
	store := newStore(t)
	d := labDevice("1", "tv", 1)
	if _, err := store.PutDevice(ctx, d, 32, labNow); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRegistration(ctx, d.Ref, labRegistration(d), labNow.Add(time.Minute), labNow); err != nil {
		t.Fatal(err)
	}
	conf := protocol.Confirmation{Peer: d.NodeKey, DiscoKey: protocol.DiscoKey{1}, Endpoint: netip.MustParseAddrPort("192.0.2.1:1234")}
	if err := store.PutConfirmation(ctx, "api", d.Ref, &conf, labNow.Add(time.Minute), labNow); err != nil {
		t.Fatal(err)
	}
	return store, d
}

// storedPolicy returns the policy of d's stored entry.
func storedPolicy(t *testing.T, store CoordinationStore, d server.Device) string {
	t.Helper()
	got, err := store.Device(context.Background(), d.Ref, labNow)
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	return string(got.Policy)
}

func runSwapDevicePolicyConformance(t *testing.T, newStore func(*testing.T) CoordinationStore, hooks conditionalHooks) {
	t.Helper()
	ctx := context.Background()
	enrolled := func(t *testing.T) (CoordinationStore, server.Device) {
		t.Helper()
		return enrolledConditionalStore(t, newStore)
	}
	t.Run("swap writes the policy of a matching entry and keeps the rest", func(t *testing.T) {
		store, d := enrolled(t)
		var leaseBefore string
		if hooks.raw != nil {
			_, leaseBefore = hooks.raw(t, store, d)
		}
		wrote, err := store.SwapDevicePolicy(ctx, d.Ref, d.NodeKey, d.Policy, []byte("audited"), labNow)
		if err != nil || !wrote {
			t.Fatalf("swap = %t, %v; want a write", wrote, err)
		}
		got, err := store.DeviceByNodeKey(ctx, d.NodeKey, labNow)
		if err != nil {
			t.Fatal(err)
		}
		// A Redis store returns the lease end rebased to the caller's clock, so for
		// it the stored lease end is what must stay exactly as it was.
		leaseKept, gotLease, wantLease := got.LeaseExpires.Equal(d.LeaseExpires), got.LeaseExpires.String(), d.LeaseExpires.String()
		if hooks.raw != nil {
			_, leaseAfter := hooks.raw(t, store, d)
			leaseKept, gotLease, wantLease = leaseAfter == leaseBefore, leaseAfter, leaseBefore
		}
		if string(got.Policy) != "audited" || !leaseKept || got.Registration == nil {
			t.Fatalf("entry after swap: policy %q, lease %s, registration kept %t; want %q, %s, true", got.Policy, gotLease, got.Registration != nil, "audited", wantLease)
		}
	})
	for _, tc := range []struct {
		name     string
		key      protocol.NodeKey
		expected string
		lapsed   bool
	}{
		{"swap leaves an entry with another key", conditionalOtherKey, "direct", false},
		{"swap leaves an entry with another policy", protocol.NodeKey{1}, "changed", false},
		{"swap leaves a lapsed entry", protocol.NodeKey{1}, "direct", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, d := enrolled(t)
			now := labNow
			if tc.lapsed {
				now = hooks.lapse(t, store, d)
			}
			wrote, err := store.SwapDevicePolicy(ctx, d.Ref, tc.key, []byte(tc.expected), []byte("audited"), now)
			if err != nil || wrote {
				t.Fatalf("swap = %t, %v; want no write", wrote, err)
			}
			if got := policyAfterRefusal(t, store, hooks, d, tc.lapsed); got != "direct" {
				t.Fatalf("policy after a refused swap = %q, want %q", got, "direct")
			}
		})
	}
	t.Run("swap creates no entry", func(t *testing.T) {
		store := newStore(t)
		d := labDevice("1", "tv", 1)
		if wrote, err := store.SwapDevicePolicy(ctx, d.Ref, d.NodeKey, d.Policy, []byte("audited"), labNow); err != nil || wrote {
			t.Fatalf("swap = %t, %v; want no write", wrote, err)
		}
		if _, err := store.Device(ctx, d.Ref, labNow); !errors.Is(err, server.ErrNotFound) {
			t.Fatalf("entry after a swap on nothing: %v, want not found", err)
		}
	})

}

func runDeleteDeviceIfConformance(t *testing.T, newStore func(*testing.T) CoordinationStore, hooks conditionalHooks) {
	t.Helper()
	ctx := context.Background()
	enrolled := func(t *testing.T) (CoordinationStore, server.Device) {
		t.Helper()
		return enrolledConditionalStore(t, newStore)
	}
	t.Run("delete removes a matching entry with its indices", func(t *testing.T) {
		store, d := enrolled(t)
		removed, err := store.DeleteDeviceIf(ctx, d.Ref, d.NodeKey, d.Policy, labNow)
		if err != nil || !removed {
			t.Fatalf("delete = %t, %v; want a removal", removed, err)
		}
		if _, err := store.Device(ctx, d.Ref, labNow); !errors.Is(err, server.ErrNotFound) {
			t.Fatalf("entry after delete: %v, want not found", err)
		}
		if _, err := store.DeviceByNodeKey(ctx, d.NodeKey, labNow); !errors.Is(err, server.ErrNotFound) {
			t.Fatalf("node key after delete: %v, want not found", err)
		}
		if list, err := store.ListDevices(ctx, d.Ref.Account, labNow); err != nil || len(list) != 0 {
			t.Fatalf("account after delete: %d entries, %v; want none", len(list), err)
		}
		if confs, err := store.Confirmations(ctx, d.Ref, labNow); err != nil || len(confs) != 0 {
			t.Fatalf("confirmations after delete: %d, %v; want none", len(confs), err)
		}
		// The key index is gone, so another device may enroll the key.
		if _, err := store.PutDevice(ctx, labDevice("1", "phone", 1), 32, labNow); err != nil {
			t.Fatalf("enroll the freed key on another device: %v", err)
		}
	})
	for _, tc := range []struct {
		name   string
		key    protocol.NodeKey
		policy string
		lapsed bool
	}{
		{"delete leaves an entry with another key", conditionalOtherKey, "direct", false},
		{"delete leaves an entry with another policy", protocol.NodeKey{1}, "audited", false},
		{"delete leaves a lapsed entry alone", protocol.NodeKey{1}, "direct", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, d := enrolled(t)
			now := labNow
			if tc.lapsed {
				now = hooks.lapse(t, store, d)
			}
			removed, err := store.DeleteDeviceIf(ctx, d.Ref, tc.key, []byte(tc.policy), now)
			if err != nil || removed {
				t.Fatalf("delete = %t, %v; want no removal", removed, err)
			}
			if got := policyAfterRefusal(t, store, hooks, d, tc.lapsed); got != "direct" {
				t.Fatalf("policy after a refused delete = %q, want the entry unchanged", got)
			}
		})
	}
}
