package directconn

import (
	"context"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
	"github.com/redis/go-redis/v9"
)

// recordRedisHook keeps the name of every command the client sends.
type recordRedisHook struct {
	mu    sync.Mutex
	names []string
}

func (*recordRedisHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *recordRedisHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		h.mu.Lock()
		h.names = append(h.names, cmd.Name())
		h.mu.Unlock()
		return next(ctx, cmd)
	}
}
func (*recordRedisHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func (h *recordRedisHook) sent(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Contains(h.names, name)
}

func indexTestConfirmation() *protocol.Confirmation {
	return &protocol.Confirmation{Peer: protocol.NodeKey{1}, DiscoKey: protocol.DiscoKey{1}, Endpoint: netip.MustParseAddrPort("192.0.2.1:1234")}
}

// Removing a host's confirmations reads the host's own index; it walks no
// keyspace. A shared Redis can hold any number of other keys, and one walk over
// them under the engine's one store deadline would never finish.
func TestRedisDeleteHostReadsItsIndexAndScansNothing(t *testing.T) {
	client, store := miniredisStore(t)
	seedLease(t, client, labFence)
	ctx := t.Context()
	hook := &recordRedisHook{}
	client.AddHook(hook)

	refs := []server.DeviceRef{{Account: "1", Device: "tv"}, {Account: "1", Device: "phone"}, {Account: "2", Device: "a:b"}}
	for _, ref := range refs {
		for _, label := range []string{"api", "host"} {
			if err := store.PutConfirmation(ctx, label, ref, indexTestConfirmation(), labNow.Add(time.Minute), labNow); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := range 300 {
		if err := client.Set(ctx, "other:app:"+strconv.Itoa(i), "v", 0).Err(); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.DeleteHost(ctx, "api"); err != nil {
		t.Fatalf("DeleteHost: %v", err)
	}

	if hook.sent("scan") {
		t.Fatal("DeleteHost sent SCAN; want it to delete the host's confirmations from the host's index")
	}
	for _, ref := range refs {
		if n, err := client.Exists(ctx, confKey("api", ref)).Result(); err != nil || n != 0 {
			t.Errorf("confirmation %v under the removed host: exists %d, err %v; want gone", ref, n, err)
		}
		if n, err := client.Exists(ctx, confKey("host", ref)).Result(); err != nil || n != 1 {
			t.Errorf("confirmation %v under the other host: exists %d, err %v; want kept", ref, n, err)
		}
	}
	if n, err := client.Exists(ctx, "silo:direct:confidx:api").Result(); err != nil || n != 0 {
		t.Errorf("index of the removed host: exists %d, err %v; want gone", n, err)
	}
	if n, err := client.Exists(ctx, "silo:direct:confidx:host").Result(); err != nil || n != 1 {
		t.Errorf("index of the other host: exists %d, err %v; want kept", n, err)
	}
	if n, err := client.Keys(ctx, "other:app:*").Result(); err != nil || len(n) != 300 {
		t.Errorf("unrelated keys after DeleteHost: %d, err %v; want 300", len(n), err)
	}
}

// miniredisClockStore is miniredisStore with the server's clock in hand. Redis
// time is the instant mr.SetTime names; an expiry moves only with FastForward,
// and neither follows the clock the caller passes to a store method.
func miniredisClockStore(t *testing.T) (*miniredis.Miniredis, *redis.Client, *redisStore) {
	t.Helper()
	mr := miniredis.RunT(t)
	mr.SetTime(labNow)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	seedLease(t, client, labFence)
	return mr, client, newRedisStore(client, labLabels, labFence)
}

// The index lists a host's live confirmations: a clear takes one out, and a
// write prunes members whose lifetime has passed on Redis time, the clock the
// confirmation key itself expires on. It lives only as long as its longest
// member, so an idle host leaves no key behind.
func TestRedisConfirmationIndexTracksLiveConfirmations(t *testing.T) {
	mr, client, store := miniredisClockStore(t)
	ctx := t.Context()
	idx := "silo:direct:confidx:api"
	tv, phone, speaker := server.DeviceRef{Account: "1", Device: "tv"}, server.DeviceRef{Account: "1", Device: "phone"}, server.DeviceRef{Account: "1", Device: "speaker"}
	members := func() []string {
		t.Helper()
		got, err := client.ZRange(ctx, idx, 0, -1).Result()
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(got)
		return got
	}
	put := func(ref server.DeviceRef, lifetime time.Duration) {
		t.Helper()
		if err := store.PutConfirmation(ctx, "api", ref, indexTestConfirmation(), labNow.Add(lifetime), labNow); err != nil {
			t.Fatal(err)
		}
	}

	put(tv, time.Second)
	put(phone, time.Minute)
	if got, want := members(), []string{confKey("api", phone), confKey("api", tv)}; !slices.Equal(got, want) {
		t.Fatalf("index after two writes = %q, want %q", got, want)
	}

	if err := store.PutConfirmation(ctx, "api", phone, nil, time.Time{}, labNow); err != nil {
		t.Fatal(err)
	}
	if got, want := members(), []string{confKey("api", tv)}; !slices.Equal(got, want) {
		t.Fatalf("index after a clear = %q, want %q", got, want)
	}

	// Redis time moves two seconds; the caller's clock stays where it was. The
	// tv's confirmation has lapsed, so the next write prunes it.
	mr.FastForward(2 * time.Second)
	mr.SetTime(labNow.Add(2 * time.Second))
	put(speaker, time.Second)
	if got, want := members(), []string{confKey("api", speaker)}; !slices.Equal(got, want) {
		t.Fatalf("index after the tv's lifetime passed on Redis time = %q, want %q", got, want)
	}
	if ttl, err := client.PTTL(ctx, idx).Result(); err != nil || ttl <= 0 || ttl > time.Second {
		t.Fatalf("index lifetime = %v, err %v; want what its longest member has left, at most 1s", ttl, err)
	}
}

// The caller's clock plays no part in the index. A caller whose clock steps
// forward while a confirmation is live must not drop it from the index, or
// removing the host would leave that confirmation beside the next host's entry
// for the rest of its lifetime.
func TestRedisConfirmationIndexIgnoresACallerClockStep(t *testing.T) {
	_, client, store := miniredisClockStore(t)
	ctx := t.Context()
	tv, phone := server.DeviceRef{Account: "1", Device: "tv"}, server.DeviceRef{Account: "1", Device: "phone"}
	if err := store.PutConfirmation(ctx, "api", tv, indexTestConfirmation(), labNow.Add(time.Minute), labNow); err != nil {
		t.Fatal(err)
	}

	// The caller's clock steps ten minutes forward, Redis time does not: the
	// tv's confirmation is still live on the key's own clock.
	stepped := labNow.Add(10 * time.Minute)
	if err := store.PutConfirmation(ctx, "api", phone, indexTestConfirmation(), stepped.Add(time.Minute), stepped); err != nil {
		t.Fatal(err)
	}
	if n, err := client.Exists(ctx, confKey("api", tv)).Result(); err != nil || n != 1 {
		t.Fatalf("fixture: the tv's confirmation exists %d, err %v; want live", n, err)
	}

	if err := store.DeleteHost(ctx, "api"); err != nil {
		t.Fatalf("DeleteHost: %v", err)
	}
	for _, ref := range []server.DeviceRef{tv, phone} {
		if n, err := client.Exists(ctx, confKey("api", ref)).Result(); err != nil || n != 0 {
			t.Errorf("confirmation %v after DeleteHost: exists %d, err %v; want gone", ref, n, err)
		}
	}
}
