package nodecontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/nodeauth/nodeauthtest"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/opslog"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

// fakeNodes is a stream_nodes table and credential registry in memory: rows by
// id, each transcode row with its own key.
type fakeNodes struct {
	mu   sync.Mutex
	rows map[int]*nodepool.Node
	keys map[int]string
}

func (f *fakeNodes) add(t *testing.T, node nodepool.Node) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rows == nil {
		f.rows, f.keys = map[int]*nodepool.Node{}, map[int]string{}
	}
	f.rows[node.ID] = &node
	if node.Type != nodepool.NodeTypeTranscode {
		return ""
	}
	key, err := nodepool.GenerateNodeKey(node.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.keys[node.ID] = key
	return key
}

func (f *fakeNodes) update(id int, change func(*nodepool.Node)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f.rows[id])
}

func (f *fakeNodes) remove(id int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, id)
	delete(f.keys, id)
}

func (f *fakeNodes) GetByID(_ context.Context, id int) (*nodepool.Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	node, ok := f.rows[id]
	if !ok {
		return nil, nodepool.ErrNodeNotFound
	}
	copied := *node
	return &copied, nil
}

func (f *fakeNodes) Identity(id int) (nodepool.Identity, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	node, ok := f.rows[id]
	if !ok {
		return nodepool.Identity{}, false
	}
	return nodepool.Identity{ID: node.ID, Name: node.Name, Type: node.Type, URL: node.URL}, true
}

// NodeKey answers as nodepool.KeyRegistry does.
func (f *fakeNodes) NodeKey(id int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	node, ok := f.rows[id]
	switch {
	case !ok:
		return "", nodepool.ErrNodeNotFound
	case node.Type != nodepool.NodeTypeTranscode:
		return "", nodepool.ErrNodeKeyNotIssuable
	case f.keys[id] == "":
		return "", nodepool.ErrNoNodeKey
	}
	return f.keys[id], nil
}

func (f *fakeNodes) ReloadNodeKey(id int) (string, error) { return f.NodeKey(id) }

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

// recordingLogs records the batches it is asked to store.
type recordingLogs struct {
	mu      sync.Mutex
	batches map[string][]opslog.Entry
}

func (l *recordingLogs) InsertNodeBatch(_ context.Context, nodeID string, entries []opslog.Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.batches == nil {
		l.batches = map[string][]opslog.Entry{}
	}
	l.batches[nodeID] = append(l.batches[nodeID], entries...)
	return nil
}

// controlFixture is the core's node control routes, mounted as the API router
// mounts them, over an in-memory node table and a miniredis.
type controlFixture struct {
	t         *testing.T
	nodes     *fakeNodes
	redis     *redis.Client
	bus       *recordingBus
	logs      *recordingLogs
	telemetry *streamtelemetry.RedisStore
	settings  map[string]string
	server    *Server
	handler   http.Handler
}

func newControlFixture(t *testing.T) *controlFixture {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	telemetryConfig := streamtelemetry.DefaultConfig("api")
	telemetryConfig.Enabled, telemetryConfig.Distributed = true, true
	f := &controlFixture{
		t: t, nodes: &fakeNodes{}, redis: rdb, bus: &recordingBus{}, logs: &recordingLogs{},
		telemetry: streamtelemetry.NewRedisStore(rdb, telemetryConfig, slog.New(slog.DiscardHandler)),
		settings: map[string]string{
			"playback.ffmpeg_path":               "/opt/ffmpeg/bin/ffmpeg",
			"playback.hw_accel":                  "qsv",
			"playback.hw_device":                 "/dev/dri/renderD128",
			"playback.transcode_dir":             "/var/silo/transcode",
			"playback.segment_retention_seconds": "900",
			"playback.trickplay_node_capacity":   "4",
			"download.artifact_dir":              "/var/silo/artifacts",
			"download.artifact_max_bytes":        "1000",
			"subtitles.sync_node_capacity":       "3",
			"server.log_level":                   "debug",
			"server.log_format":                  "json",
			"server.log_quiet":                   "scanner",
			"opslog.capture_level":               " Warn ",
			"auth.jwt_secret":                    "must-never-reach-a-node",
		},
	}
	f.server = NewServer(Options{
		Nodes:      f.nodes,
		Identities: f.nodes,
		Settings:   func(context.Context) (map[string]string, error) { return maps.Clone(f.settings), nil },
		Redis:      rdb,
		Telemetry:  f.telemetry,
		Logs:       f.logs,
		Events:     f.bus,
	})
	f.handler = mountForTest(f.server, f.nodes)
	return f
}

// mountForTest mounts the handlers the way internal/api's router does.
func mountForTest(server *Server, keys KeySource) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET "+PathConfig, server.LimitNode(http.HandlerFunc(server.HandleConfig)))
	mux.Handle("POST "+PathReport, server.LimitNode(http.HandlerFunc(server.HandleReport)))
	mux.Handle("POST "+PathLogs, server.LimitNode(http.HandlerFunc(server.HandleLogs)))
	mux.Handle("POST "+PathHealthChanged, server.LimitNode(http.HandlerFunc(server.HandleHealthChanged)))
	mux.Handle("POST "+PathResolve, server.LimitNodePlayback(http.HandlerFunc(server.HandleResolve)))
	mux.Handle("POST "+PathSourcesOpen, server.LimitNodePlayback(http.HandlerFunc(server.HandleSourceOpen)))
	mux.Handle("POST "+PathRevokeRecipes, server.LimitNode(http.HandlerFunc(server.HandleRevokeRecipes)))
	// No budget: source reads stream.
	mux.Handle("GET "+PathSources, http.HandlerFunc(server.HandleSource))
	return RequireNode(keys)(mux)
}

// addNode registers a transcode node and returns its key.
func (f *controlFixture) addNode(id int, name string) string {
	return f.nodes.add(f.t, nodepool.Node{ID: id, Name: name, Type: nodepool.NodeTypeTranscode, URL: "http://" + name + ":8080", Enabled: true})
}

// serve sends a request signed as node id with key.
func (f *controlFixture) serve(id int, key, method, path string, body any, header http.Header) *httptest.ResponseRecorder {
	f.t.Helper()
	var encoded []byte
	switch v := body.(type) {
	case nil:
	case []byte:
		encoded = v
	default:
		var err error
		if encoded, err = json.Marshal(v); err != nil {
			f.t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	maps.Copy(r.Header, header)
	nodeauthtest.SignFromNode(f.t, r, id, key)
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, r)
	return recorder
}

func (f *controlFixture) config(id int, key string) (NodeConfig, string) {
	f.t.Helper()
	got := f.serve(id, key, http.MethodGet, PathConfig, nil, nil)
	if got.Code != http.StatusOK {
		f.t.Fatalf("config: %d %s", got.Code, got.Body.String())
	}
	var doc NodeConfig
	if err := json.Unmarshal(got.Body.Bytes(), &doc); err != nil {
		f.t.Fatal(err)
	}
	return doc, got.Header().Get("ETag")
}
