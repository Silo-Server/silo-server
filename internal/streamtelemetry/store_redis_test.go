package streamtelemetry

import (
	"context"
	"log/slog"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func testRedisStoreConfig(publisherID string) Config {
	cfg := DefaultConfig("node-" + publisherID)
	cfg.Enabled = true
	cfg.Distributed = true
	cfg.PublisherID = publisherID
	cfg.PublisherEpoch = 10
	cfg.KeyPrefix = "test:stelem:" + uuid.NewString()
	cfg.MembershipTTL = time.Minute
	return cfg
}

func markPlanPublished(t *testing.T, store *RedisStore, snapshot Snapshot) {
	t.Helper()
	fields, err := snapshotHashFields(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	clear(store.published)
	for field, value := range fields {
		store.published[field] = digest128(value)
	}
	store.needFullResync = false
	store.publishCount++
}

func TestRedisStorePlan(t *testing.T) {
	cfg := testRedisStoreConfig("publisher")
	cfg.FullResyncEvery = 3
	store := NewRedisStore(nil, cfg, slog.New(slog.DiscardHandler))
	base := Snapshot{PublisherID: cfg.PublisherID, PublisherEpoch: 1, Sequence: 1, CapturedAt: time.Unix(10, 0), Sessions: []SessionView{
		{SessionID: "a", RequestCount: 1, TokenIssuedAtSources: map[TokenIssuedAtSource]int64{}, Outcomes: map[httpstream.StreamOutcome]int64{}},
		{SessionID: "b", TokenIssuedAtSources: map[TokenIssuedAtSource]int64{}, Outcomes: map[httpstream.StreamOutcome]int64{}},
	}}
	sets, dels, _, full, err := store.plan(base)
	if err != nil {
		t.Fatal(err)
	}
	if !full || len(dels) != 0 || len(sets) != 3 {
		t.Fatalf("first plan: full=%v sets=%v dels=%v", full, keysOf(sets), dels)
	}
	markPlanPublished(t, store, base)

	changed := cloneSnapshot(base)
	changed.Sequence++
	changed.Sessions[0].RequestCount++
	sets, dels, _, full, err = store.plan(changed)
	if err != nil {
		t.Fatal(err)
	}
	if full || len(dels) != 0 || !reflect.DeepEqual(keysOf(sets), []string{"meta", "s:a"}) {
		t.Fatalf("changed plan: full=%v sets=%v dels=%v", full, keysOf(sets), dels)
	}
	markPlanPublished(t, store, changed)

	removed := cloneSnapshot(changed)
	removed.Sequence++
	removed.Sessions = removed.Sessions[:1]
	sets, dels, _, full, err = store.plan(removed)
	if err != nil {
		t.Fatal(err)
	}
	if full || !reflect.DeepEqual(dels, []string{"s:b"}) || !reflect.DeepEqual(keysOf(sets), []string{"meta"}) {
		t.Fatalf("removed plan: full=%v sets=%v dels=%v", full, keysOf(sets), dels)
	}

	store.needFullResync = true
	clear(store.published)
	if _, _, _, full, err = store.plan(removed); err != nil || !full {
		t.Fatalf("failed publish recovery: full=%v err=%v", full, err)
	}
	markPlanPublished(t, store, removed)
	store.publishCount = uint64(cfg.FullResyncEvery)
	if _, _, _, full, err = store.plan(removed); err != nil || !full {
		t.Fatalf("periodic resync: full=%v err=%v", full, err)
	}
}

func TestRedisStoreFailedPublishForcesFullResync(t *testing.T) {
	cfg := testRedisStoreConfig("publisher")
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisStore(client, cfg, slog.New(slog.DiscardHandler))
	snapshot := Snapshot{PublisherID: cfg.PublisherID, Sequence: 1, CapturedAt: time.Now()}
	markPlanPublished(t, store, snapshot)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Publish(ctx, snapshot); err == nil {
		t.Fatal("publish unexpectedly succeeded")
	}
	if !store.needFullResync || len(store.published) != 0 {
		t.Fatalf("failed state: full=%v published=%d", store.needFullResync, len(store.published))
	}
	if _, _, _, full, err := store.plan(snapshot); err != nil || !full {
		t.Fatalf("next plan: full=%v err=%v", full, err)
	}
}

func TestSnapshotHashFieldsRoundTripAndDeterministicCap(t *testing.T) {
	snapshot := Snapshot{PublisherID: "publisher", NodeID: "node", PublisherEpoch: 1, Sequence: 2, CapturedAt: time.Unix(3, 4),
		Sessions: []SessionView{
			{SessionID: "z", TokenIssuedAtSources: map[TokenIssuedAtSource]int64{}, Outcomes: map[httpstream.StreamOutcome]int64{}},
			{SessionID: "a", TokenIssuedAtSources: map[TokenIssuedAtSource]int64{}, Outcomes: map[httpstream.StreamOutcome]int64{}},
		}, Transfers: []TransferView{{ID: "z", Outcomes: map[httpstream.StreamOutcome]int64{}}, {ID: "a", Outcomes: map[httpstream.StreamOutcome]int64{}}}}
	encoded, err := snapshotHashFields(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	fields := make(map[string]string, len(encoded))
	for field, value := range encoded {
		fields[field] = string(value)
	}
	got, problem, err := decodeSnapshotHash("publisher", fields, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if problem.Reason != "" {
		t.Fatalf("problem = %+v", problem)
	}
	want := cloneSnapshot(snapshot)
	sort.Slice(want.Sessions, func(i, j int) bool { return want.Sessions[i].SessionID < want.Sessions[j].SessionID })
	sort.Slice(want.Transfers, func(i, j int) bool { return want.Transfers[i].ID < want.Transfers[j].ID })
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hash round trip mismatch\n got: %#v\nwant: %#v", got, want)
	}
	capped, _, err := decodeSnapshotHash("publisher", fields, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(capped.Sessions) != 1 || capped.Sessions[0].SessionID != "a" || len(capped.Transfers) != 1 || capped.Transfers[0].ID != "a" {
		t.Fatalf("deterministic cap = %+v", capped)
	}
}

func keysOf(values map[string][]byte) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// A failover to a replica that missed the last delta leaves the hash behind
// what the publisher last wrote, with the same field count. A session whose
// counters have stopped changing is never part of a later delta, so the next
// publishes must notice the rollback and rewrite the hash in full.
func TestRedisStoreLostDeltaForcesFullResync(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	cfg := testRedisStoreConfig("publisher")
	cfg.FullResyncEvery = 1000
	store := NewRedisStore(client, cfg, slog.New(slog.DiscardHandler))
	key := store.snapshotKey(cfg.PublisherID)
	at := time.Unix(100, 0)
	snapshot := Snapshot{PublisherID: cfg.PublisherID, PublisherEpoch: cfg.PublisherEpoch, Sequence: 1, CapturedAt: at, Sessions: []SessionView{
		{SessionID: "a", BytesAccepted: 100, TokenIssuedAtSources: map[TokenIssuedAtSource]int64{}, Outcomes: map[httpstream.StreamOutcome]int64{}},
		{SessionID: "b", BytesAccepted: 50, TokenIssuedAtSources: map[TokenIssuedAtSource]int64{}, Outcomes: map[httpstream.StreamOutcome]int64{}},
	}}
	publish := func() {
		t.Helper()
		if err := store.Publish(ctx, snapshot); err != nil {
			t.Fatalf("publish %d: %v", snapshot.Sequence, err)
		}
		snapshot = cloneSnapshot(snapshot)
		snapshot.Sequence++
		snapshot.CapturedAt = snapshot.CapturedAt.Add(time.Second)
	}
	storedBytes := func() int64 {
		t.Helper()
		loaded, err := store.Load(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, session := range loaded.Sessions {
			if session.SessionID == "a" {
				return session.BytesAccepted
			}
		}
		t.Fatalf("session a missing from %+v", loaded.Sessions)
		return 0
	}

	publish()
	lost := client.HGetAll(ctx, key).Val()
	snapshot.Sessions[0].BytesAccepted = 200 // the delta the failover loses
	publish()
	if store.needFullResync {
		t.Fatal("a delta that landed forced a full resync")
	}

	// The new primary holds the hash as it was before that delta.
	client.Del(ctx, key)
	for field, value := range lost {
		client.HSet(ctx, key, field, value)
	}

	// Session a has paused: its counters no longer change. The first publish
	// is a delta onto the rolled-back hash; the next one must be full.
	publish()
	if !store.needFullResync {
		t.Fatalf("rollback went unnoticed: session a bytes = %d", storedBytes())
	}
	publish()
	if got := storedBytes(); got != 200 || store.needFullResync {
		t.Fatalf("after the full resync: session a bytes = %d (want 200), resync still pending = %v", got, store.needFullResync)
	}
}
