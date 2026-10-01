package streamtelemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
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

func redisTestClient(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("SILO_TEST_REDIS_URL")
	if url == "" {
		url = "redis://127.0.0.1:6380/15"
	}
	options, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("parse SILO_TEST_REDIS_URL: %v", err)
	}
	client := redis.NewClient(options)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		t.Skipf("Redis integration test skipped: no server answers %s: %v", url, err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestRedisStoreIntegration(t *testing.T) {
	client := redisTestClient(t)
	ctx := context.Background()
	newStore := func(prefix, publisher string) *RedisStore {
		cfg := testRedisStoreConfig(publisher)
		cfg.KeyPrefix = prefix
		return NewRedisStore(client, cfg, slog.New(slog.DiscardHandler))
	}
	t.Run("publish delta load and leave", func(t *testing.T) {
		prefix := "test:stelem:" + uuid.NewString()
		store := newStore(prefix, "p1")
		t.Cleanup(func() { _ = client.Del(ctx, store.rosterKey(), store.snapshotKey("p1")).Err() })
		first := Snapshot{PublisherID: "p1", NodeID: "n1", PublisherEpoch: 1, Sequence: 1, CapturedAt: time.Now(), Sessions: []SessionView{{SessionID: "a"}, {SessionID: "removed"}}}
		if err := store.Publish(ctx, first); err != nil {
			t.Fatal(err)
		}
		set, err := store.LoadAll(ctx)
		if err != nil || len(set.Snapshots) != 1 || len(set.Snapshots[0].Sessions) != 2 {
			t.Fatalf("first load = %+v, %v", set, err)
		}
		second := cloneSnapshot(first)
		second.Sequence++
		second.CapturedAt = time.Now()
		second.Sessions = []SessionView{{SessionID: "a", RequestCount: 9}}
		if err := store.Publish(ctx, second); err != nil {
			t.Fatal(err)
		}
		set, err = store.LoadAll(ctx)
		if err != nil || len(set.Snapshots) != 1 || len(set.Snapshots[0].Sessions) != 1 || set.Snapshots[0].Sessions[0].RequestCount != 9 {
			t.Fatalf("delta load = %+v, %v", set, err)
		}
		if err := store.Leave(ctx); err != nil {
			t.Fatal(err)
		}
		if _, _, _, full, planErr := store.plan(second); planErr != nil || !full {
			t.Fatalf("plan after leave: full=%v err=%v", full, planErr)
		}
		if err := store.Leave(ctx); err != nil {
			t.Fatal(err)
		}
		set, err = store.LoadAll(ctx)
		if err != nil || len(set.Members) != 0 {
			t.Fatalf("after leave = %+v, %v", set, err)
		}
	})

	t.Run("publisher cap counts only live roster", func(t *testing.T) {
		prefix := "test:stelem:" + uuid.NewString()
		store := newStore(prefix, "reader")
		store.cfg.MaxPublishers = 2
		t.Cleanup(func() { _ = client.Del(ctx, store.rosterKey()).Err() })
		observedAt := time.Now()
		staleScore := observedAt.Add(-store.cfg.MembershipTTL - store.cfg.MembershipTTL/2).UnixNano()
		if err := client.ZAdd(ctx, store.rosterKey(),
			redis.Z{Score: float64(staleScore), Member: "stale-1"},
			redis.Z{Score: float64(staleScore), Member: "stale-2"},
			redis.Z{Score: float64(observedAt.UnixNano()), Member: "live-1"},
		).Err(); err != nil {
			t.Fatal(err)
		}
		set, err := store.LoadAll(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if set.Truncated || hasPublisherReason(set.Errors, "", "publisher_cap") {
			t.Fatalf("stale roster entries triggered cap: %+v", set)
		}
		if err := client.ZAdd(ctx, store.rosterKey(),
			redis.Z{Score: float64(observedAt.UnixNano()), Member: "live-2"},
			redis.Z{Score: float64(observedAt.UnixNano()), Member: "live-3"},
		).Err(); err != nil {
			t.Fatal(err)
		}
		set, err = store.LoadAll(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !set.Truncated || !hasPublisherReason(set.Errors, "", "publisher_cap") {
			t.Fatalf("live roster cap not reported: %+v", set)
		}
	})

	t.Run("two publishers missing meta oversized and pruning", func(t *testing.T) {
		prefix := "test:stelem:" + uuid.NewString()
		one, two := newStore(prefix, "p1"), newStore(prefix, "p2")
		one.cfg.MembershipTTL, two.cfg.MembershipTTL = 100*time.Millisecond, 100*time.Millisecond
		t.Cleanup(func() {
			keys := []string{one.rosterKey(), one.snapshotKey("p1"), one.snapshotKey("p2"), one.snapshotKey("missing"), one.snapshotKey("oversized"), one.snapshotKey("crashed")}
			_ = client.Del(ctx, keys...).Err()
		})
		now := time.Now()
		if err := one.Publish(ctx, Snapshot{PublisherID: "p1", NodeID: "n1", CapturedAt: now, Sessions: []SessionView{{SessionID: "one"}}}); err != nil {
			t.Fatal(err)
		}
		if err := two.Publish(ctx, Snapshot{PublisherID: "p2", NodeID: "n2", CapturedAt: now, Sessions: []SessionView{{SessionID: "two"}}}); err != nil {
			t.Fatal(err)
		}
		set, err := one.LoadAll(ctx)
		if err != nil || len(set.Snapshots) != 2 {
			t.Fatalf("two publisher load = %+v, %v", set, err)
		}

		if err := client.ZAdd(ctx, one.rosterKey(), redis.Z{Score: float64(time.Now().UnixNano()), Member: "missing"}).Err(); err != nil {
			t.Fatal(err)
		}
		set, err = one.LoadAll(ctx)
		if err != nil || !hasPublisherReason(set.Errors, "missing", "meta_missing") {
			t.Fatalf("missing meta = %+v, %v", set, err)
		}

		one.cfg.MaxSessions, one.cfg.MaxTransfers = 1, 1
		oversized := map[string]any{}
		for i := 0; i < 19; i++ {
			oversized[fmt.Sprintf("junk:%02d", i)] = "x"
		}
		if err := client.HSet(ctx, one.snapshotKey("oversized"), oversized).Err(); err != nil {
			t.Fatal(err)
		}
		if err := client.ZAdd(ctx, one.rosterKey(), redis.Z{Score: float64(time.Now().UnixNano()), Member: "oversized"}).Err(); err != nil {
			t.Fatal(err)
		}
		set, err = one.LoadAll(ctx)
		if err != nil || !hasPublisherReason(set.Errors, "oversized", "oversized") {
			t.Fatalf("oversized = %+v, %v", set, err)
		}

		old := time.Now().Add(-300 * time.Millisecond)
		if err := client.ZAdd(ctx, one.rosterKey(), redis.Z{Score: float64(old.UnixNano()), Member: "crashed"}).Err(); err != nil {
			t.Fatal(err)
		}
		if err := one.Publish(ctx, Snapshot{PublisherID: "p1", NodeID: "n1", CapturedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if score := client.ZScore(ctx, one.rosterKey(), "crashed"); !errors.Is(score.Err(), redis.Nil) {
			t.Fatalf("crashed publisher remains: %v", score.Err())
		}
	})
}

func hasPublisherReason(problems []PublisherError, publisher, reason string) bool {
	for _, problem := range problems {
		if problem.PublisherID == publisher && problem.Reason == reason {
			return true
		}
	}
	return false
}
