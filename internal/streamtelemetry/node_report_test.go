package streamtelemetry

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
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

// What the core stores from a transcode node's report is exactly what the
// node's own Redis publisher stores: the same hash fields under the same key
// and the same roster entry. Telemetry readers cannot tell the two apart.
func TestIngestNodeSnapshotStoresWhatPublishWould(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = client.Close() })
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
	ingestCfg := testRedisStoreConfig("")
	ingestCfg.KeyPrefix = cfg.KeyPrefix
	ingester := NewRedisStore(client, ingestCfg, slog.New(slog.DiscardHandler))
	if err := ingester.IngestNodeSnapshot(ctx, "node:5", "gpu-1", 77, 9, received, at, at.Add(30*time.Second)); err != nil {
		t.Fatalf("ingest: %v", err)
	}
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
