package nodecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/logstream"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/opslog"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

func controlTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("SILO_TEST_REDIS_URL")
	if url == "" {
		t.Skip("SILO_TEST_REDIS_URL is not set")
	}
	options, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("parse SILO_TEST_REDIS_URL: %v", err)
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// recordingBus records what is published.
type recordingBus struct {
	mu     sync.Mutex
	events []string
}

func (b *recordingBus) Publish(_ context.Context, channel string, event cache.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, channel+" "+event.Type+" "+event.Payload)
	return nil
}
func (b *recordingBus) Subscribe(context.Context, string, cache.EventHandler) error { return nil }
func (b *recordingBus) Close() error                                                { return nil }

func (b *recordingBus) published() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.events...)
}

// controlFixture is the core's node control endpoints, mounted as the API
// router mounts them, for one registered transcode node with a real key, a
// real credential registry, Postgres and Redis.
type controlFixture struct {
	t         *testing.T
	repo      *nodepool.Repository
	registry  *nodepool.KeyRegistry
	node      *nodepool.Node
	key       string
	rdb       *redis.Client
	bus       *recordingBus
	hub       *logstream.Hub
	telemetry *streamtelemetry.RedisStore
	settings  map[string]string
	handler   http.Handler
}

func newControlFixture(t *testing.T) *controlFixture {
	t.Helper()
	pool := newControlTestPool(t)
	rdb := controlTestRedis(t)
	ctx := context.Background()
	repo := nodepool.NewRepository(pool)
	cipher, err := secret.New([]byte("node-control-test-master-key-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	suffix := time.Now().UnixNano()
	node, err := repo.Create(ctx, nodepool.CreateNodeInput{Name: fmt.Sprintf("control-%d", suffix), Type: nodepool.NodeTypeTranscode,
		URL: fmt.Sprintf("http://control-%d:8080", suffix)})
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
	telemetryConfig.KeyPrefix = fmt.Sprintf("test:stelem:control:%d", suffix)
	f := &controlFixture{
		t: t, repo: repo, registry: registry, node: node, key: key, rdb: rdb, bus: &recordingBus{},
		hub:       logstream.NewHub("api", nil),
		telemetry: streamtelemetry.NewRedisStore(rdb, telemetryConfig, slog.New(slog.DiscardHandler)),
		settings: map[string]string{
			"playback.ffmpeg_path":               "/opt/ffmpeg/bin/ffmpeg",
			"playback.hw_accel":                  "qsv",
			"playback.hw_device":                 "/dev/dri/renderD128",
			"playback.transcode_dir":             "/var/silo/transcode",
			"playback.segment_retention_seconds": "900",
			"download.artifact_dir":              "/var/silo/artifacts",
			"download.artifact_max_bytes":        "1000",
			"subtitles.sync_node_capacity":       "3",
			"server.log_level":                   "debug",
			"server.log_format":                  "json",
			"opslog.capture_level":               " Warn ",
			"auth.jwt_secret":                    "must-never-reach-a-node",
		},
	}
	server := NewServer(Options{
		Nodes:     repo,
		Settings:  func(context.Context) (map[string]string, error) { return maps.Clone(f.settings), nil },
		Redis:     rdb,
		Telemetry: f.telemetry,
		Logs:      opslog.NewConsumer(pool, f.hub),
		Events:    f.bus,
	})
	f.handler = MountForTest(server, registry)
	t.Cleanup(func() {
		ctx := context.Background()
		index := "silo:nodesessions:" + strconv.Itoa(node.ID)
		keys, _ := rdb.SMembers(ctx, index).Result()
		keys = append(keys, index, reportFenceKey(node.ID))
		if prefixed, err := rdb.Keys(ctx, telemetryConfig.KeyPrefix+":*").Result(); err == nil {
			keys = append(keys, prefixed...)
		}
		rdb.Del(ctx, keys...)
		_, _ = pool.Exec(ctx, `DELETE FROM operational_logs WHERE node_id = $1`, node.Name)
		_ = repo.Delete(ctx, node.ID)
	})
	return f
}

// MountForTest mounts the handlers the way internal/api's router does.
func MountForTest(server *Server, keys KeySource) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+PathConfig, server.HandleConfig)
	mux.HandleFunc("POST "+PathReport, server.HandleReport)
	mux.HandleFunc("POST "+PathLogs, server.HandleLogs)
	mux.HandleFunc("POST "+PathHealthChanged, server.HandleHealthChanged)
	return RequireNode(keys)(mux)
}

// serve sends a request signed as the fixture's node.
func (f *controlFixture) serve(method, path string, body any, header http.Header) *httptest.ResponseRecorder {
	f.t.Helper()
	var reader *bytes.Reader
	switch v := body.(type) {
	case nil:
		reader = bytes.NewReader(nil)
	case []byte:
		reader = bytes.NewReader(v)
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			f.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	r := httptest.NewRequest(method, path, reader)
	for name, values := range header {
		r.Header[name] = values
	}
	nodeauthtest.SignFromNode(f.t, r, f.node.ID, f.key)
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, r)
	return recorder
}

func (f *controlFixture) config() (NodeConfig, string) {
	f.t.Helper()
	got := f.serve(http.MethodGet, PathConfig, nil, nil)
	if got.Code != http.StatusOK {
		f.t.Fatalf("config: %d %s", got.Code, got.Body.String())
	}
	var doc NodeConfig
	if err := json.Unmarshal(got.Body.Bytes(), &doc); err != nil {
		f.t.Fatal(err)
	}
	return doc, got.Header().Get("ETag")
}

func TestConfigServesTheNodesDocument(t *testing.T) {
	f := newControlFixture(t)
	doc, etag := f.config()
	if etag != `"`+doc.Revision+`"` {
		t.Fatalf("ETag %q does not name revision %q", etag, doc.Revision)
	}
	recomputed, err := newNodeConfig(doc.NodeConfigBody)
	if err != nil || recomputed.Revision != doc.Revision {
		t.Fatalf("revision %q is not the hash of the body (%q)", doc.Revision, recomputed.Revision)
	}
	want := NodeConfigBody{
		Node:         NodeIdentity{ID: f.node.ID, Name: f.node.Name, URL: f.node.URL},
		SourceAccess: "local",
		Playback: PlaybackConfig{FFmpegPath: "/opt/ffmpeg/bin/ffmpeg", HWAccel: "qsv", HWDevice: "/dev/dri/renderD128",
			TranscodeDir: "/var/silo/transcode", SegmentRetentionSeconds: 900},
		Download:  DownloadConfig{ArtifactDir: "/var/silo/artifacts", ArtifactMaxBytes: 1000},
		Subtitles: SubtitlesConfig{SyncNodeCapacity: 3},
		Logging:   LoggingConfig{Level: "debug", Format: "json", CaptureLevel: "warn"},
	}
	if doc.NodeConfigBody != want {
		t.Fatalf("document\n%+v\nwant\n%+v", doc.NodeConfigBody, want)
	}
	if bytes.Contains(f.serve(http.MethodGet, PathConfig, nil, nil).Body.Bytes(), []byte("must-never-reach-a-node")) {
		t.Fatal("the configuration carries the JWT secret")
	}

	// Stable while nothing changes, and 304 for the current revision.
	again, _ := f.config()
	if again.Revision != doc.Revision {
		t.Fatalf("revision changed from %q to %q with nothing changed", doc.Revision, again.Revision)
	}
	notModified := f.serve(http.MethodGet, PathConfig, nil, http.Header{"If-None-Match": {etag}})
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 || notModified.Header().Get("ETag") != etag {
		t.Fatalf("If-None-Match current revision: %d %q", notModified.Code, notModified.Body.String())
	}
}

// The node's own row decides what its overrides cover, and every change
// moves the revision.
func TestConfigAppliesTheNodesRow(t *testing.T) {
	f := newControlFixture(t)
	ctx := context.Background()
	before, etag := f.config()

	accel, device, dir, budget := "vaapi", "/dev/dri/renderD129", "/srv/node-artifacts", int64(5000)
	if _, err := f.repo.Update(ctx, f.node.ID, nodepool.UpdateNodeInput{HWAccelOverride: &accel, HWDeviceOverride: &device,
		DownloadArtifactDirOverride: &dir, DownloadArtifactMaxBytesOverride: &budget}); err != nil {
		t.Fatal(err)
	}
	overridden, _ := f.config()
	if overridden.Playback.HWAccel != "vaapi" || overridden.Playback.HWDevice != device ||
		overridden.Download.ArtifactDir != dir || overridden.Download.ArtifactMaxBytes != budget {
		t.Fatalf("overrides not applied: %+v %+v", overridden.Playback, overridden.Download)
	}
	if overridden.Revision == before.Revision {
		t.Fatal("revision did not move with the overrides")
	}
	if got := f.serve(http.MethodGet, PathConfig, nil, http.Header{"If-None-Match": {etag}}); got.Code != http.StatusOK {
		t.Fatalf("stale If-None-Match: %d, want 200", got.Code)
	}

	pool := newControlTestPool(t)
	if _, err := pool.Exec(ctx, `UPDATE stream_nodes SET source_access = 'core' WHERE id = $1`, f.node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.SetDrainRequested(ctx, f.node.ID, true); err != nil {
		t.Fatal(err)
	}
	moved, _ := f.config()
	if moved.SourceAccess != "core" || !moved.DrainRequested || moved.Revision == overridden.Revision {
		t.Fatalf("source access %q, drain %v, revision moved %v", moved.SourceAccess, moved.DrainRequested, moved.Revision != overridden.Revision)
	}

	unsigned := httptest.NewRecorder()
	f.handler.ServeHTTP(unsigned, httptest.NewRequest(http.MethodGet, PathConfig, nil))
	if unsigned.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned config read: %d, want 401", unsigned.Code)
	}
}

func testReport(boot string, seq uint64, sessions ...string) Report {
	report := Report{BootID: boot, Seq: seq}
	for _, id := range sessions {
		report.Sessions = append(report.Sessions, nodesessions.SessionInfo{SessionID: id, Type: "transcode", NodeURL: "http://spoofed", NodeName: "spoofed"})
	}
	return report
}

func (f *controlFixture) sessionIDs() []string {
	f.t.Helper()
	listed, err := nodesessions.ListAll(context.Background(), f.rdb, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	var ids []string
	for _, row := range listed.Sessions {
		if row.NodeURL == f.node.URL {
			if row.NodeName != f.node.Name {
				f.t.Fatalf("record names node %q, want the registered %q", row.NodeName, f.node.Name)
			}
			ids = append(ids, row.SessionID)
		}
	}
	return ids
}

// The fence takes a report only when (boot, seq) is newer: replays, reordered
// reports and an old boot after a new one are refused and change nothing.
func TestReportFenceRefusesStaleReports(t *testing.T) {
	f := newControlFixture(t)
	post := func(report Report) int { return f.serve(http.MethodPost, PathReport, report, nil).Code }
	const boot, newerBoot = "1800000000000000000", "1800000000000000001"

	if code := post(testReport(boot, 1, "a")); code != http.StatusNoContent {
		t.Fatalf("first report: %d", code)
	}
	if code := post(testReport(boot, 1, "replayed")); code != http.StatusConflict {
		t.Fatalf("replay: %d, want 409", code)
	}
	if code := post(testReport(boot, 3, "c")); code != http.StatusNoContent {
		t.Fatalf("seq 3: %d", code)
	}
	if code := post(testReport(boot, 2, "late")); code != http.StatusConflict {
		t.Fatalf("reordered seq 2: %d, want 409", code)
	}
	if ids := f.sessionIDs(); fmt.Sprint(ids) != "[c]" {
		t.Fatalf("sessions after refused reports = %v, want [c]", ids)
	}
	if code := post(testReport(newerBoot, 1, "restarted")); code != http.StatusNoContent {
		t.Fatalf("new boot: %d", code)
	}
	if code := post(testReport(boot, 100, "old-boot")); code != http.StatusConflict {
		t.Fatalf("old boot after a new one: %d, want 409", code)
	}
	if ids := f.sessionIDs(); fmt.Sprint(ids) != "[restarted]" {
		t.Fatalf("sessions = %v, want [restarted]", ids)
	}
	for _, bad := range []string{"", "0", "-1", "01", "x", "18446744073709551615"} {
		if code := post(testReport(bad, 200)); code != http.StatusBadRequest {
			t.Fatalf("boot_id %q: %d, want 400", bad, code)
		}
	}
}

// The fence compares decimal strings by length, then lexically, since Lua
// numbers cannot hold a nanosecond boot id: a sequence or boot id gaining or
// losing a digit must still order numerically.
func TestReportFenceOrdersAcrossDigitLengths(t *testing.T) {
	f := newControlFixture(t)
	type step struct {
		boot string
		seq  uint64
		want int
	}
	run := func(steps ...step) {
		t.Helper()
		f.rdb.Del(context.Background(), reportFenceKey(f.node.ID))
		for _, s := range steps {
			if code := f.serve(http.MethodPost, PathReport, testReport(s.boot, s.seq), nil).Code; code != s.want {
				t.Fatalf("boot %s seq %d: %d, want %d", s.boot, s.seq, code, s.want)
			}
		}
	}
	t.Cleanup(func() { f.rdb.Del(context.Background(), reportFenceKey(f.node.ID)) })
	run(
		step{"9", 9, http.StatusNoContent},
		step{"9", 10, http.StatusNoContent}, // seq 9 -> 10
		step{"9", 9, http.StatusConflict},   // seq 10 -> 9
		step{"9", 100, http.StatusNoContent},
		step{"99", 1, http.StatusNoContent},
		step{"1000", 1, http.StatusNoContent}, // boot 99 -> 1000
		step{"999", 1000, http.StatusConflict},
		step{"99", 1, http.StatusConflict},
	)
	run(
		step{"999", 5, http.StatusNoContent},
		step{"1000", 1, http.StatusNoContent}, // boot 999 -> 1000
		step{"999", 6, http.StatusConflict},
	)
}

// identityDirectory names every node as one transcode node.
type identityDirectory struct{ node nodepool.Identity }

func (d identityDirectory) GetByID(context.Context, int) (*nodepool.Node, error) {
	return nil, nodepool.ErrNodeNotFound
}

func (d identityDirectory) GetIdentity(context.Context, int) (nodepool.Identity, error) {
	return d.node, nil
}

// During a Redis outage every report fails. The core says so once a minute
// per node, not once a report: its own warnings go to the operational log.
func TestReportWarningsDuringARedisOutageAreRateLimited(t *testing.T) {
	warnings := captureWarnings(t)
	down := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 200 * time.Millisecond})
	t.Cleanup(func() { _ = down.Close() })
	srv := NewServer(Options{Nodes: identityDirectory{node: nodepool.Identity{ID: 9, Name: "gpu-9", Type: nodepool.NodeTypeTranscode}}, Redis: down})
	clock := time.Now()
	srv.now = func() time.Time { return clock }
	var seq uint64
	post := func() {
		t.Helper()
		seq++
		body, err := json.Marshal(testReport("1800000000000000000", seq, "s1"))
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, PathReport, bytes.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), nodeIDContextKey{}, 9))
		recorder := httptest.NewRecorder()
		srv.HandleReport(recorder, r)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("report during the outage: %d, want 503", recorder.Code)
		}
	}
	for range 5 {
		post()
	}
	if n := warnings.count("fence node report"); n != 1 {
		t.Fatalf("%d warnings for five failed reports, want 1", n)
	}
	clock = clock.Add(reportWarnInterval)
	post()
	if n := warnings.count("fence node report"); n != 2 {
		t.Fatalf("%d warnings a minute later, want 2", n)
	}
}

// What the report carries lands where today's readers look, under the
// identity and clock of the core.
func TestReportWritesUnderTheCoresIdentity(t *testing.T) {
	f := newControlFixture(t)
	now := time.Now()
	snapshot := streamtelemetry.Snapshot{
		PublisherID: "api-replica-1", NodeID: "spoofed", CapturedAt: now.Add(24 * time.Hour),
		Sessions:  []streamtelemetry.SessionView{{SessionID: "s1", Subject: streamtelemetry.UserSubject(3), LastByteAccepted: now.Add(24 * time.Hour)}},
		Transfers: []streamtelemetry.TransferView{{ID: "t1", Method: "GET", Pattern: "/downloads/artifacts/{artifact_id}"}},
	}
	// A bound of one transfer for two leaves one out: the snapshot is
	// truncated, and the stored view must say so.
	snapshot.Transfers = append(snapshot.Transfers, streamtelemetry.TransferView{ID: "t0"})
	wire, omitted, err := streamtelemetry.EncodeNodeSnapshot(snapshot, MaxReportSessions, 1)
	if err != nil || !omitted {
		t.Fatalf("encode: omitted=%v err=%v", omitted, err)
	}
	report := testReport("1800000000000000000", 1, "s1")
	report.Telemetry = &wire
	report.Truncated = true
	if got := f.serve(http.MethodPost, PathReport, report, nil); got.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", got.Code, got.Body.String())
	}
	if ids := f.sessionIDs(); fmt.Sprint(ids) != "[s1]" {
		t.Fatalf("session records = %v", ids)
	}
	set, err := f.telemetry.LoadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Errors) != 0 || len(set.Snapshots) != 1 {
		t.Fatalf("telemetry readers see %+v", set)
	}
	stored := set.Snapshots[0]
	if stored.PublisherID != PublisherID(f.node.ID) || stored.NodeID != f.node.Name {
		t.Fatalf("stored as publisher %q node %q, want %q %q", stored.PublisherID, stored.NodeID, PublisherID(f.node.ID), f.node.Name)
	}
	if stored.CapturedAt.After(time.Now()) || stored.Sessions[0].LastByteAccepted.After(time.Now().Add(maxClockSkew)) || !stored.Truncated {
		t.Fatalf("stored snapshot keeps the node's clock or loses truncation: %+v", stored)
	}
}

func TestReportRefusesMoreThanItsBounds(t *testing.T) {
	f := newControlFixture(t)
	ids := make([]string, MaxReportSessions+1)
	for i := range ids {
		ids[i] = strconv.Itoa(i)
	}
	if got := f.serve(http.MethodPost, PathReport, testReport("1800000000000000000", 1, ids...), nil); got.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the session bound: %d, want 413", got.Code)
	}
	if ids := f.sessionIDs(); len(ids) != 0 {
		t.Fatalf("an over-bound report wrote %d sessions", len(ids))
	}
}

// Shipped entries are filed under the authenticated node, whatever node id
// they carry, and reach the live tail.
func TestLogsStampTheAuthenticatedNode(t *testing.T) {
	f := newControlFixture(t)
	tail, unsubscribe := f.hub.Subscribe(nil)
	defer unsubscribe()
	message := fmt.Sprintf("node log %d", time.Now().UnixNano())
	batch := LogBatch{Entries: []opslog.Entry{
		{Timestamp: time.Now(), Level: "INFO", Component: "transcodenode", Message: message, NodeID: "spoofed-node"},
		{Timestamp: time.Now(), Level: "WARN", Component: "ffmpeg", Message: message + " stderr"},
	}}
	if got := f.serve(http.MethodPost, PathLogs, batch, nil); got.Code != http.StatusNoContent {
		t.Fatalf("logs: %d %s", got.Code, got.Body.String())
	}
	pool := newControlTestPool(t)
	rows, err := pool.Query(context.Background(), `SELECT node_id FROM operational_logs WHERE message LIKE $1`, message+"%")
	if err != nil {
		t.Fatal(err)
	}
	var nodeIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		nodeIDs = append(nodeIDs, id)
	}
	rows.Close()
	// Names are not unique; the row id makes the field name one node.
	want := f.node.Name + "#" + strconv.Itoa(f.node.ID)
	if len(nodeIDs) != 2 || nodeIDs[0] != want || nodeIDs[1] != want {
		t.Fatalf("stored node ids = %v, want both %q", nodeIDs, want)
	}
	select {
	case msg := <-tail:
		var entry opslog.EntryRow
		if err := json.Unmarshal(msg.Entry, &entry); err != nil || entry.NodeID != want {
			t.Fatalf("live tail entry %s (%v), want node %q", msg.Entry, err, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing reached the live tail")
	}

	tooMany := LogBatch{Entries: make([]opslog.Entry, MaxLogBatchEntries+1)}
	if got := f.serve(http.MethodPost, PathLogs, tooMany, nil); got.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the entry bound: %d, want 413", got.Code)
	}
}

func TestHealthChangedAnnouncesTheNodesDrainChange(t *testing.T) {
	f := newControlFixture(t)
	if got := f.serve(http.MethodPost, PathHealthChanged, []byte(`{"node_id":1}`), nil); got.Code != http.StatusNoContent {
		t.Fatalf("health-changed: %d %s", got.Code, got.Body.String())
	}
	want := cache.ChannelAdmin + " " + cache.EventNodeDrainChanged + " " + strconv.Itoa(f.node.ID)
	if got := f.bus.published(); len(got) != 1 || got[0] != want {
		t.Fatalf("published %v, want [%s]", got, want)
	}
}

// identityRegistry is a credential registry holding one node.
type identityRegistry struct{ node nodepool.Identity }

func (r identityRegistry) Identity(id int) (nodepool.Identity, bool) {
	return r.node, id == r.node.ID
}

// downDirectory is a node table that cannot be read.
type downDirectory struct{}

func (downDirectory) GetByID(context.Context, int) (*nodepool.Node, error) {
	return nil, errors.New("database unavailable")
}

func (downDirectory) GetIdentity(context.Context, int) (nodepool.Identity, error) {
	return nodepool.Identity{}, errors.New("database unavailable")
}

// Every report and log batch names its node. With Postgres down the registry
// still answers, so neither is dropped for want of a name; a node it misses
// falls back to the table.
func TestReportsAndLogsReadTheNodeFromTheRegistry(t *testing.T) {
	gpu := nodepool.Identity{ID: 9, Name: "gpu-9", Type: nodepool.NodeTypeTranscode, URL: "http://gpu-9:8080"}
	srv := NewServer(Options{Nodes: downDirectory{}, Identities: identityRegistry{node: gpu}})
	if got, err := srv.identity(t.Context(), 9); err != nil || got != gpu {
		t.Fatalf("identity(9) = %+v, %v; want the registry's", got, err)
	}
	if _, err := srv.identity(t.Context(), 10); err == nil {
		t.Fatal("a node the registry misses did not fall back to the table")
	}
}
