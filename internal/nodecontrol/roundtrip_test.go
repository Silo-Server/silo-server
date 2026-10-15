package nodecontrol_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/logstream"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/opslog"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

type busEvents struct {
	mu     sync.Mutex
	events []cache.Event
}

func (b *busEvents) Publish(_ context.Context, _ string, event cache.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, event)
	return nil
}
func (b *busEvents) Subscribe(context.Context, string, cache.EventHandler) error { return nil }
func (b *busEvents) Close() error                                                { return nil }

// One round trip per call, in process: the core's real handlers behind the
// real credential registry, and the node's real clients — the configuration
// watcher, the reporter, the log shipper and the health notifier — signing
// with the node's issued key.
func TestNodeClientsRoundTripThroughTheCore(t *testing.T) {
	dsn, redisURL := os.Getenv("SILO_TEST_DATABASE_URL"), os.Getenv("SILO_TEST_REDIS_URL")
	if dsn == "" || redisURL == "" {
		t.Skip("SILO_TEST_DATABASE_URL and SILO_TEST_REDIS_URL are required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(options)
	t.Cleanup(func() { _ = rdb.Close() })

	repo := nodepool.NewRepository(pool)
	cipher, err := secret.New([]byte("node-control-roundtrip-master-key-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UnixNano()
	node, err := repo.Create(ctx, nodepool.CreateNodeInput{Name: fmt.Sprintf("roundtrip-%d", suffix), Type: nodepool.NodeTypeTranscode,
		URL: fmt.Sprintf("http://roundtrip-%d:8080", suffix)})
	if err != nil {
		t.Fatal(err)
	}
	key, err := repo.IssueNodeKey(ctx, cipher, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	registry := nodepool.NewKeyRegistry(repo, cipher)
	if err := registry.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	telemetryConfig := streamtelemetry.DefaultConfig("api")
	telemetryConfig.Enabled, telemetryConfig.Distributed = true, true
	telemetryConfig.KeyPrefix = fmt.Sprintf("test:stelem:roundtrip:%d", suffix)
	telemetry := streamtelemetry.NewRedisStore(rdb, telemetryConfig, slog.New(slog.DiscardHandler))
	bus := &busEvents{}
	t.Cleanup(func() {
		ctx := context.Background()
		index := "silo:nodesessions:" + strconv.Itoa(node.ID)
		keys, _ := rdb.SMembers(ctx, index).Result()
		keys = append(keys, index, "silo:nodereport:"+strconv.Itoa(node.ID))
		if prefixed, err := rdb.Keys(ctx, telemetryConfig.KeyPrefix+":*").Result(); err == nil {
			keys = append(keys, prefixed...)
		}
		rdb.Del(ctx, keys...)
		_, _ = pool.Exec(ctx, `DELETE FROM operational_logs WHERE node_id = $1`, node.Name)
		_ = repo.Delete(ctx, node.ID)
	})

	server := nodecontrol.NewServer(nodecontrol.Options{
		Nodes: repo,
		Settings: func(context.Context) (map[string]string, error) {
			return map[string]string{"playback.hw_accel": "vaapi"}, nil
		},
		Redis:     rdb,
		Telemetry: telemetry,
		Logs:      opslog.NewConsumer(pool, logstream.NewHub("api", nil)),
		Events:    bus,
	})
	core := httptest.NewServer(nodecontrol.MountForTest(server, registry))
	defer core.Close()
	client, err := nodecontrol.NewClient(core.URL+"/", key)
	if err != nil {
		t.Fatal(err)
	}

	// Configuration.
	remote := nodeconfig.NewRemote(client, ":8080")
	if err := remote.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if id, ok := remote.NodeRowID(); !ok || id != node.ID || remote.Config().Playback.HWAccel != "vaapi" {
		t.Fatalf("node %d (%v) runs %q, want node %d on vaapi", id, ok, remote.Config().Playback.HWAccel, node.ID)
	}
	first := remote.Config()
	if err := remote.ForceReload(ctx); err != nil || remote.Config() != first {
		t.Fatalf("an unchanged reread: %v, pointer kept %v", err, remote.Config() == first)
	}

	// Report.
	tracker := nodesessions.NewLocalTracker(nil)
	tracker.Track(ctx, nodesessions.SessionInfo{SessionID: "rt-1", Type: "transcode"})
	nodeTelemetryConfig := streamtelemetry.DefaultConfig(node.Name)
	nodeTelemetryConfig.Enabled = true
	nodeTelemetry := streamtelemetry.NewRegistry(nodeTelemetryConfig, streamtelemetry.NewLocalStore(), slog.New(slog.DiscardHandler))
	if err := nodecontrol.NewReporter(client, tracker.Snapshot, nodeTelemetry.Sweep).Flush(ctx); err != nil {
		t.Fatalf("report: %v", err)
	}
	listed, err := nodesessions.ListAll(ctx, rdb, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range listed.Sessions {
		found = found || (row.SessionID == "rt-1" && row.NodeURL == node.URL && row.NodeName == node.Name)
	}
	if !found {
		t.Fatal("the reported session is not where the admin views read sessions")
	}
	set, err := telemetry.LoadAll(ctx)
	if err != nil || len(set.Snapshots) != 1 || set.Snapshots[0].PublisherID != nodecontrol.PublisherID(node.ID) {
		t.Fatalf("telemetry readers see %+v (%v)", set, err)
	}

	// Logs.
	message := fmt.Sprintf("roundtrip log %d", suffix)
	if err := client.ShipLogs(ctx, []opslog.Entry{{Timestamp: time.Now(), Level: "INFO", Component: "transcodenode", Message: message, NodeID: "spoofed"}}); err != nil {
		t.Fatalf("ship logs: %v", err)
	}
	var stamped string
	if err := pool.QueryRow(ctx, `SELECT node_id FROM operational_logs WHERE message = $1`, message).Scan(&stamped); err != nil || stamped != nodecontrol.LogNodeID(node.Name, node.ID) {
		t.Fatalf("stored log entry node %q (%v), want %q", stamped, err, nodecontrol.LogNodeID(node.Name, node.ID))
	}

	// Health change.
	if err := client.NotifyHealthChanged(ctx); err != nil {
		t.Fatalf("health-changed: %v", err)
	}
	bus.mu.Lock()
	defer bus.mu.Unlock()
	if len(bus.events) != 1 || bus.events[0].Type != cache.EventNodeDrainChanged || bus.events[0].Payload != strconv.Itoa(node.ID) {
		t.Fatalf("published %+v", bus.events)
	}
}
