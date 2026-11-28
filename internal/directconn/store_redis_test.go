package directconn

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
	"github.com/darthShadow/shunpike/coordination/server/storetest"
	"github.com/redis/go-redis/v9"
)

// failingRedisHook fails every command and pipeline before it reaches the network.
type failingRedisHook struct{}

func (failingRedisHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (failingRedisHook) ProcessHook(redis.ProcessHook) redis.ProcessHook {
	return func(context.Context, redis.Cmder) error { return errors.New("redis down") }
}
func (failingRedisHook) ProcessPipelineHook(redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(context.Context, []redis.Cmder) error { return errors.New("redis down") }
}

// replayRedisHook sends the next script run twice, as the Redis client does when
// the connection drops after the server applied the command and before the reply
// arrived: the first run's reply is lost and the caller sees the second run's.
type replayRedisHook struct{ armed atomic.Bool }

func (*replayRedisHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *replayRedisHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if err != nil || (cmd.Name() != "evalsha" && cmd.Name() != "eval") || !h.armed.CompareAndSwap(true, false) {
			return err
		}
		return next(ctx, cmd)
	}
}
func (*replayRedisHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// A write that the client sends again after the server applied it reports the
// key it replaced, not a renewal of the key it installed. A later write with its
// own revision is not taken for a replay.
func TestRedisPutDeviceReplayReportsTheKeyItReplaced(t *testing.T) {
	client, store := miniredisStore(t)
	hook := &replayRedisHook{}
	client.AddHook(hook)
	ctx := t.Context()
	ref := server.DeviceRef{Account: "7", Device: "tv"}
	keyA, keyB, keyC := protocol.NodeKey{1}, protocol.NodeKey{2}, protocol.NodeKey{3}
	put := func(key protocol.NodeKey, revision string, replay bool) protocol.NodeKey {
		t.Helper()
		hook.armed.Store(replay)
		d := server.Device{Ref: ref, NodeKey: key, Policy: marshalPolicy(devicePolicy{ProfileID: "p", SessionID: "s", Revision: revision}), LeaseExpires: labNow.Add(time.Minute)}
		replaced, err := store.PutDeviceReplacing(ctx, d, 32, labNow)
		if err != nil {
			t.Fatalf("PutDeviceReplacing(key %v, revision %q): %v", key[0], revision, err)
		}
		if hook.armed.Load() {
			t.Fatalf("the hook did not send the write for revision %q twice", revision)
		}
		return replaced
	}

	if got := put(keyA, "r1", false); !got.IsZero() {
		t.Fatalf("first enrollment replaced %v, want no key", got)
	}
	if got := put(keyB, "r2", true); got != keyA {
		t.Fatalf("replayed re-key replaced %v, want the original key %v", got, keyA)
	}
	if got := put(keyC, "r3", false); got != keyB {
		t.Fatalf("later re-key replaced %v, want %v", got, keyB)
	}
}

func TestRedisKeyLayout(t *testing.T) {
	ref := server.DeviceRef{Account: "42", Device: "a:b"}
	// Bytes 0xFB 0xFF encode to sextets 62, 63, 60. RFC 4648 section 5 maps
	// them to '-', '_', '8'; the standard alphabet would give "+/8=" and a
	// padded URL encoding "-_8=".
	urlSafeRef := server.DeviceRef{Account: "42", Device: "\xfb\xff"}
	nodeKey := protocol.NodeKey{0xAB, 0x01}
	nkHex := "ab01" + strings.Repeat("00", 30)
	tests := []struct{ name, got, want string }{
		{"device id keeps ':' out of the key", devKey(ref), "silo:direct:dev:42:YTpi"},
		{"device id is unpadded base64url", devKey(urlSafeRef), "silo:direct:dev:42:-_8"},
		{"registration", regKey(ref), "silo:direct:reg:42:YTpi"},
		{"confirmation", confKey("api", ref), "silo:direct:conf:api:42:YTpi"},
		{"confirmation index", confIndexKey("api"), "silo:direct:confidx:api"},
		{"account index", acctKey("42"), "silo:direct:acct:42"},
		{"host", hostKey("api"), "silo:direct:host:api"},
		{"node key is lowercase hex", nkKey(nodeKey), "silo:direct:nk:" + nkHex},
		{"owner value", ownerValue(ref), "42:YTpi"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
	if devKey(server.DeviceRef{Account: "42", Device: "a"}) == devKey(server.DeviceRef{Account: "42:a", Device: ""}) {
		t.Error("distinct references share a device key")
	}
}

func TestRedisOwnerValueRoundTrip(t *testing.T) {
	for _, id := range []server.DeviceID{"plain", "with:colon", "with:many:colons:", "ünï", "a/b+c=="} {
		ref := server.DeviceRef{Account: "7", Device: id}
		got, err := parseOwner(ownerValue(ref))
		if err != nil || got != ref {
			t.Errorf("parseOwner(ownerValue(%q)) = %+v, %v", id, got, err)
		}
	}
	for _, bad := range []string{"", "7", "7:", "7:!!!"} {
		if _, err := parseOwner(bad); err == nil {
			t.Errorf("parseOwner(%q) accepted a malformed value", bad)
		}
	}
}

func TestRedisStoreMapsRedisErrorsToUnavailable(t *testing.T) {
	client := redis.NewClient(&redis.Options{Dialer: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("unexpected network call")
	}})
	t.Cleanup(func() { _ = client.Close() })
	client.AddHook(failingRedisHook{})
	store := newRedisStore(client, []string{"api"}, labFence)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ref := server.DeviceRef{Account: "1", Device: "d"}
	dev := server.Device{Ref: ref, NodeKey: protocol.NodeKey{1}, LeaseExpires: now.Add(time.Minute)}
	reg := protocol.Registration{NodeKey: dev.NodeKey, DiscoKey: protocol.DiscoKey{1}, Endpoints: []netip.AddrPort{netip.MustParseAddrPort("192.0.2.1:1")}}
	host := server.HostEntry{Label: "api", NodeKey: protocol.NodeKey{2}, DiscoKey: protocol.DiscoKey{2}, PresenceExpires: now.Add(time.Minute)}
	conf := protocol.Confirmation{Peer: dev.NodeKey, DiscoKey: protocol.DiscoKey{1}, Endpoint: netip.MustParseAddrPort("192.0.2.1:1")}
	calls := map[string]func() error{
		"PutDevice":       func() error { _, err := store.PutDevice(ctx, dev, 32, now); return err },
		"Device":          func() error { _, err := store.Device(ctx, ref, now); return err },
		"DeviceByNodeKey": func() error { _, err := store.DeviceByNodeKey(ctx, dev.NodeKey, now); return err },
		"ListDevices":     func() error { _, err := store.ListDevices(ctx, "1", now); return err },
		"DeleteDevice":    func() error { return store.DeleteDevice(ctx, ref) },
		"PutRegistration": func() error { return store.PutRegistration(ctx, ref, reg, now.Add(time.Second), now) },
		"PutHost":         func() error { return store.PutHost(ctx, host, now) },
		"DeleteHost":      func() error { return store.DeleteHost(ctx, "api") },
		"Hosts":           func() error { _, err := store.Hosts(ctx, now); return err },
		"PutConfirmation": func() error { return store.PutConfirmation(ctx, "api", ref, &conf, now.Add(time.Second), now) },
		"Confirmations":   func() error { _, err := store.Confirmations(ctx, ref, now); return err },
		"SwapDevicePolicy": func() error {
			_, err := store.SwapDevicePolicy(ctx, ref, dev.NodeKey, nil, []byte("p"), now)
			return err
		},
		"DeleteDeviceIf": func() error { _, err := store.DeleteDeviceIf(ctx, ref, dev.NodeKey, nil, now); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, server.ErrUnavailable) {
			t.Errorf("%s error = %v, want server.ErrUnavailable", name, err)
		}
	}
}

func TestRedisStoreRejectsUnknownHostLabel(t *testing.T) {
	store := newRedisStore(redis.NewClient(&redis.Options{Dialer: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("unexpected network call")
	}}), []string{"api"}, labFence)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	err := store.PutHost(context.Background(), server.HostEntry{Label: "other", PresenceExpires: now.Add(time.Minute)}, now)
	if err == nil || errors.Is(err, server.ErrUnavailable) {
		t.Fatalf("PutHost with an unconfigured label = %v, want a non-availability error before any Redis call", err)
	}
}

// The store cases below run against miniredis. The disposable-Redis lab file
// (store_redis_lab_test.go) keeps the cases that need a real server and shares
// these fixtures with it.
var labLabels = []string{"api", "host"}

var labNow = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// labFence is the presence ID the test stores write host entries and
// confirmations under; seedLease makes it the engine lease's value.
const labFence = "test-replica:fence"

// seedLease sets the engine lease to fence with no expiry, as the replica
// monitor of the process with that presence ID would.
func seedLease(t *testing.T, client *redis.Client, fence string) {
	t.Helper()
	if err := client.Set(t.Context(), engineLeaseKey, fence, 0).Err(); err != nil {
		t.Fatalf("seed engine lease: %v", err)
	}
}

func labDevice(account, id string, key byte) server.Device {
	return server.Device{
		Ref:          server.DeviceRef{Account: server.AccountID(account), Device: server.DeviceID(id)},
		NodeKey:      protocol.NodeKey{key},
		Policy:       []byte("direct"),
		LeaseExpires: labNow.Add(time.Minute),
	}
}

func labRegistration(d server.Device) protocol.Registration {
	return protocol.Registration{NodeKey: d.NodeKey, DiscoKey: protocol.DiscoKey{1}, Endpoints: []netip.AddrPort{netip.MustParseAddrPort("192.0.2.1:1234")}}
}

func miniredisStore(t *testing.T) (*redis.Client, *redisStore) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client, newRedisStore(client, labLabels, labFence)
}

// redisClockStore runs each call of shunpike's store suite with Redis time at
// the call's now: it sets miniredis time to now and moves every TTL by the
// forward step, so the suite's clock and Redis time agree. A step back moves no
// TTL. One fixture uses one redisClockStore.
type redisClockStore struct {
	*redisStore
	mr   *miniredis.Miniredis
	mu   sync.Mutex
	last time.Time
}

func (s *redisClockStore) at(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.last.IsZero() && now.After(s.last) {
		s.mr.FastForward(now.Sub(s.last))
	}
	s.mr.SetTime(now)
	s.last = now
}

func (s *redisClockStore) PutDevice(ctx context.Context, d server.Device, maxPerAccount int, now time.Time) (bool, error) {
	s.at(now)
	return s.redisStore.PutDevice(ctx, d, maxPerAccount, now)
}

func (s *redisClockStore) Device(ctx context.Context, ref server.DeviceRef, now time.Time) (server.Device, error) {
	s.at(now)
	return s.redisStore.Device(ctx, ref, now)
}

func (s *redisClockStore) DeviceByNodeKey(ctx context.Context, k protocol.NodeKey, now time.Time) (server.Device, error) {
	s.at(now)
	return s.redisStore.DeviceByNodeKey(ctx, k, now)
}

func (s *redisClockStore) ListDevices(ctx context.Context, account server.AccountID, now time.Time) ([]server.Device, error) {
	s.at(now)
	return s.redisStore.ListDevices(ctx, account, now)
}

func (s *redisClockStore) PutRegistration(ctx context.Context, ref server.DeviceRef, reg protocol.Registration, presenceExpires, now time.Time) error {
	s.at(now)
	return s.redisStore.PutRegistration(ctx, ref, reg, presenceExpires, now)
}

func (s *redisClockStore) PutHost(ctx context.Context, h server.HostEntry, now time.Time) error {
	s.at(now)
	return s.redisStore.PutHost(ctx, h, now)
}

func (s *redisClockStore) Hosts(ctx context.Context, now time.Time) ([]server.HostEntry, error) {
	s.at(now)
	return s.redisStore.Hosts(ctx, now)
}

func (s *redisClockStore) PutConfirmation(ctx context.Context, hostLabel string, ref server.DeviceRef, c *protocol.Confirmation, expires, now time.Time) error {
	s.at(now)
	return s.redisStore.PutConfirmation(ctx, hostLabel, ref, c, expires, now)
}

func (s *redisClockStore) Confirmations(ctx context.Context, ref server.DeviceRef, now time.Time) (map[string]protocol.Confirmation, error) {
	s.at(now)
	return s.redisStore.Confirmations(ctx, ref, now)
}

func TestRedisStoreConformance(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	storetest.Run(t, func(t *testing.T) server.Store {
		mr.FlushAll()
		seedLease(t, client, labFence)
		return &redisClockStore{redisStore: newRedisStore(client, labLabels, labFence), mr: mr}
	})
	runConditionalStoreConformance(t, func(t *testing.T) CoordinationStore {
		mr.FlushAll()
		// Unset miniredis time follows the wall clock; the cases run at labNow.
		mr.SetTime(labNow)
		seedLease(t, client, labFence)
		return newRedisStore(client, labLabels, labFence)
	}, redisConditionalHooks)
}

// redisTimeStore returns a store on a fresh miniredis whose time is base.
func redisTimeStore(t *testing.T, base time.Time) (*miniredis.Miniredis, *redisStore) {
	t.Helper()
	mr := miniredis.RunT(t)
	mr.SetTime(base)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return mr, newRedisStore(client, labLabels, labFence)
}

// TestRedisStoreUsesRedisTime: two replicas whose clocks sit 30 s on either side
// of Redis time agree on which enrollments are live, because the store counts
// every deadline as a lifetime from the caller's now and decides expiry with
// Redis time.
func TestRedisStoreUsesRedisTime(t *testing.T) {
	const (
		skew     = 30 * time.Second
		lifetime = 600 * time.Second
	)
	base := labNow
	// A's clock is skew behind Redis time and B's is skew ahead.
	aNow := func(redisNow time.Time) time.Time { return redisNow.Add(-skew) }
	bNow := func(redisNow time.Time) time.Time { return redisNow.Add(skew) }
	enroll := func(t *testing.T, store *redisStore, d server.Device, callerNow time.Time) error {
		t.Helper()
		d.LeaseExpires = callerNow.Add(lifetime)
		_, err := store.PutDevice(t.Context(), d, maxDirectDevicesPerAccount, callerNow)
		return err
	}
	listed := func(t *testing.T, store *redisStore, ref server.DeviceRef, callerNow time.Time) bool {
		t.Helper()
		list, err := store.ListDevices(t.Context(), ref.Account, callerNow)
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(list, func(d server.Device) bool { return d.Ref == ref })
	}
	t.Run("an enrollment by a slow clock stays live and counted for a fast clock", func(t *testing.T) {
		mr, store := redisTimeStore(t, base)
		d := labDevice("1", "slow-clock", 1)
		if err := enroll(t, store, d, aNow(base)); err != nil {
			t.Fatalf("A enrolls D: %v", err)
		}
		for i := range maxDirectDevicesPerAccount - 2 {
			if err := enroll(t, store, labDevice("1", fmt.Sprintf("fill-%02d", i), byte(i+2)), bNow(base)); err != nil {
				t.Fatalf("fill %d: %v", i, err)
			}
		}
		at := base.Add(580 * time.Second)
		mr.SetTime(at)
		mr.FastForward(580 * time.Second)
		if !listed(t, store, d.Ref, bNow(at)) {
			t.Fatalf("B's ListDevices at Redis +580s omits D, whose 600 s lease A wrote at Redis +0s")
		}
		if err := enroll(t, store, labDevice("1", "32nd", 100), bNow(at)); err != nil {
			t.Fatalf("B's 32nd enrollment: %v, want it admitted", err)
		}
		if err := enroll(t, store, labDevice("1", "33rd", 101), bNow(at)); !errors.Is(err, server.ErrDeviceLimit) {
			t.Fatalf("B's 33rd enrollment: %v, want server.ErrDeviceLimit", err)
		}
	})
	t.Run("an enrollment lapses at its Redis deadline for every clock", func(t *testing.T) {
		mr, store := redisTimeStore(t, base)
		d := labDevice("1", "slow-clock", 1)
		if err := enroll(t, store, d, aNow(base)); err != nil {
			t.Fatal(err)
		}
		// SetTime alone keeps the key's TTL, so only the store's Redis-time
		// check can drop D.
		at := base.Add(601 * time.Second)
		mr.SetTime(at)
		for name, callerNow := range map[string]time.Time{"A": aNow(at), "B": bNow(at)} {
			if listed(t, store, d.Ref, callerNow) {
				t.Errorf("%s's ListDevices at Redis +601s lists D, whose lease ended at Redis +600s", name)
			}
		}
	})
	t.Run("a registration by a slow clock is present for a fast clock", func(t *testing.T) {
		mr, store := redisTimeStore(t, base)
		d := labDevice("1", "slow-clock", 1)
		if err := enroll(t, store, d, aNow(base)); err != nil {
			t.Fatal(err)
		}
		if err := store.PutRegistration(t.Context(), d.Ref, labRegistration(d), aNow(base).Add(90*time.Second), aNow(base)); err != nil {
			t.Fatal(err)
		}
		at := base.Add(61 * time.Second)
		mr.SetTime(at)
		mr.FastForward(61 * time.Second)
		got, err := store.Device(t.Context(), d.Ref, bNow(at))
		if err != nil {
			t.Fatal(err)
		}
		if got.Registration == nil {
			t.Fatal("B's Device at Redis +61s has no registration; A wrote a 90 s presence at Redis +0s")
		}
		if want := bNow(at).Add(29 * time.Second); !got.PresenceExpires.Equal(want) {
			t.Fatalf("PresenceExpires = %v, want B's now + 29s = %v", got.PresenceExpires, want)
		}
	})
	t.Run("a returned lease end is on the reader's clock", func(t *testing.T) {
		mr, store := redisTimeStore(t, base)
		d := labDevice("1", "slow-clock", 1)
		if err := enroll(t, store, d, aNow(base)); err != nil {
			t.Fatal(err)
		}
		at := base.Add(580 * time.Second)
		mr.SetTime(at)
		mr.FastForward(580 * time.Second)
		got, err := store.Device(t.Context(), d.Ref, bNow(at))
		if err != nil {
			t.Fatal(err)
		}
		if want := bNow(at).Add(20 * time.Second); !got.LeaseExpires.Equal(want) {
			t.Fatalf("LeaseExpires = %v, want B's now + 20s = %v", got.LeaseExpires, want)
		}
	})
}

// TestRedisFencedWritesNeedTheLease: host and confirmation writes, and the host
// withdrawal, go through only while the engine lease holds the store's fence.
func TestRedisFencedWritesNeedTheLease(t *testing.T) {
	ref := server.DeviceRef{Account: "1", Device: "tv"}
	host := func(disco byte) server.HostEntry {
		return server.HostEntry{Label: "api", NodeKey: protocol.NodeKey{8}, DiscoKey: protocol.DiscoKey{disco},
			Endpoints: []netip.AddrPort{netip.MustParseAddrPort("192.0.2.8:41641")}, PresenceExpires: labNow.Add(time.Minute)}
	}
	conf := func(disco byte) *protocol.Confirmation {
		return &protocol.Confirmation{Peer: protocol.NodeKey{1}, DiscoKey: protocol.DiscoKey{disco}, Endpoint: netip.MustParseAddrPort("192.0.2.1:1234")}
	}
	// fixture writes a host entry and a confirmation under the lease, then
	// leaves the lease as lease ("" deletes it).
	fixture := func(t *testing.T, lease string) (*redis.Client, *redisStore) {
		t.Helper()
		client, store := miniredisStore(t)
		seedLease(t, client, labFence)
		if err := store.PutHost(t.Context(), host(1), labNow); err != nil {
			t.Fatal(err)
		}
		if err := store.PutConfirmation(t.Context(), "api", ref, conf(1), labNow.Add(time.Minute), labNow); err != nil {
			t.Fatal(err)
		}
		if lease == "" {
			if err := client.Del(t.Context(), engineLeaseKey).Err(); err != nil {
				t.Fatal(err)
			}
		} else {
			seedLease(t, client, lease)
		}
		return client, store
	}
	raw := func(t *testing.T, client *redis.Client) map[string]string {
		t.Helper()
		got := make(map[string]string)
		for _, key := range []string{hostKey("api"), confKey("api", ref)} {
			v, err := client.Get(t.Context(), key).Result()
			if err != nil && !errors.Is(err, redis.Nil) {
				t.Fatal(err)
			}
			got[key] = v
		}
		return got
	}
	writes := map[string]func(ctx context.Context, store *redisStore) error{
		"PutHost": func(ctx context.Context, store *redisStore) error { return store.PutHost(ctx, host(2), labNow) },
		"PutConfirmation set": func(ctx context.Context, store *redisStore) error {
			return store.PutConfirmation(ctx, "api", ref, conf(2), labNow.Add(time.Minute), labNow)
		},
		"PutConfirmation clear": func(ctx context.Context, store *redisStore) error {
			return store.PutConfirmation(ctx, "api", ref, nil, time.Time{}, labNow)
		},
		"DeleteHost": func(ctx context.Context, store *redisStore) error { return store.DeleteHost(ctx, "api") },
	}
	for _, lease := range []struct{ name, value string }{{"lease unset", ""}, {"lease held by another process", "other-replica:id"}} {
		for name, write := range writes {
			t.Run(lease.name+"/"+name, func(t *testing.T) {
				client, store := fixture(t, lease.value)
				before := raw(t, client)
				if err := write(t.Context(), store); !errors.Is(err, errNotLeaseHolder) {
					t.Fatalf("%s error = %v, want errNotLeaseHolder", name, err)
				}
				if after := raw(t, client); !maps.Equal(after, before) {
					t.Fatalf("%s refused but changed the keys: %q, want %q", name, after, before)
				}
			})
		}
	}
	for name, write := range writes {
		t.Run("lease held by this process/"+name, func(t *testing.T) {
			client, store := fixture(t, labFence)
			before := raw(t, client)
			if err := write(t.Context(), store); err != nil {
				t.Fatalf("%s under the lease: %v", name, err)
			}
			if after := raw(t, client); maps.Equal(after, before) {
				t.Fatalf("%s under the lease left the keys as they were: %q", name, after)
			}
		})
	}
	t.Run("a predecessor's host withdrawal leaves the successor's entries", func(t *testing.T) {
		client, successor := miniredisStore(t)
		predecessor := newRedisStore(client, labLabels, "predecessor:id")
		seedLease(t, client, labFence)
		if err := successor.PutHost(t.Context(), host(2), labNow); err != nil {
			t.Fatal(err)
		}
		if err := successor.PutConfirmation(t.Context(), "api", ref, conf(2), labNow.Add(time.Minute), labNow); err != nil {
			t.Fatal(err)
		}
		if err := predecessor.DeleteHost(t.Context(), "api"); !errors.Is(err, errNotLeaseHolder) {
			t.Fatalf("predecessor DeleteHost = %v, want errNotLeaseHolder", err)
		}
		hosts, err := successor.Hosts(t.Context(), labNow)
		if err != nil || len(hosts) != 1 || hosts[0].DiscoKey != host(2).DiscoKey {
			t.Fatalf("hosts after the predecessor's withdrawal = %+v, %v; want the successor's", hosts, err)
		}
		confs, err := successor.Confirmations(t.Context(), ref, labNow)
		if err != nil || confs["api"] != *conf(2) {
			t.Fatalf("confirmations after the predecessor's withdrawal = %+v, %v; want the successor's", confs, err)
		}
	})
}

func TestRedisPutRegistrationConditional(t *testing.T) {
	client, store := miniredisStore(t)
	ctx := t.Context()
	assertNoRegistration := func(d server.Device, why string) {
		t.Helper()
		if n, err := client.Exists(ctx, regKey(d.Ref)).Result(); err != nil || n != 0 {
			t.Fatalf("%s: registration key exists=%d err=%v; want nothing written", why, n, err)
		}
	}
	d := labDevice("1", "never-enrolled", 1)
	if err := store.PutRegistration(ctx, d.Ref, labRegistration(d), labNow.Add(time.Minute), labNow); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("never enrolled: %v", err)
	}
	assertNoRegistration(d, "never enrolled")

	d = labDevice("1", "deleted", 2)
	if _, err := store.PutDevice(ctx, d, 32, labNow); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteDevice(ctx, d.Ref); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRegistration(ctx, d.Ref, labRegistration(d), labNow.Add(time.Minute), labNow); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("deleted entry: %v", err)
	}
	assertNoRegistration(d, "deleted entry")

	d = labDevice("1", "rekeyed", 3)
	if _, err := store.PutDevice(ctx, d, 32, labNow); err != nil {
		t.Fatal(err)
	}
	stale := labRegistration(d)
	rekeyed := d
	rekeyed.NodeKey = protocol.NodeKey{4}
	if replaced, err := store.PutDevice(ctx, rekeyed, 32, labNow); err != nil || !replaced {
		t.Fatalf("rekey = %v, %v", replaced, err)
	}
	if err := store.PutRegistration(ctx, d.Ref, stale, labNow.Add(time.Minute), labNow); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("changed node key: %v", err)
	}
	assertNoRegistration(d, "changed node key")
	if err := store.PutRegistration(ctx, d.Ref, labRegistration(rekeyed), labNow.Add(time.Minute), labNow); err != nil {
		t.Fatalf("current node key: %v", err)
	}
}

// The session selectors withdraw through the conditional delete the Redis store
// runs as a script: the entry, its registration and its node-key index go, and
// another session's entry stays, whether the revocation names the ending session
// or the session a password change keeps.
func TestRedisRevokeBySession(t *testing.T) {
	_, store := miniredisStore(t)
	ctx := t.Context()
	c, err := NewCoordination(CoordinationConfig{Store: store, AuthSecret: testAuthSecret, Clock: func() time.Time { return labNow }})
	if err != nil {
		t.Fatal(err)
	}
	enroll := func(session, device string, key byte) Enrollment {
		t.Helper()
		e, err := c.Enroll(ctx, EnrollRequest{UserID: 1, ProfileID: "profile-1", SessionID: session, DeviceID: device, NodeKey: testNodeKey(key), ConnectionPath: "automatic"})
		if err != nil {
			t.Fatalf("Enroll(%q, %q) = %v", session, device, err)
		}
		return e
	}
	tv, phone, watch := enroll("session-a", "tv", 1), enroll("session-b", "phone", 2), enroll("session-a", "watch", 3)
	tvRef := server.DeviceRef{Account: "1", Device: "tv"}
	if err := store.PutRegistration(ctx, tvRef, labRegistration(server.Device{NodeKey: testNodeKey(1)}), labNow.Add(time.Minute), labNow); err != nil {
		t.Fatal(err)
	}

	c.RevokeSession(ctx, 1, "session-a")

	for name, ticket := range map[string]string{"tv": tv.Ticket, "watch": watch.Ticket} {
		if _, _, err := c.Authorize(ctx, ticket); !errors.Is(err, ErrInvalidTicket) {
			t.Errorf("%s ticket after its session ended: %v, want invalid", name, err)
		}
	}
	if _, _, err := c.Authorize(ctx, phone.Ticket); err != nil {
		t.Fatalf("another session's entry after RevokeSession: %v", err)
	}
	if _, err := store.DeviceByNodeKey(ctx, testNodeKey(1), labNow); !errors.Is(err, server.ErrNotFound) {
		t.Errorf("node-key index of the withdrawn entry: %v, want ErrNotFound", err)
	}
	if err := store.PutRegistration(ctx, tvRef, labRegistration(server.Device{NodeKey: testNodeKey(1)}), labNow.Add(time.Minute), labNow); !errors.Is(err, server.ErrNotFound) {
		t.Errorf("registration of the withdrawn entry: %v, want ErrNotFound", err)
	}

	c.RevokeAccountExceptSession(ctx, 1, "session-b", "tv")
	if _, _, err := c.Authorize(ctx, phone.Ticket); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("phone entry after a password change kept session-b on the tv: %v, want invalid", err)
	}
}
