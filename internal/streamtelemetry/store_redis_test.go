package streamtelemetry

import (
	"context"
	"log/slog"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/httpstream"
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

func TestRedisStoreKeyBuilders(t *testing.T) {
	cfg := testRedisStoreConfig("publisher")
	store := NewRedisStore(nil, cfg, nil)
	if got := store.snapshotKey("other"); got != cfg.KeyPrefix+":snap:other" {
		t.Fatalf("snapshot key = %q", got)
	}
	if got := store.rosterKey(); got != cfg.KeyPrefix+":roster" {
		t.Fatalf("roster key = %q", got)
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
