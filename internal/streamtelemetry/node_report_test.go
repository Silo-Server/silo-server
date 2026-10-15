package streamtelemetry

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func nodeReportSnapshot(at time.Time) Snapshot {
	return Snapshot{
		PublisherID: "node:5", NodeID: "gpu-1", PublisherEpoch: 77, Sequence: 9, CapturedAt: at,
		DroppedObservations: 2, UnattributedBytes: 10,
		Sessions: []SessionView{
			{SessionID: "s1", Subject: UserSubject(3), ProfileID: "p", MediaFileID: 9, PlayMethod: "transcode",
				StartedAt: at.Add(-time.Hour), StartedAtSource: StartedAtSourceClaim, BytesAccepted: 4096,
				LastByteAccepted: at.Add(-time.Second), RequestCount: 4,
				Routes: []RouteActivityView{{Method: "GET", Pattern: "/transcode/{session_id}/segment/{name}", Requests: 4, BytesAccepted: 4096, LastByteAccepted: at.Add(-time.Second)}}},
			{SessionID: "s2", Subject: UserSubject(4), LastObservationEnd: at.Add(-2 * time.Second)},
		},
		Transfers: []TransferView{{ID: "t1", Subject: UserSubject(3), MediaFileID: 9, Method: "GET", Pattern: "/downloads/artifacts/{artifact_id}", BytesAccepted: 10, LastByteAccepted: at}},
	}
}

// What the core stores for a node is exactly what the node's own Redis
// publisher stored: the same hash fields under the same key, the same roster
// entry. Telemetry readers therefore cannot tell the two apart.
func TestIngestNodeSnapshotStoresWhatPublishWould(t *testing.T) {
	client := redisTestClient(t)
	ctx := context.Background()
	cfg := testRedisStoreConfig("node:5")
	at := time.Unix(0, time.Now().UnixNano())
	snapshot := nodeReportSnapshot(at)

	publisher := NewRedisStore(client, cfg, slog.New(slog.DiscardHandler))
	if err := publisher.Publish(ctx, snapshot); err != nil {
		t.Fatalf("publish: %v", err)
	}
	key := publisher.snapshotKey("node:5")
	want := client.HGetAll(ctx, key).Val()
	wantScore := client.ZScore(ctx, publisher.rosterKey(), "node:5").Val()
	client.Del(ctx, key, publisher.rosterKey())

	wire, omitted, err := EncodeNodeSnapshot(snapshot, 2000, 2000)
	if err != nil || omitted {
		t.Fatalf("encode: omitted=%v err=%v", omitted, err)
	}
	// Through JSON, as the report travels.
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var received NodeSnapshot
	if err := json.Unmarshal(encoded, &received); err != nil {
		t.Fatal(err)
	}
	ingester := NewRedisStore(client, testRedisStoreConfigWithPrefix(cfg.KeyPrefix), slog.New(slog.DiscardHandler))
	if err := ingester.IngestNodeSnapshot(ctx, "node:5", "gpu-1", 77, 9, received, at, at.Add(30*time.Second)); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	t.Cleanup(func() { client.Del(context.Background(), key, publisher.rosterKey()) })
	if got := client.HGetAll(ctx, key).Val(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ingested hash\n%v\nwant the publisher's\n%v", got, want)
	}
	if got := client.ZScore(ctx, publisher.rosterKey(), "node:5").Val(); got != wantScore {
		t.Fatalf("roster score = %v, want %v", got, wantScore)
	}
	set, err := ingester.LoadAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Errors) != 0 || len(set.Snapshots) != 1 || len(set.Snapshots[0].Sessions) != 2 {
		t.Fatalf("readers see %+v", set)
	}
}

func testRedisStoreConfigWithPrefix(prefix string) Config {
	cfg := testRedisStoreConfig("")
	cfg.KeyPrefix = prefix
	return cfg
}

// The core owns the publisher's identity and clock: the roster score and the
// pruning cutoff are the core's now, and nothing a node reports can date
// itself later than latest, so a node cannot evict other publishers.
func TestIngestNodeSnapshotUsesTheCoresClock(t *testing.T) {
	client := redisTestClient(t)
	ctx := context.Background()
	cfg := testRedisStoreConfig("")
	store := NewRedisStore(client, cfg, slog.New(slog.DiscardHandler))
	t.Cleanup(func() {
		client.Del(context.Background(), store.rosterKey(), store.snapshotKey("node:5"), store.snapshotKey("api-1"))
	})
	now := time.Unix(0, time.Now().UnixNano())
	// Another publisher last heard from 90 s ago: inside the two-TTL roster
	// window, so a sweep keyed on the core's clock keeps it.
	client.ZAdd(ctx, store.rosterKey(), redis.Z{Score: float64(now.Add(-90 * time.Second).UnixNano()), Member: "api-1"})

	future := now.Add(24 * time.Hour)
	snapshot := nodeReportSnapshot(future)
	for i := range snapshot.Sessions {
		snapshot.Sessions[i].LastByteAccepted = future
		snapshot.Sessions[i].StartedAt = future
	}
	wire, _, err := EncodeNodeSnapshot(snapshot, 2000, 2000)
	if err != nil {
		t.Fatal(err)
	}
	latest := now.Add(30 * time.Second)
	if err := store.IngestNodeSnapshot(ctx, "node:5", "gpu-1", 1, 1, wire, now, latest); err != nil {
		t.Fatal(err)
	}
	if score := client.ZScore(ctx, store.rosterKey(), "node:5").Val(); score != float64(now.UnixNano()) {
		t.Fatalf("roster score = %v, want the core's now %v", score, float64(now.UnixNano()))
	}
	if err := client.ZScore(ctx, store.rosterKey(), "api-1").Err(); err != nil {
		t.Fatalf("another publisher was pruned: %v", err)
	}
	stored, _, err := decodeSnapshotHash("node:5", client.HGetAll(ctx, store.snapshotKey("node:5")).Val(), 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.CapturedAt.Equal(now) {
		t.Fatalf("captured at %v, want the core's now %v", stored.CapturedAt, now)
	}
	for _, session := range stored.Sessions {
		if session.LastByteAccepted.After(latest) || session.StartedAt.After(latest) || session.Routes != nil && session.Routes[0].LastByteAccepted.After(latest) {
			t.Fatalf("session %s keeps a time past %v: %+v", session.SessionID, latest, session)
		}
	}
}

func TestEncodeNodeSnapshotKeepsTheNewest(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	snapshot := Snapshot{
		Sessions: []SessionView{
			{SessionID: "old", LastByteAccepted: at.Add(-time.Hour)},
			{SessionID: "new", LastByteAccepted: at},
			{SessionID: "mid", LastObservationEnd: at.Add(-time.Minute)},
		},
		Transfers: []TransferView{{ID: "t-old", LastByteAccepted: at.Add(-time.Hour)}, {ID: "t-new", LastObservationEnd: at}},
	}
	wire, omitted, err := EncodeNodeSnapshot(snapshot, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !omitted || !wire.Truncated {
		t.Fatalf("omitted=%v truncated=%v, want both", omitted, wire.Truncated)
	}
	var ids []string
	for _, raw := range wire.Sessions {
		session, err := decodeSession(raw)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, session.SessionID)
	}
	transfer, err := decodeTransfer(wire.Transfers[0])
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, []string{"new", "mid"}) || len(wire.Transfers) != 1 || transfer.ID != "t-new" {
		t.Fatalf("kept sessions %v and transfer %s, want the newest", ids, transfer.ID)
	}
}

func TestIngestNodeSnapshotRefusesInvalidRecords(t *testing.T) {
	// Validation precedes any Redis call, so this client is never dialed.
	store := NewRedisStore(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}), testRedisStoreConfig(""), slog.New(slog.DiscardHandler))
	session, err := encodeSession(SessionView{SessionID: "dup"})
	if err != nil {
		t.Fatal(err)
	}
	for name, wire := range map[string]NodeSnapshot{
		"duplicate session": {Sessions: []json.RawMessage{session, session}},
		"garbage session":   {Sessions: []json.RawMessage{json.RawMessage(`{"v":99}`)}},
		"negative counter":  {DroppedBytes: -1},
	} {
		err := store.IngestNodeSnapshot(context.Background(), "node:1", "n", 1, 1, wire, time.Now(), time.Now())
		if !errors.Is(err, ErrInvalidNodeSnapshot) {
			t.Fatalf("%s: %v, want ErrInvalidNodeSnapshot", name, err)
		}
	}
}
